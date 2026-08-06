package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// sprintfModule lays out an API client whose request URL is built via
// fmt.Sprintf in the same function — the exact vega_api_client.go /
// willow_client.go shape Tier X.11 targets (docs/sprintf-url-resolution-plan.md).
func sprintfModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/sprintftest\n\ngo 1.25.0\n",
		"client.go": `package client

import (
	"fmt"
	"net/http"
)

type Client struct{ baseURL string }

func (c *Client) GetFolderByPath(folderPath string) error {
	reqURL := fmt.Sprintf("%s/client_api/v1/folders/details_by_path?path=%s", c.baseURL, folderPath)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (c *Client) LookupUserByEmail(email string) error {
	reqURL := fmt.Sprintf("%s/api/v1/users?email=%s", c.baseURL, email)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (c *Client) HealthCheck() error {
	reqURL := fmt.Sprintf("%s/health", c.baseURL)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (c *Client) FetchResourceDetail(resourceType string, id int) error {
	// Dynamic segment inside the path itself (not just at the end) — out of
	// scope per the plan's non-goals; must stay on the dynamic ledger.
	reqURL := fmt.Sprintf("%s/v1/%s/%d/detail", c.baseURL, resourceType, id)
	req, err := http.NewRequest("GET", reqURL, nil)
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

// TestSprintfURL_ResolvesLiteralPathPrefix: the three literal-shaped callers each
// yield a resolved http_client producer; the caller whose path has a dynamic
// middle segment yields none (dynamic ledger, not a guess).
func TestSprintfURL_ResolvesLiteralPathPrefix(t *testing.T) {
	dir := sprintfModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:client.go:method:GetFolderByPath:10":     true,
		"svc:client.go:method:LookupUserByEmail:20":   true,
		"svc:client.go:method:HealthCheck:30":         true,
		"svc:client.go:method:FetchResourceDetail:40": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	synth := map[string]graph.Node{} // path → node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["synthesized"] == "sprintf_url" {
			synth[n.Meta["path"]] = n
		}
	}

	want := map[string]string{
		"*/client_api/v1/folders/details_by_path": "GetFolderByPath",
		"*/api/v1/users":                          "LookupUserByEmail",
		"*/health":                                "HealthCheck",
	}
	if len(synth) != len(want) {
		t.Fatalf("expected %d synth producers, got %d: %+v", len(want), len(synth), synth)
	}
	for path, viaFn := range want {
		n, ok := synth[path]
		if !ok {
			t.Fatalf("missing synth producer %q; got %+v", path, synth)
		}
		if n.Meta["via_wrapper"] != viaFn {
			t.Errorf("%q: via_wrapper=%q, want %q", path, n.Meta["via_wrapper"], viaFn)
		}
		if n.Meta["method"] != "GET" {
			t.Errorf("%q: method=%q, want GET", path, n.Meta["method"])
		}
		if n.Meta["url_confidence"] != graph.ConfidenceInferred {
			t.Errorf("%q: url_confidence=%q, want %q", path, n.Meta["url_confidence"], graph.ConfidenceInferred)
		}
	}

	// The dynamic-middle-segment caller must NOT produce a synth node.
	for path, n := range synth {
		if n.Meta["via_wrapper"] == "FetchResourceDetail" {
			t.Errorf("dynamic-middle-segment caller wrongly resolved: %q -> %+v", path, n)
		}
	}

	// Each synth node must be attributed to its caller via a calls edge tagged sprintf_url.
	var callsEdges int
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "sprintf_url" {
			callsEdges++
			if !known[e.From] {
				t.Errorf("sprintf_url calls edge from unknown node %q", e.From)
			}
		}
	}
	if callsEdges != 3 {
		t.Errorf("expected 3 sprintf_url calls edges, got %d", callsEdges)
	}
}

// TestSprintfURL_SkipsTestFileCallSites: X.9 parity — a Sprintf-composed request
// built inside a _test.go file must not mint a synthesized producer.
func TestSprintfURL_SkipsTestFileCallSites(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/sprintftesttf\n\ngo 1.25.0\n",
		"client_test.go": `package client

import (
	"fmt"
	"net/http"
)

type Client struct{ baseURL string }

func (c *Client) fetchInTest() error {
	reqURL := fmt.Sprintf("%s/only-in-test", c.baseURL)
	req, err := http.NewRequest("GET", reqURL, nil)
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
		"svc:client_test.go:method:fetchInTest:10": true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	for _, n := range res.Nodes {
		if n.Meta["synthesized"] == "sprintf_url" {
			t.Errorf("synth producer minted at a test-file call site: %s (%s)", n.ID, n.Meta["path"])
		}
	}
}

// TestSprintfURL_Determinism: two runs produce byte-identical node/edge ID sets.
func TestSprintfURL_Determinism(t *testing.T) {
	dir := sprintfModule(t)
	t.Chdir(dir)
	known := map[string]bool{
		"svc:client.go:method:GetFolderByPath:10":     true,
		"svc:client.go:method:LookupUserByEmail:20":   true,
		"svc:client.go:method:HealthCheck:30":         true,
		"svc:client.go:method:FetchResourceDetail:40": true,
	}
	ids := func() []string {
		a := &GoSemanticAnalyzer{}
		res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
		var out []string
		for _, n := range res.Nodes {
			if n.Meta["synthesized"] == "sprintf_url" {
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

// TestExtractSprintfPathPrefix covers the boundary heuristic directly.
func TestExtractSprintfPathPrefix(t *testing.T) {
	cases := []struct {
		name   string
		format string
		want   string
		wantOK bool
	}{
		{"path+query", "%s/client_api/v1/folders/details_by_path?path=%s", "/client_api/v1/folders/details_by_path", true},
		{"path+query simple", "%s/api/v1/users?email=%s", "/api/v1/users", true},
		{"path only, no query", "%s/health", "/health", true},
		{"dynamic mid-path segment", "%s/v1/%s/detail", "", false},
		{"dynamic mid-path segment with query", "%s/v1/%s/detail?x=%s", "", false},
		{"no leading verb", "/static/path", "", false},
		{"unsupported verb", "%x/path", "", false},
		{"nothing after verb", "%s", "", false},
		{"empty after verb before query", "%s?x=%s", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractSprintfPathPrefix(tc.format)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("extractSprintfPathPrefix(%q) = (%q, %v), want (%q, %v)", tc.format, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
