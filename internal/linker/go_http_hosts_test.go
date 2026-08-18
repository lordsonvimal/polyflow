package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// ── multi-hop forwarding ────────────────────────────────────────────────────

// threeHopFixture is the shape that used to dead-end, taken verbatim from
// migrate-from-legacy/26.1.0_to_26.2.0: main wires a config field into a
// publisher, and the publisher hands its own parameter to the HTTP client's
// constructor. `apiURL` is never assigned anywhere — it exists only as
// NewTilePublisher's parameter — so resolving it needs the fact that callers
// pass DSW_MANAGER_API_URL to that position.
func threeHopFixture() map[string]string {
	return map[string]string{
		"config.go": `package main

import "os"

type Config struct {
	DSWManagerAPIURL string
	DSWAPIKey        string
}

func LoadConfig() *Config {
	return &Config{
		DSWManagerAPIURL: os.Getenv("DSW_MANAGER_API_URL"),
		DSWAPIKey:        os.Getenv("DSW_API_KEY"),
	}
}
`,
		"tile_publisher.go": `package main

type TilePublisher struct {
	client *DSWManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return &TilePublisher{
		client: NewDSWManagerUserAppClient(apiURL, authToken),
	}
}
`,
		"dsw_manager_user_app_client.go": `package main

import (
	"fmt"
	"net/http"
)

type DSWManagerUserAppClient struct {
	baseURL   string
	authToken string
}

func NewDSWManagerUserAppClient(baseURL, authToken string) *DSWManagerUserAppClient {
	return &DSWManagerUserAppClient{baseURL: baseURL, authToken: authToken}
}

func (c *DSWManagerUserAppClient) List() error {
	reqURL := fmt.Sprintf("%s/user-apps", c.baseURL)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`,
		"main.go": `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.DSWManagerAPIURL, config.DSWAPIKey)
}
`,
	}
}

// userAppNode is the */user-apps client node threeHopFixture produces.
func userAppNode(dir string) graph.Node {
	return graph.Node{
		ID:       "svc:client:list",
		Service:  "svc",
		Type:     graph.NodeTypeHTTPClient,
		Language: "go",
		File:     filepath.Join(dir, "dsw_manager_user_app_client.go"),
		Line:     19, // inside List
		Meta:     map[string]string{"method": "GET", "path": "*/user-apps", "synthesized": "sprintf_url"},
	}
}

func TestResolveGoHTTPHosts_ThreeHopConstructor(t *testing.T) {
	t.Parallel()
	dir, paths := writeGoFixture(t, threeHopFixture())
	nodes := []graph.Node{userAppNode(dir)}

	changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

	if len(changed) != 1 {
		t.Fatalf("changed = %d nodes, want 1", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "DSW_MANAGER_API_URL" {
		t.Errorf("env_var = %q, want DSW_MANAGER_API_URL", got)
	}
}

// One more forwarding frame than the motivating case, to prove the fixed point
// iterates rather than hard-coding a depth of three.
func TestResolveGoHTTPHosts_FourHopConstructor(t *testing.T) {
	t.Parallel()
	files := threeHopFixture()
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *DSWManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return newTilePublisherInner(apiURL, authToken)
}

func newTilePublisherInner(baseURL, authToken string) *TilePublisher {
	return &TilePublisher{
		client: NewDSWManagerUserAppClient(baseURL, authToken),
	}
}
`
	dir, paths := writeGoFixture(t, files)
	nodes := []graph.Node{userAppNode(dir)}

	ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

	if got := nodes[0].Meta["env_var"]; got != "DSW_MANAGER_API_URL" {
		t.Errorf("env_var = %q, want DSW_MANAGER_API_URL", got)
	}
}

// forwarderChain builds NewTilePublisher → forward0 → … → forward(n-1) →
// NewDSWManagerUserAppClient, i.e. n intermediate frames between the wired
// constructor and the client's.
func forwarderChain(n int) string {
	var b strings.Builder
	b.WriteString("package main\n\ntype TilePublisher struct {\n\tclient *DSWManagerUserAppClient\n}\n\n")
	b.WriteString("func NewTilePublisher(apiURL, authToken string) *TilePublisher {\n\treturn forward0(apiURL, authToken)\n}\n\n")
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("forward%d(baseURL, authToken)", i+1)
		if i == n-1 {
			next = "&TilePublisher{client: NewDSWManagerUserAppClient(baseURL, authToken)}"
		}
		b.WriteString(fmt.Sprintf(
			"func forward%d(baseURL, authToken string) *TilePublisher {\n\treturn %s\n}\n\n", i, next))
	}
	return b.String()
}

// The hop limit is the whole point of this tier, so pin where it actually
// falls. Each propagation round buys one frame: the direct fact is
// (NewTilePublisher, 0), round k resolves forward(k-1), and one further round
// is needed to reach the client's constructor — so n intermediate frames need
// n+1 rounds and resolve exactly while n+1 <= maxHostHops.
func TestResolveGoHTTPHosts_HopLimitBoundary(t *testing.T) {
	t.Parallel()
	for n := 1; n <= maxHostHops+2; n++ {
		wantResolved := n+1 <= maxHostHops
		t.Run(fmt.Sprintf("%d_frames", n), func(t *testing.T) {
			files := threeHopFixture()
			files["tile_publisher.go"] = forwarderChain(n)
			dir, paths := writeGoFixture(t, files)
			nodes := []graph.Node{userAppNode(dir)}

			ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

			got := nodes[0].Meta["env_var"]
			if wantResolved && got != "DSW_MANAGER_API_URL" {
				t.Errorf("env_var = %q, want DSW_MANAGER_API_URL within the hop limit", got)
			}
			if !wantResolved && got != "" {
				t.Errorf("env_var = %q, want empty past the hop limit (never guess)", got)
			}
		})
	}
}

// Positional agreement alone is not evidence across a frame: a forwarder that
// hands a non-host-named parameter onward must not carry its env var.
func TestResolveGoHTTPHosts_NonHostParamDoesNotForward(t *testing.T) {
	t.Parallel()
	files := threeHopFixture()
	files["config.go"] = `package main

import "os"

type Config struct {
	Token string
}

func LoadConfig() *Config {
	return &Config{Token: os.Getenv("DSW_API_TOKEN")}
}
`
	// `token` is param 0 and is forwarded to the client's baseURL position.
	// Nothing about it says "base URL", so it must not become the host.
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *DSWManagerUserAppClient
}

func NewTilePublisher(token string) *TilePublisher {
	return &TilePublisher{client: NewDSWManagerUserAppClient(token, "")}
}
`
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.Token)
}
`
	dir, paths := writeGoFixture(t, files)
	nodes := []graph.Node{userAppNode(dir)}

	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths}); len(changed) != 0 {
		t.Fatalf("changed = %d nodes, want 0", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "" {
		t.Errorf("env_var = %q, want empty (a token is not a host)", got)
	}
}

// A conflict discovered upstream must not leave a stale downstream derivation:
// two callers wire NewTilePublisher to different env vars, so nothing it
// forwards has support any more.
func TestResolveGoHTTPHosts_ForwardedConflictUnstamped(t *testing.T) {
	t.Parallel()
	files := threeHopFixture()
	files["config.go"] = `package main

import "os"

type Config struct {
	DSWManagerAPIURL string
	FallbackAPIURL   string
	DSWAPIKey        string
}

func LoadConfig() *Config {
	return &Config{
		DSWManagerAPIURL: os.Getenv("DSW_MANAGER_API_URL"),
		FallbackAPIURL:   os.Getenv("DSW_FALLBACK_API_URL"),
		DSWAPIKey:        os.Getenv("DSW_API_KEY"),
	}
}
`
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.DSWManagerAPIURL, config.DSWAPIKey)
	_ = NewTilePublisher(config.FallbackAPIURL, config.DSWAPIKey)
}
`
	dir, paths := writeGoFixture(t, files)
	nodes := []graph.Node{userAppNode(dir)}

	if changed := ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths}); len(changed) != 0 {
		t.Fatalf("changed = %d nodes, want 0 (forwarded ambiguity must not be guessed)", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "" {
		t.Errorf("env_var = %q, want empty", got)
	}
}

// Mutual recursion between forwarders must terminate, not spin.
func TestResolveGoHTTPHosts_RecursiveForwarderTerminates(t *testing.T) {
	t.Parallel()
	files := threeHopFixture()
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *DSWManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return pingBaseURL(apiURL, authToken)
}

func pingBaseURL(baseURL, authToken string) *TilePublisher {
	if baseURL == "" {
		return pongBaseURL(baseURL, authToken)
	}
	return &TilePublisher{client: NewDSWManagerUserAppClient(baseURL, authToken)}
}

func pongBaseURL(baseURL, authToken string) *TilePublisher {
	return pingBaseURL(baseURL, authToken)
}
`
	dir, paths := writeGoFixture(t, files)
	nodes := []graph.Node{userAppNode(dir)}

	ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})

	if got := nodes[0].Meta["env_var"]; got != "DSW_MANAGER_API_URL" {
		t.Errorf("env_var = %q, want DSW_MANAGER_API_URL", got)
	}
}

// Determinism: the fixed point must not depend on map iteration order.
func TestResolveGoHTTPHosts_ForwardingDeterministic(t *testing.T) {
	t.Parallel()
	dir, paths := writeGoFixture(t, threeHopFixture())
	for i := 0; i < 25; i++ {
		nodes := []graph.Node{userAppNode(dir)}
		ResolveGoHTTPHosts(nodes, map[string][]string{"svc": paths})
		if got := nodes[0].Meta["env_var"]; got != "DSW_MANAGER_API_URL" {
			t.Fatalf("run %d: env_var = %q, want DSW_MANAGER_API_URL", i, got)
		}
	}
}
