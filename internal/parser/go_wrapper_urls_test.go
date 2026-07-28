package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// wrapperModule lays out an API-client whose request URL is a wrapper parameter,
// the exact svc-c-mgr ClientB shape X.7 targets.
func wrapperModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/wraptest\n\ngo 1.25.0\n",
		"client.go": `package client

import "net/http"

type Client struct{ baseURL string }

func (c *Client) RegisterApp(app any) error {
	return c.doWithRetry(http.MethodPost, "/api/v1/service/apps/register", app)
}

func (c *Client) ProvisionUser() error {
	return c.doWithRetry(http.MethodPut, "/api/v1/users/provision", nil)
}

func (c *Client) FetchDynamic(p string) error {
	return c.doWithRetry(http.MethodGet, p, nil)
}

func (c *Client) doWithRetry(method, path string, body any) error {
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestWrapperURL_FanOutAndDynamicLedger: the two literal-path callers each yield
// a resolved http_client producer with the composed URL + method (bug-class #1
// fan-out); the caller passing a variable path yields no synth node — its call
// stays represented by the wrapper's own param-dynamic node.
func TestWrapperURL_FanOutAndDynamicLedger(t *testing.T) {
	dir := wrapperModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:client.go:method:RegisterApp:7":   true,
		"svc:client.go:method:ProvisionUser:11": true,
		"svc:client.go:method:FetchDynamic:15":  true,
		"svc:client.go:method:doWithRetry:19":   true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	synth := map[string]graph.Node{} // method+path → node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["synthesized"] == "wrapper_url" {
			synth[n.Meta["method"]+" "+n.Meta["path"]] = n
		}
	}

	want := map[string]string{
		"POST */api/v1/service/apps/register": "RegisterApp",
		"PUT */api/v1/users/provision":        "ProvisionUser",
	}
	if len(synth) != len(want) {
		t.Fatalf("expected %d synth producers, got %d: %+v", len(want), len(synth), synth)
	}
	for key, viaCaller := range want {
		n, ok := synth[key]
		if !ok {
			t.Fatalf("missing synth producer %q; got %+v", key, synth)
		}
		if n.Meta["via_wrapper"] != "doWithRetry" {
			t.Errorf("%q: via_wrapper=%q, want doWithRetry", key, n.Meta["via_wrapper"])
		}
		_ = viaCaller
	}

	// The dynamic caller must NOT produce a synth node.
	for k := range synth {
		if k == "GET */api/v1/users/provision" {
			t.Errorf("dynamic-path caller wrongly resolved: %q", k)
		}
	}

	// Each synth node must be attributed to its caller via a calls edge.
	var callsEdges int
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "wrapper_url" {
			callsEdges++
			if !known[e.From] {
				t.Errorf("wrapper_url calls edge from unknown node %q", e.From)
			}
		}
	}
	if callsEdges != 2 {
		t.Errorf("expected 2 wrapper_url calls edges, got %d", callsEdges)
	}
}

// TestWrapperURL_TwoHopChain locks in the real svc-c-mgr shape: the literal
// path is two call boundaries away from http.NewRequest
// (RegisterApp → doWithRetry(method, path) → doRequest(method, path) →
// http.NewRequest(method, base+path)). The transitive closure must still resolve
// it to a single producer with the composed URL and propagated method.
func TestWrapperURL_TwoHopChain(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/twohop\n\ngo 1.25.0\n",
		"client.go": `package client

import "net/http"

type Client struct{ baseURL string }

func (c *Client) RegisterApp(app any) error {
	return c.doWithRetry(http.MethodPost, "/api/v1/service/apps/register", app)
}

func (c *Client) doWithRetry(method, path string, body any) error {
	return c.doRequest(method, path, body)
}

func (c *Client) doRequest(method, path string, body any) error {
	fullURL := c.baseURL + path
	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:client.go:method:RegisterApp:7":  true,
		"svc:client.go:method:doWithRetry:11": true,
		"svc:client.go:method:doRequest:15":   true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var got []graph.Node
	for _, n := range res.Nodes {
		if n.Meta["synthesized"] == "wrapper_url" {
			got = append(got, n)
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 synth producer across the two-hop chain, got %d: %+v", len(got), got)
	}
	n := got[0]
	if n.Meta["method"] != "POST" || n.Meta["path"] != "*/api/v1/service/apps/register" {
		t.Fatalf("wrong composed producer: method=%q path=%q", n.Meta["method"], n.Meta["path"])
	}
	// Attributed to the outermost literal caller, not an intermediate wrapper.
	if n.ID != "svc:client.go:http_client:doWithRetry:8" {
		t.Errorf("synth node should sit at the RegisterApp call site (line 8): %q", n.ID)
	}
}

// TestWrapperURL_SkipsTestFileCallSites: a wrapper called from a _test.go file
// (httptest fixtures) must not mint a synthesized producer — that call site is
// test scaffolding, not a service endpoint, and would otherwise re-enter the
// cross-service denominator (fix #1 parity for the SSA synthesis path).
func TestWrapperURL_SkipsTestFileCallSites(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/wraptesttf\n\ngo 1.25.0\n",
		"client.go": `package client

import "net/http"

type Client struct{ baseURL string }

func (c *Client) RegisterApp(app any) error {
	return c.doWithRetry(http.MethodPost, "/api/v1/service/apps/register", app)
}

func (c *Client) doWithRetry(method, path string, body any) error {
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}
`,
		"client_test.go": `package client

func exercise() error {
	c := &Client{}
	return c.doWithRetry("PUT", "/dsw/only-in-test", nil)
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:client.go:method:RegisterApp:7":  true,
		"svc:client.go:method:doWithRetry:11": true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	for _, n := range res.Nodes {
		if n.Meta["synthesized"] != "wrapper_url" {
			continue
		}
		if graph.IsTestFilePath(n.File) {
			t.Errorf("synth producer minted at a test-file call site: %s (%s)", n.ID, n.Meta["path"])
		}
		if n.Meta["path"] == "/dsw/only-in-test" {
			t.Errorf("the test-only wrapper call must not produce a synth node")
		}
	}
}

// TestWrapperURL_Determinism: two runs produce byte-identical node/edge ID sets.
func TestWrapperURL_Determinism(t *testing.T) {
	dir := wrapperModule(t)
	t.Chdir(dir)
	known := map[string]bool{
		"svc:client.go:method:RegisterApp:7":    true,
		"svc:client.go:method:ProvisionUser:11": true,
		"svc:client.go:method:FetchDynamic:15":  true,
		"svc:client.go:method:doWithRetry:19":   true,
	}
	ids := func() []string {
		a := &GoSemanticAnalyzer{}
		res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
		var out []string
		for _, n := range res.Nodes {
			if n.Meta["synthesized"] == "wrapper_url" {
				out = append(out, n.ID)
			}
		}
		return out
	}
	first, second := ids(), ids()
	if len(first) != len(second) {
		t.Fatalf("run count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("nondeterministic synth IDs at %d: %q vs %q", i, first[i], second[i])
		}
	}
}
