package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeGoFixture writes the given files into a temp dir and returns the dir plus
// the absolute paths, in the order given.
func writeGoFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

// twoHopFixture reproduces the exact shape J.2b targets, taken from
// migrate-from-legacy/26.1.0_to_26.2.0: a config struct literal reading
// os.Getenv, a constructor taking the base URL as its first parameter and
// storing it on a receiver field, and a request method reading that field.
func twoHopFixture() map[string]string {
	return map[string]string{
		"config.go": `package main

import "os"

type Config struct {
	MySycamoreAPIURL string
	MySycamoreAPIKey string
}

func LoadConfig() *Config {
	return &Config{
		MySycamoreAPIURL: os.Getenv("MYSYCAMORE_API_URL"),
		MySycamoreAPIKey: os.Getenv("MYSYCAMORE_API_KEY"),
	}
}
`,
		"mysycamore_client.go": `package main

import (
	"fmt"
	"net/http"
	"strings"
)

type MySycamoreClient struct {
	baseURL string
	apiKey  string
}

func NewMySycamoreClient(baseURL, apiKey string) *MySycamoreClient {
	return &MySycamoreClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
	}
}

func (c *MySycamoreClient) LookupUserByEmail(email string) error {
	reqURL := fmt.Sprintf("%s/api/v1/users?email=%s", c.baseURL, email)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`,
		"main.go": `package main

func main() {
	config := LoadConfig()
	client := NewMySycamoreClient(config.MySycamoreAPIURL, config.MySycamoreAPIKey)
	_ = client.LookupUserByEmail("a@b.c")
}
`,
	}
}

func TestResolveGoHTTPHosts_TwoHopConstructor(t *testing.T) {
	dir, paths := writeGoFixture(t, twoHopFixture())

	nodes := []graph.Node{
		{
			ID:       "svc:client:lookup",
			Service:  "svc",
			Type:     graph.NodeTypeHTTPClient,
			Language: "go",
			File:     filepath.Join(dir, "mysycamore_client.go"),
			Line:     23, // inside LookupUserByEmail
			Meta:     map[string]string{"method": "GET", "path": "*/api/v1/users", "synthesized": "sprintf_url"},
		},
	}

	changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

	if len(changed) != 1 {
		t.Fatalf("changed = %d nodes, want 1", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "MYSYCAMORE_API_URL" {
		t.Errorf("env_var = %q, want MYSYCAMORE_API_URL", got)
	}
	if got := nodes[0].Meta["host_resolved_via"]; got != "go_env_field" {
		t.Errorf("host_resolved_via = %q, want go_env_field", got)
	}
}

// A key-dynamic node (the matcher's `c.baseURL+path` shape) in the same method
// resolves through the same chain.
func TestResolveGoHTTPHosts_KeyDynamicNode(t *testing.T) {
	dir, paths := writeGoFixture(t, twoHopFixture())

	nodes := []graph.Node{
		{
			ID:       "svc:client:req",
			Service:  "svc",
			Type:     graph.NodeTypeHTTPClient,
			Language: "go",
			File:     filepath.Join(dir, "mysycamore_client.go"),
			Line:     24,
			Meta:     map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "reqURL"},
		},
	}

	ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

	if got := nodes[0].Meta["env_var"]; got != "MYSYCAMORE_API_URL" {
		t.Errorf("env_var = %q, want MYSYCAMORE_API_URL", got)
	}
}

func TestResolveGoHTTPHosts_AmbiguousBaseLeftUnstamped(t *testing.T) {
	files := twoHopFixture()
	// Two call sites wire the same constructor to two different env vars: the
	// deployed target is genuinely unknown here, so nothing may be stamped.
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	primary := NewMySycamoreClient(config.MySycamoreAPIURL, config.MySycamoreAPIKey)
	fallback := NewMySycamoreClient(config.FallbackURL, config.MySycamoreAPIKey)
	_ = primary
	_ = fallback
}
`
	files["config.go"] = `package main

import "os"

type Config struct {
	MySycamoreAPIURL string
	FallbackURL      string
	MySycamoreAPIKey string
}

func LoadConfig() *Config {
	return &Config{
		MySycamoreAPIURL: os.Getenv("MYSYCAMORE_API_URL"),
		FallbackURL:      os.Getenv("MYSYCAMORE_FALLBACK_URL"),
		MySycamoreAPIKey: os.Getenv("MYSYCAMORE_API_KEY"),
	}
}
`
	dir, paths := writeGoFixture(t, files)

	nodes := []graph.Node{
		{
			ID:       "svc:client:lookup",
			Service:  "svc",
			Type:     graph.NodeTypeHTTPClient,
			Language: "go",
			File:     filepath.Join(dir, "mysycamore_client.go"),
			Line:     23,
			Meta:     map[string]string{"method": "GET", "path": "*/api/v1/users"},
		},
	}

	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths}); len(changed) != 0 {
		t.Fatalf("changed = %d nodes, want 0 (ambiguous base must never be guessed)", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "" {
		t.Errorf("env_var = %q, want empty", got)
	}
}

// A request built from a hard-coded URL needs no attribution and must not
// borrow the receiver's env var.
func TestResolveGoHTTPHosts_LiteralURLNotAttributed(t *testing.T) {
	dir, paths := writeGoFixture(t, twoHopFixture())

	nodes := []graph.Node{
		{
			ID:       "svc:client:literal",
			Service:  "svc",
			Type:     graph.NodeTypeHTTPClient,
			Language: "go",
			File:     filepath.Join(dir, "mysycamore_client.go"),
			Line:     23,
			Meta:     map[string]string{"method": "GET", "url": "https://pinned.example.com/api/v1/users"},
		},
	}

	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths}); len(changed) != 0 {
		t.Fatalf("changed = %d nodes, want 0", len(changed))
	}
}

func TestResolveGoHTTPHosts_NoGoNodes_NoOp(t *testing.T) {
	nodes := []graph.Node{
		{ID: "r1", Service: "web", Type: graph.NodeTypeHTTPClient, Language: "ruby",
			File: "/x/a.rb", Meta: map[string]string{"key_dynamic": "true"}},
		{ID: "f1", Service: "web", Type: graph.NodeTypeFunction, Language: "go", File: "/x/a.go"},
	}
	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"web": {"/x/a.go"}}); changed != nil {
		t.Fatalf("changed = %v, want nil (cheap gate must skip the parse)", changed)
	}
}

// A method that reads two env-derived base fields cannot be attributed to one.
func TestResolveGoHTTPHosts_TwoBasesInOneMethodUnstamped(t *testing.T) {
	files := map[string]string{
		"config.go": `package main

import "os"

type Config struct {
	AURL string
	BURL string
}

func LoadConfig() *Config {
	return &Config{AURL: os.Getenv("A_URL"), BURL: os.Getenv("B_URL")}
}
`,
		"client.go": `package main

import "net/http"

type Client struct {
	aBase string
	bBase string
}

func NewClient(a, b string) *Client {
	return &Client{aBase: a, bBase: b}
}

func (c *Client) Sync(path string) error {
	_, err := http.NewRequest("GET", c.aBase+path, nil)
	if err != nil {
		return err
	}
	_, err = http.NewRequest("GET", c.bBase+path, nil)
	return err
}
`,
		"main.go": `package main

func main() {
	cfg := LoadConfig()
	_ = NewClient(cfg.AURL, cfg.BURL)
}
`,
	}
	dir, paths := writeGoFixture(t, files)

	nodes := []graph.Node{
		{
			ID:       "svc:client:sync",
			Service:  "svc",
			Type:     graph.NodeTypeHTTPClient,
			Language: "go",
			File:     filepath.Join(dir, "client.go"),
			Line:     16,
			Meta:     map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "c.aBase+path"},
		},
	}

	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths}); len(changed) != 0 {
		t.Fatalf("changed = %d nodes, want 0 (two env bases in one method is ambiguous)", len(changed))
	}
}
