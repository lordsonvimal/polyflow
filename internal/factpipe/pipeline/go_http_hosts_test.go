package pipeline_test

// Tier FX migration of internal/linker/go_http_hosts.go's retired
// ResolveGoHTTPHosts (Tier J.2b) — replaced by
// patterns/go/go_http_hosts.yaml + rules/go/go_http_hosts.dl, driven by the
// "go_http_hosts" hub provider (internal/factpipe/hub_go_http_hosts.go).
// These port the retired Go test suite verbatim through pipeline.Run.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ghMaxHostHops mirrors the retired maxHostHops (internal/linker's
// go_http_hosts.go:566, now rules/go/go_http_hosts.dl's four unrolled hop
// layers) — the boundary the hop-limit test pins.
const ghMaxHostHops = 4

func ghWriteFixture(t *testing.T, files map[string]string) (string, []string) {
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

func ghRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("go_http_hosts")
	if fw == nil {
		t.Fatal("go_http_hosts framework not embedded")
	}
	res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// ghPatchFor returns the patch targeting id, or nil.
func ghPatchFor(res pipeline.Result, id string) *factpipe.NodePatch {
	for i := range res.Patches {
		if res.Patches[i].ID == id {
			return &res.Patches[i]
		}
	}
	return nil
}

func ghTwoHopFixture() map[string]string {
	return map[string]string{
		"config.go": `package main

import "os"

type Config struct {
	WillowAPIURL string
	WillowAPIKey string
}

func LoadConfig() *Config {
	return &Config{
		WillowAPIURL: os.Getenv("MYSYCAMORE_API_URL"),
		WillowAPIKey: os.Getenv("MYSYCAMORE_API_KEY"),
	}
}
`,
		"willow_client.go": `package main

import (
	"fmt"
	"net/http"
	"strings"
)

type WillowClient struct {
	baseURL string
	apiKey  string
}

func NewWillowClient(baseURL, apiKey string) *WillowClient {
	return &WillowClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
	}
}

func (c *WillowClient) LookupUserByEmail(email string) error {
	reqURL := fmt.Sprintf("%s/api/v1/users?email=%s", c.baseURL, email)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`,
		"main.go": `package main

func main() {
	config := LoadConfig()
	client := NewWillowClient(config.WillowAPIURL, config.WillowAPIKey)
	_ = client.LookupUserByEmail("a@b.c")
}
`,
	}
}

func TestGoHTTPHosts_TwoHopConstructor(t *testing.T) {
	dir, paths := ghWriteFixture(t, ghTwoHopFixture())
	nodes := []graph.Node{{
		ID: "svc:client:lookup", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "willow_client.go"), Line: 23,
		Meta: map[string]string{"method": "GET", "path": "*/api/v1/users", "synthesized": "sprintf_url"},
	}}

	res := ghRun(t, nodes, paths)

	p := ghPatchFor(res, "svc:client:lookup")
	if p == nil {
		t.Fatal("expected a patch")
	}
	if p.Meta["env_var"] != "MYSYCAMORE_API_URL" {
		t.Errorf("env_var = %q, want MYSYCAMORE_API_URL", p.Meta["env_var"])
	}
	if p.Meta["host_resolved_via"] != "go_env_field" {
		t.Errorf("host_resolved_via = %q, want go_env_field", p.Meta["host_resolved_via"])
	}
}

func TestGoHTTPHosts_KeyDynamicNode(t *testing.T) {
	dir, paths := ghWriteFixture(t, ghTwoHopFixture())
	nodes := []graph.Node{{
		ID: "svc:client:req", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "willow_client.go"), Line: 24,
		Meta: map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "reqURL"},
	}}

	res := ghRun(t, nodes, paths)
	p := ghPatchFor(res, "svc:client:req")
	if p == nil || p.Meta["env_var"] != "MYSYCAMORE_API_URL" {
		t.Fatalf("patch = %+v, want env_var MYSYCAMORE_API_URL", p)
	}
}

func TestGoHTTPHosts_AmbiguousBaseLeftUnstamped(t *testing.T) {
	files := ghTwoHopFixture()
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	primary := NewWillowClient(config.WillowAPIURL, config.WillowAPIKey)
	fallback := NewWillowClient(config.FallbackURL, config.WillowAPIKey)
	_ = primary
	_ = fallback
}
`
	files["config.go"] = `package main

import "os"

type Config struct {
	WillowAPIURL string
	FallbackURL      string
	WillowAPIKey string
}

func LoadConfig() *Config {
	return &Config{
		WillowAPIURL: os.Getenv("MYSYCAMORE_API_URL"),
		FallbackURL:      os.Getenv("MYSYCAMORE_FALLBACK_URL"),
		WillowAPIKey: os.Getenv("MYSYCAMORE_API_KEY"),
	}
}
`
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{{
		ID: "svc:client:lookup", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "willow_client.go"), Line: 23,
		Meta: map[string]string{"method": "GET", "path": "*/api/v1/users"},
	}}

	res := ghRun(t, nodes, paths)
	if p := ghPatchFor(res, "svc:client:lookup"); p != nil {
		t.Fatalf("expected no patch (ambiguous base must never be guessed), got %+v", p)
	}
}

func TestGoHTTPHosts_LiteralURLNotAttributed(t *testing.T) {
	dir, paths := ghWriteFixture(t, ghTwoHopFixture())
	nodes := []graph.Node{{
		ID: "svc:client:literal", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "willow_client.go"), Line: 23,
		Meta: map[string]string{"method": "GET", "url": "https://pinned.example.com/api/v1/users"},
	}}

	res := ghRun(t, nodes, paths)
	if p := ghPatchFor(res, "svc:client:literal"); p != nil {
		t.Fatalf("expected no patch, got %+v", p)
	}
}

func TestGoHTTPHosts_NoGoNodes_NoOp(t *testing.T) {
	nodes := []graph.Node{
		{ID: "r1", Service: "web", Type: graph.NodeTypeHTTPClient, Language: "ruby",
			File: "/x/a.rb", Meta: map[string]string{"key_dynamic": "true"}},
		{ID: "f1", Service: "web", Type: graph.NodeTypeFunction, Language: "go", File: "/x/a.go"},
	}
	res := ghRun(t, nodes, []string{"/x/a.go"})
	if len(res.Patches) != 0 {
		t.Fatalf("patches = %+v, want none", res.Patches)
	}
}

func TestGoHTTPHosts_TwoBasesInOneMethodUnstamped(t *testing.T) {
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
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{{
		ID: "svc:client:sync", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "client.go"), Line: 16,
		Meta: map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "c.aBase+path"},
	}}

	res := ghRun(t, nodes, paths)
	if p := ghPatchFor(res, "svc:client:sync"); p != nil {
		t.Fatalf("expected no patch (two env bases in one method is ambiguous), got %+v", p)
	}
}

// ── multi-hop forwarding ────────────────────────────────────────────────────

func ghThreeHopFixture() map[string]string {
	return map[string]string{
		"config.go": `package main

import "os"

type Config struct {
	MapleManagerAPIURL string
	MapleAPIKey        string
}

func LoadConfig() *Config {
	return &Config{
		MapleManagerAPIURL: os.Getenv("MAPLE_MANAGER_API_URL"),
		MapleAPIKey:        os.Getenv("MAPLE_API_KEY"),
	}
}
`,
		"tile_publisher.go": `package main

type TilePublisher struct {
	client *MapleManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return &TilePublisher{
		client: NewMapleManagerUserAppClient(apiURL, authToken),
	}
}
`,
		"maple_manager_user_app_client.go": `package main

import (
	"fmt"
	"net/http"
)

type MapleManagerUserAppClient struct {
	baseURL   string
	authToken string
}

func NewMapleManagerUserAppClient(baseURL, authToken string) *MapleManagerUserAppClient {
	return &MapleManagerUserAppClient{baseURL: baseURL, authToken: authToken}
}

func (c *MapleManagerUserAppClient) List() error {
	reqURL := fmt.Sprintf("%s/user-apps", c.baseURL)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`,
		"main.go": `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.MapleManagerAPIURL, config.MapleAPIKey)
}
`,
	}
}

func ghUserAppNode(dir string) graph.Node {
	return graph.Node{
		ID: "svc:client:list", Service: "svc", Type: graph.NodeTypeHTTPClient, Language: "go",
		File: filepath.Join(dir, "maple_manager_user_app_client.go"), Line: 19,
		Meta: map[string]string{"method": "GET", "path": "*/user-apps", "synthesized": "sprintf_url"},
	}
}

func TestGoHTTPHosts_ThreeHopConstructor(t *testing.T) {
	dir, paths := ghWriteFixture(t, ghThreeHopFixture())
	nodes := []graph.Node{ghUserAppNode(dir)}

	res := ghRun(t, nodes, paths)
	p := ghPatchFor(res, "svc:client:list")
	if p == nil || p.Meta["env_var"] != "MAPLE_MANAGER_API_URL" {
		t.Fatalf("patch = %+v, want env_var MAPLE_MANAGER_API_URL", p)
	}
}

func TestGoHTTPHosts_FourHopConstructor(t *testing.T) {
	files := ghThreeHopFixture()
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *MapleManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return newTilePublisherInner(apiURL, authToken)
}

func newTilePublisherInner(baseURL, authToken string) *TilePublisher {
	return &TilePublisher{
		client: NewMapleManagerUserAppClient(baseURL, authToken),
	}
}
`
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{ghUserAppNode(dir)}

	res := ghRun(t, nodes, paths)
	p := ghPatchFor(res, "svc:client:list")
	if p == nil || p.Meta["env_var"] != "MAPLE_MANAGER_API_URL" {
		t.Fatalf("patch = %+v, want env_var MAPLE_MANAGER_API_URL", p)
	}
}

func ghForwarderChain(n int) string {
	var b strings.Builder
	b.WriteString("package main\n\ntype TilePublisher struct {\n\tclient *MapleManagerUserAppClient\n}\n\n")
	b.WriteString("func NewTilePublisher(apiURL, authToken string) *TilePublisher {\n\treturn forward0(apiURL, authToken)\n}\n\n")
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("forward%d(baseURL, authToken)", i+1)
		if i == n-1 {
			next = "&TilePublisher{client: NewMapleManagerUserAppClient(baseURL, authToken)}"
		}
		b.WriteString(fmt.Sprintf(
			"func forward%d(baseURL, authToken string) *TilePublisher {\n\treturn %s\n}\n\n", i, next))
	}
	return b.String()
}

func TestGoHTTPHosts_HopLimitBoundary(t *testing.T) {
	for n := 1; n <= ghMaxHostHops+2; n++ {
		wantResolved := n+1 <= ghMaxHostHops
		t.Run(fmt.Sprintf("%d_frames", n), func(t *testing.T) {
			files := ghThreeHopFixture()
			files["tile_publisher.go"] = ghForwarderChain(n)
			dir, paths := ghWriteFixture(t, files)
			nodes := []graph.Node{ghUserAppNode(dir)}

			res := ghRun(t, nodes, paths)
			p := ghPatchFor(res, "svc:client:list")
			if wantResolved && (p == nil || p.Meta["env_var"] != "MAPLE_MANAGER_API_URL") {
				t.Errorf("patch = %+v, want env_var MAPLE_MANAGER_API_URL within the hop limit", p)
			}
			if !wantResolved && p != nil {
				t.Errorf("patch = %+v, want none past the hop limit (never guess)", p)
			}
		})
	}
}

func TestGoHTTPHosts_NonHostParamDoesNotForward(t *testing.T) {
	files := ghThreeHopFixture()
	files["config.go"] = `package main

import "os"

type Config struct {
	Token string
}

func LoadConfig() *Config {
	return &Config{Token: os.Getenv("MAPLE_API_TOKEN")}
}
`
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *MapleManagerUserAppClient
}

func NewTilePublisher(token string) *TilePublisher {
	return &TilePublisher{client: NewMapleManagerUserAppClient(token, "")}
}
`
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.Token)
}
`
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{ghUserAppNode(dir)}

	res := ghRun(t, nodes, paths)
	if p := ghPatchFor(res, "svc:client:list"); p != nil {
		t.Fatalf("expected no patch (a token is not a host), got %+v", p)
	}
}

func TestGoHTTPHosts_ForwardedConflictUnstamped(t *testing.T) {
	files := ghThreeHopFixture()
	files["config.go"] = `package main

import "os"

type Config struct {
	MapleManagerAPIURL string
	FallbackAPIURL   string
	MapleAPIKey        string
}

func LoadConfig() *Config {
	return &Config{
		MapleManagerAPIURL: os.Getenv("MAPLE_MANAGER_API_URL"),
		FallbackAPIURL:   os.Getenv("MAPLE_FALLBACK_API_URL"),
		MapleAPIKey:        os.Getenv("MAPLE_API_KEY"),
	}
}
`
	files["main.go"] = `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.MapleManagerAPIURL, config.MapleAPIKey)
	_ = NewTilePublisher(config.FallbackAPIURL, config.MapleAPIKey)
}
`
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{ghUserAppNode(dir)}

	res := ghRun(t, nodes, paths)
	if p := ghPatchFor(res, "svc:client:list"); p != nil {
		t.Fatalf("expected no patch (forwarded ambiguity must not be guessed), got %+v", p)
	}
}

func TestGoHTTPHosts_RecursiveForwarderTerminates(t *testing.T) {
	files := ghThreeHopFixture()
	files["tile_publisher.go"] = `package main

type TilePublisher struct {
	client *MapleManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return pingBaseURL(apiURL, authToken)
}

func pingBaseURL(baseURL, authToken string) *TilePublisher {
	if baseURL == "" {
		return pongBaseURL(baseURL, authToken)
	}
	return &TilePublisher{client: NewMapleManagerUserAppClient(baseURL, authToken)}
}

func pongBaseURL(baseURL, authToken string) *TilePublisher {
	return pingBaseURL(baseURL, authToken)
}
`
	dir, paths := ghWriteFixture(t, files)
	nodes := []graph.Node{ghUserAppNode(dir)}

	res := ghRun(t, nodes, paths)
	p := ghPatchFor(res, "svc:client:list")
	if p == nil || p.Meta["env_var"] != "MAPLE_MANAGER_API_URL" {
		t.Fatalf("patch = %+v, want env_var MAPLE_MANAGER_API_URL", p)
	}
}

func TestGoHTTPHosts_ForwardingDeterministic(t *testing.T) {
	dir, paths := ghWriteFixture(t, ghThreeHopFixture())
	for i := 0; i < 10; i++ {
		nodes := []graph.Node{ghUserAppNode(dir)}
		res := ghRun(t, nodes, paths)
		p := ghPatchFor(res, "svc:client:list")
		if p == nil || p.Meta["env_var"] != "MAPLE_MANAGER_API_URL" {
			t.Fatalf("run %d: patch = %+v, want env_var MAPLE_MANAGER_API_URL", i, p)
		}
	}
}
