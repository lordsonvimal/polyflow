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
	// One generic segment behind an opaque host: every service in a fleet
	// exposes /health, so this cannot identify a callee (K.1 evidence gate).
	reqURL := fmt.Sprintf("%s/health", c.baseURL)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (c *Client) FetchResourceDetail(resourceType string, id int) error {
	// Dynamic segments inside the path itself. Tier K.1 resolves these to
	// wildcards rather than dropping the call: the literal /v1/ and /detail
	// anchors are real evidence, and param_wildcard reduces the handler's
	// ":id" the same way.
	reqURL := fmt.Sprintf("%s/v1/%s/%d/detail", c.baseURL, resourceType, id)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (c *Client) Opaque(seg string) error {
	// No literal segment survives: zero evidence, so no producer (#12).
	reqURL := fmt.Sprintf("%s/%s", c.baseURL, seg)
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

// TestSprintfURL_ResolvesLiteralPathPrefix: every caller whose format string pins
// at least one literal path segment yields a resolved http_client producer, with
// dynamic segments rendered as wildcards; the caller whose path is entirely
// dynamic yields none (dynamic ledger, not a guess). A single literal segment
// behind an opaque host is emitted but stamped weak + capped at `partial`.
func TestSprintfURL_ResolvesLiteralPathPrefix(t *testing.T) {
	dir := sprintfModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:client.go:method:GetFolderByPath:10":     true,
		"svc:client.go:method:LookupUserByEmail:20":   true,
		"svc:client.go:method:HealthCheck:30":         true,
		"svc:client.go:method:FetchResourceDetail:40": true,
		"svc:client.go:method:Opaque:50":              true,
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
		"*/v1/*/*/detail":                         "FetchResourceDetail",
		// One literal segment behind an opaque host is emitted too, marked
		// weak: whether `*/health` names a route or a convention depends on how
		// many services answer to it, which only the contract engine can see.
		"*/health": "HealthCheck",
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

	// The all-wildcard caller has no evidence at all and must NOT produce a
	// synth node.
	for path, n := range synth {
		if n.Meta["via_wrapper"] == "Opaque" {
			t.Errorf("evidence-free caller wrongly resolved: %q -> %+v", path, n)
		}
	}

	// The single-segment caller is emitted, but must carry both markers: the
	// engine needs `path_evidence` to suppress it when the path spans services,
	// and the ceiling keeps a surviving edge from being promoted to `verified`
	// on spec evidence alone.
	if got := synth["*/health"].Meta["path_evidence"]; got != "weak" {
		t.Errorf("*/health: path_evidence=%q, want weak", got)
	}
	if got := synth["*/health"].Meta["confidence_ceiling"]; got != graph.ConfidencePartial {
		t.Errorf("*/health: confidence_ceiling=%q, want %q", got, graph.ConfidencePartial)
	}
	// `api` and `v1` are generic REST-namespace segments, so `*/api/v1/users`
	// pins a service no better than `*/users` — one real literal behind an
	// opaque host. It carries both markers and the engine suppresses it when
	// the path spans services (willow and orion-atlas both serve it).
	if got := synth["*/api/v1/users"].Meta["path_evidence"]; got != "weak" {
		t.Errorf("*/api/v1/users: path_evidence=%q, want weak", got)
	}
	if got := synth["*/api/v1/users"].Meta["confidence_ceiling"]; got != graph.ConfidencePartial {
		t.Errorf("*/api/v1/users: confidence_ceiling=%q, want %q", got, graph.ConfidencePartial)
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
	if callsEdges != len(want) {
		t.Errorf("expected %d sprintf_url calls edges, got %d", len(want), callsEdges)
	}
}

// TestComposedURL_ResolvesConcatenatedHostField is the Tier K.1 regression guard
// for the other half of the measured Maple→orion client surface: the URL is not a
// Sprintf at all but a concatenation onto a struct field that a constructor
// parameter filled in, so the host is unknowable statically while the path beside
// it is a plain literal.
//
// Modelled on maple-manager/internal/clients/vega_user_sync.go, which produced zero
// resolved edges before K.1 despite three fully-literal /client_api/v1/ paths.
func TestComposedURL_ResolvesConcatenatedHostField(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/concattest\n\ngo 1.25.0\n",
		"client.go": `package client

import (
	"context"
	"fmt"
	"net/http"
)

type Adapter struct{ baseURL string }

func NewAdapter(baseURL string) *Adapter { return &Adapter{baseURL: baseURL} }

func (a *Adapter) CreateUser(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/client_api/v1/users/", nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (a *Adapter) ListUsers(ctx context.Context, page int) error {
	path := fmt.Sprintf("/client_api/v1/users?page=%d&page_size=25", page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		return err
	}
	_ = req
	return nil
}

func (a *Adapter) Ping(ctx context.Context, host string) error {
	// Host and path both opaque: no evidence, no producer.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+a.baseURL, nil)
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
		"svc:client.go:method:CreateUser:10": true,
		"svc:client.go:method:ListUsers:20":  true,
		"svc:client.go:method:Ping:30":       true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	got := map[string]graph.Node{} // path → node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["synthesized"] == "concat_url" {
			got[n.Meta["path"]] = n
		}
	}

	want := map[string]string{
		"*/client_api/v1/users/": "POST",
		"*/client_api/v1/users":  "GET",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d concat_url producers, got %d: %+v", len(want), len(got), got)
	}
	for path, method := range want {
		n, ok := got[path]
		if !ok {
			t.Fatalf("missing concat_url producer %q; got %+v", path, got)
		}
		if n.Meta["method"] != method {
			t.Errorf("%q: method=%q, want %q", path, n.Meta["method"], method)
		}
	}
	for path, n := range got {
		if n.Meta["via_wrapper"] == "Ping" {
			t.Errorf("evidence-free concatenation wrongly resolved: %q -> %+v", path, n)
		}
	}
}

// TestComposedURL_SkipsBareLiteralURL: a literal URL argument already gets a fully
// resolved node from the tree-sitter HTTP matcher, so this pass must not mint a
// second producer for the same call site.
func TestComposedURL_SkipsBareLiteralURL(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/literaltest\n\ngo 1.25.0\n",
		"client.go": `package client

import "net/http"

func Fetch() error {
	req, err := http.NewRequest("GET", "https://api.example.com/v1/things", nil)
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

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), map[string]bool{
		"svc:client.go:function:Fetch:5": true,
	})
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	for _, n := range res.Nodes {
		switch n.Meta["synthesized"] {
		case "concat_url", "sprintf_url":
			t.Errorf("bare literal URL wrongly synthesized: %s (%s)", n.ID, n.Meta["path"])
		}
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

// TestExpandFormatVerbs covers the format-string rewrite directly, including the
// verb forms that carry flags or width and the `%%` literal-percent escape.
func TestExpandFormatVerbs(t *testing.T) {
	cases := []struct {
		name   string
		format string
		want   string
	}{
		{"host + path + query", "%s/client_api/v1/folders/details_by_path?path=%s", "*/client_api/v1/folders/details_by_path?path=*"},
		{"path only", "%s/health", "*/health"},
		{"numeric mid-path segment", "%s/client_api/v1/folders/%d/files", "*/client_api/v1/folders/*/files"},
		{"width-qualified verb", "%s/reports/%02d", "*/reports/*"},
		{"flagged verb", "%s/x/%-10s/y", "*/x/*/y"},
		{"literal percent", "%s/discount/100%%", "*/discount/100%"},
		{"no verbs", "/static/path", "/static/path"},
		{"trailing bare percent", "/a/%", "/a/%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandFormatVerbs(tc.format); got != tc.want {
				t.Errorf("expandFormatVerbs(%q) = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
}

// TestPathEvidence pins the evidence grading. With an opaque host, two literal
// segments name an API surface and are strong evidence; a single literal segment
// is *weak* — it may name a real endpoint (`*/user-apps`) or a convention every
// service implements (`*/health`), and which one it is depends on how many
// services answer to it, not on the pattern. The contract engine settles that;
// this function must not pre-judge it (bug-class #1, #12).
func TestPathEvidence(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    string
	}{
		{"named API surface", "*/client_api/v1/users", pathEvidenceStrong},
		{"two real literals past the namespace", "*/api/v1/service/apps", pathEvidenceStrong},
		{"fully literal single segment", "/health", pathEvidenceStrong},
		// `api`/`v1`/`v2` are generic REST-namespace tokens and do not count as
		// discriminating literals: `*/api/v1/users` pins a service no better
		// than `*/users`, and `*/v1/*/*/detail` / `http://*/api/things` each
		// leave one real literal behind an opaque host.
		{"namespace + one literal", "*/api/v1/users", pathEvidenceWeak},
		{"version segment then one literal", "*/v1/*/*/detail", pathEvidenceWeak},
		{"namespace + one literal, opaque host", "http://*/api/things", pathEvidenceWeak},
		// Weak, not rejected: the engine suppresses these only when the path
		// resolves in more than one service. On the juniper fleet that
		// suppresses `*/health` (3 services) and keeps `*/user-apps` (1).
		{"generic single segment, opaque host", "*/health", pathEvidenceWeak},
		{"single segment, opaque host", "*/payment_links", pathEvidenceWeak},
		{"real endpoint, single segment", "*/user-apps", pathEvidenceWeak},
		{"single literal after wildcards", "*/*/*/messages", pathEvidenceWeak},
		{"bare wildcard", "*", pathEvidenceNone},
		{"no literals", "*/*", pathEvidenceNone},
		{"empty", "", pathEvidenceNone},
		{"root", "/", pathEvidenceNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathEvidence(tc.pattern); got != tc.want {
				t.Errorf("pathEvidence(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestHasLiteralAuthority: a pattern naming a concrete third-party host belongs to
// the tree-sitter matcher, not to this pass. Stripping such a host at match time
// (`https://api.github.com/users/*` → `/users/*`) would otherwise let a call that
// never leaves for a workspace service match a workspace route.
func TestHasLiteralAuthority(t *testing.T) {
	cases := map[string]bool{
		"https://api.github.com/users/*": true,
		"http://localhost:5200/app/x":    true,
		"http://*/api/things":            false,
		"*/client_api/v1/users":          false,
		"/relative/path":                 false,
		"https://*/v1":                   false,
	}
	for pattern, want := range cases {
		t.Run(pattern, func(t *testing.T) {
			if got := hasLiteralAuthority(pattern); got != want {
				t.Errorf("hasLiteralAuthority(%q) = %v, want %v", pattern, got, want)
			}
		})
	}
}

// TestCollapseWildcards: adjacent opaque parts must fold into a single segment
// wildcard so patterns stay comparable segment by segment.
func TestCollapseWildcards(t *testing.T) {
	cases := map[string]string{
		"*/api/*":   "*/api/*",
		"**/api":    "*/api",
		"*/a/***/b": "*/a/*/b",
		"/plain":    "/plain",
	}
	for in, want := range cases {
		if got := collapseWildcards(in); got != want {
			t.Errorf("collapseWildcards(%q) = %q, want %q", in, got, want)
		}
	}
}
