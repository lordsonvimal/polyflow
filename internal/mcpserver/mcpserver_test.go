package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// fakeStore backs the tool handlers with an in-memory node list: search is a
// case-insensitive label substring match, mirroring FTS closely enough for
// resolution tests.
type fakeStore struct {
	nodes      []*graph.Node
	unresolved []graph.UnresolvedRef
	meta       map[string]string
}

func (f *fakeStore) SearchNodes(_ context.Context, query string, limit int) ([]*graph.Node, error) {
	var out []*graph.Node
	for _, n := range f.nodes {
		if strings.Contains(strings.ToLower(n.Label), strings.ToLower(query)) {
			out = append(out, n)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) ListUnresolvedRefs(_ context.Context) ([]graph.UnresolvedRef, error) {
	return f.unresolved, nil
}

func (f *fakeStore) GetMeta(_ context.Context, key string) (string, error) {
	if v, ok := f.meta[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("meta key not found: %s", key)
}

// fixture: frontend:fetchUser --http_call--> backend:getUser --calls--> backend:queryDB
func fixture() (*fakeStore, *graph.AdjacencyIndex) {
	nodes := []*graph.Node{
		{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10, Language: "javascript"},
		{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "getUser", Service: "backend", File: "handler.go", Line: 20, Language: "go"},
		{ID: "be:queryDB", Type: graph.NodeTypeFunction, Label: "queryDB", Service: "backend", File: "db.go", Line: 40, Language: "go"},
	}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	idx.AddEdge(&graph.Edge{ID: "e1", From: "fe:fetchUser", To: "be:getUser", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "be:getUser", To: "be:queryDB", Type: graph.EdgeTypeCalls})

	store := &fakeStore{
		nodes: nodes,
		unresolved: []graph.UnresolvedRef{
			{Service: "backend", File: "db.go", Line: 41, Name: "dynDispatch", Kind: "call_ref"},
			{Service: "backend", File: "unrelated.go", Line: 5, Name: "other", Kind: "call_ref"},
		},
	}
	return store, idx
}

// connect wires the server to an in-memory client session.
func connect(t *testing.T, store Store, idx *graph.AdjacencyIndex) *mcp.ClientSession {
	t.Helper()
	srv, _ := New(store, idx, "test", 0, true)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callJSON invokes a tool and decodes its JSON text content into out.
func callJSON(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool %s returned error: %v", tool, res.Content)
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	require.NoError(t, json.Unmarshal([]byte(text.Text), out))
}

func TestToolDiscovery(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	assert.ElementsMatch(t, []string{"investigate", "search", "context", "impact", "trace", "flows", "entrypoints", "resolve", "read", "hierarchy"}, names)
}

func TestToolDiscovery_Disabled(t *testing.T) {
	store, idx := fixture()

	srv, _ := New(store, idx, "test", 0, false)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	// Disabled: only the probe tool, no code-graph tools (clean A/B control).
	assert.ElementsMatch(t, []string{"status"}, names)

	var out map[string]string
	callJSON(t, cs, "status", nil, &out)
	assert.Equal(t, "disabled", out["status"])
}

// TestReadTool covers span-exact reads: a known end_line returns exactly
// start..end (span_known=true), a node without end_line falls back to a bounded
// window (span_known=false), and an unknown target surfaces candidates.
func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\nfunc Foo() int {\n\treturn 1\n}\n\nvar loose = 2\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.go"), []byte(src), 0o644))
	t.Chdir(dir) // read resolves n.File against root "."

	nodes := []*graph.Node{
		{ID: "p:foo.go:function:Foo:3", Type: graph.NodeTypeFunction, Label: "Foo",
			Service: "p", File: "foo.go", Line: 3, Language: "go",
			Meta: map[string]string{"end_line": "5"}},
		{ID: "p:foo.go:variable:loose:7", Type: graph.NodeTypeVariable, Label: "loose",
			Service: "p", File: "foo.go", Line: 7, Language: "go"}, // no end_line
	}
	store := &fakeStore{nodes: nodes}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	cs := connect(t, store, idx)

	// Known span → exact lines 3..5, span_known.
	var out readOutput
	callJSON(t, cs, "read", map[string]any{"target": "p:foo.go:function:Foo:3"}, &out)
	assert.True(t, out.SpanKnown)
	assert.Equal(t, 3, out.StartLine)
	assert.Equal(t, 5, out.EndLine)
	assert.Equal(t, "func Foo() int {\n\treturn 1\n}", out.Source)
	assert.False(t, out.Truncated)

	// No end_line → bounded window, span_known=false.
	var win readOutput
	callJSON(t, cs, "read", map[string]any{"target": "p:foo.go:variable:loose:7", "max_lines": 1}, &win)
	assert.False(t, win.SpanKnown)
	assert.Equal(t, "var loose = 2", win.Source)

	// Unknown target with no match → tool error (consistent with context/impact).
	res, err := cs.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "read", Arguments: map[string]any{"target": "does-not-exist"}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "unknown target should return a tool error")
}

// hierFixture: two services with nested dirs, so hierarchy has a real tree to
// budget. svcA: internal/parser/{parse.go,js.go} + main.go; svcB: app/handler.go.
func hierFixture() (*fakeStore, *graph.AdjacencyIndex) {
	nodes := []*graph.Node{
		{ID: "svcA:internal/parser/parse.go:function:Parse:10", Type: graph.NodeTypeFunction, Label: "Parse", Service: "svcA", File: "internal/parser/parse.go", Line: 10},
		{ID: "svcA:internal/parser/parse.go:function:helper:30", Type: graph.NodeTypeFunction, Label: "helper", Service: "svcA", File: "internal/parser/parse.go", Line: 30},
		{ID: "svcA:internal/parser/js.go:function:ParseJS:5", Type: graph.NodeTypeFunction, Label: "ParseJS", Service: "svcA", File: "internal/parser/js.go", Line: 5},
		{ID: "svcA:main.go:function:main:3", Type: graph.NodeTypeFunction, Label: "main", Service: "svcA", File: "main.go", Line: 3},
		// A non-symbol node (variable) must not appear at depth 3.
		{ID: "svcA:main.go:variable:cfg:1", Type: graph.NodeTypeVariable, Label: "cfg", Service: "svcA", File: "main.go", Line: 1},
		{ID: "svcB:app/handler.go:http_handler:Serve:8", Type: graph.NodeTypeHTTPHandler, Label: "Serve", Service: "svcB", File: "app/handler.go", Line: 8},
	}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	return &fakeStore{nodes: nodes}, idx
}

func TestHierarchyTool(t *testing.T) {
	store, idx := hierFixture()
	cs := connect(t, store, idx)

	// depth 1 → service roots only, collapsed to dir counts, no children.
	var d1 hierarchyOutput
	callJSON(t, cs, "hierarchy", map[string]any{"depth": 1}, &d1)
	require.Len(t, d1.Roots, 2)
	assert.Equal(t, "svcA", d1.Roots[0].Name)
	assert.Equal(t, "service", d1.Roots[0].Kind)
	assert.Nil(t, d1.Roots[0].Children)
	assert.Positive(t, d1.Roots[0].Count)

	// depth 2 → dirs + files, files collapsed to symbol counts (no symbol nodes).
	var d2 hierarchyOutput
	callJSON(t, cs, "hierarchy", map[string]any{"depth": 2, "service": "svcA"}, &d2)
	require.Len(t, d2.Roots, 1)
	dirs := d2.Roots[0].Children
	// dirs sorted: "." then "internal/parser"
	require.Len(t, dirs, 2)
	assert.Equal(t, ".", dirs[0].Name)
	assert.Equal(t, "internal/parser", dirs[1].Name)
	parseDir := dirs[1]
	require.Len(t, parseDir.Children, 2) // js.go, parse.go
	assert.Equal(t, "js.go", parseDir.Children[0].Name)
	assert.Equal(t, "file", parseDir.Children[0].Kind)
	assert.Equal(t, "internal/parser/js.go", parseDir.Children[0].File)
	assert.Equal(t, 2, parseDir.Children[1].Count) // parse.go has Parse+helper
	assert.Nil(t, parseDir.Children[1].Children)

	// depth 3 → top-level symbols with usable ids; ordered by line; no variable.
	var d3 hierarchyOutput
	callJSON(t, cs, "hierarchy", map[string]any{"depth": 3, "path": "internal/parser"}, &d3)
	require.Len(t, d3.Roots, 1) // path scoped out svcB and main.go
	pdir := d3.Roots[0].Children[0]
	assert.Equal(t, "internal/parser", pdir.Name)
	parseFile := pdir.Children[1] // parse.go
	require.Len(t, parseFile.Children, 2)
	assert.Equal(t, "Parse", parseFile.Children[0].Name) // line 10 before 30
	assert.Equal(t, "helper", parseFile.Children[1].Name)
	assert.Equal(t, "svcA:internal/parser/parse.go:function:Parse:10", parseFile.Children[0].ID)
	assert.Equal(t, "function", parseFile.Children[0].Kind)

	// service filter excludes svcB.
	for _, r := range d2.Roots {
		assert.NotEqual(t, "svcB", r.Name)
	}

	// max_tokens small → Truncated and the deepest level collapses to counts.
	var budgeted hierarchyOutput
	callJSON(t, cs, "hierarchy", map[string]any{"depth": 3, "max_tokens": 1}, &budgeted)
	assert.True(t, budgeted.Truncated)

	// Deterministic across runs.
	var again hierarchyOutput
	callJSON(t, cs, "hierarchy", map[string]any{"depth": 3, "path": "internal/parser"}, &again)
	a, _ := json.Marshal(d3)
	b, _ := json.Marshal(again)
	assert.JSONEq(t, string(a), string(b))
}

func TestSearchTool(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Nodes []graph.Node `json:"nodes"`
	}
	callJSON(t, cs, "search", map[string]any{"query": "getUser"}, &out)
	require.Len(t, out.Nodes, 1)
	assert.Equal(t, "be:getUser", out.Nodes[0].ID)
}

func TestContextTool_CarriesUnresolved(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Target     graph.Node            `json:"target"`
		Upstream   []map[string]any      `json:"upstream"`
		Downstream []map[string]any      `json:"downstream"`
		Unresolved []graph.UnresolvedRef `json:"unresolved"`
		Note       string                `json:"unresolved_note"`
	}
	callJSON(t, cs, "context", map[string]any{"target": "getUser"}, &out)

	assert.Equal(t, "be:getUser", out.Target.ID)
	require.Len(t, out.Upstream, 1)
	require.Len(t, out.Downstream, 1)

	// db.go is traversed; its unresolved ref must surface, unrelated.go's not.
	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynDispatch", out.Unresolved[0].Name)
	assert.Contains(t, out.Note, "verify this 1 unresolved reference manually")
}

func TestContextTool_CarriesTrust(t *testing.T) {
	store, idx := fixture()
	data, err := graph.EncodeTrustStamp(graph.TrustStamp{
		Measured: true, Corpus: "chessleap", Cases: 12, Recall: 1.0, MeasuredAt: "2026-07-19T10:31:00Z",
	})
	require.NoError(t, err)
	store.meta = map[string]string{graph.TrustStampMetaKey: string(data)}
	cs := connect(t, store, idx)

	var out struct {
		Trust graph.TrustStamp `json:"trust"`
	}
	callJSON(t, cs, "context", map[string]any{"target": "getUser"}, &out)

	assert.True(t, out.Trust.Measured)
	assert.Equal(t, "chessleap", out.Trust.Corpus)
	assert.InDelta(t, 1.0, out.Trust.Recall, 1e-9)
}

func TestContextTool_TrustUnmeasuredByDefault(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Trust graph.TrustStamp `json:"trust"`
	}
	callJSON(t, cs, "context", map[string]any{"target": "getUser"}, &out)

	assert.Equal(t, graph.TrustStamp{Measured: false}, out.Trust)
}

func TestContextTool_FilesMode(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	// db.go is called by handler.go (backend:getUser -> backend:queryDB), so
	// asking for files related to db.go returns handler.go with a direct ref.
	var out struct {
		Files      []string                 `json:"files"`
		Related    []graph.RelatedFileEntry `json:"related"`
		Unresolved []graph.UnresolvedRef    `json:"unresolved"`
	}
	callJSON(t, cs, "context", map[string]any{"files": []string{"db.go"}}, &out)

	assert.Equal(t, []string{"db.go"}, out.Files)
	require.NotEmpty(t, out.Related)
	assert.Equal(t, "handler.go", out.Related[0].File)
	assert.Equal(t, 1, out.Related[0].Refs)
	// db.go's own unresolved ref surfaces (seed file is in scope).
	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynDispatch", out.Unresolved[0].Name)
}

func TestContextTool_FilesModeMissing(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "context",
		Arguments: map[string]any{"files": []string{"nonexistent.go"}},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestContextTool_TargetAndFilesConflict(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "context",
		Arguments: map[string]any{"target": "getUser", "files": []string{"db.go"}},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

func TestImpactTool_NodeMode(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Target       graph.Node            `json:"target"`
		TotalCallers int                   `json:"total_callers"`
		Unresolved   []graph.UnresolvedRef `json:"unresolved"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB"}, &out)

	assert.Equal(t, "be:queryDB", out.Target.ID)
	assert.Equal(t, 2, out.TotalCallers)
	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynDispatch", out.Unresolved[0].Name)
}

func TestImpactTool_CarriesTrust(t *testing.T) {
	store, idx := fixture()
	data, err := graph.EncodeTrustStamp(graph.TrustStamp{
		Measured: true, Corpus: "chessleap", Cases: 12, Recall: 1.0, MeasuredAt: "2026-07-19T10:31:00Z",
	})
	require.NoError(t, err)
	store.meta = map[string]string{graph.TrustStampMetaKey: string(data)}
	cs := connect(t, store, idx)

	var out struct {
		Trust graph.TrustStamp `json:"trust"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB"}, &out)

	assert.True(t, out.Trust.Measured)
	assert.Equal(t, "chessleap", out.Trust.Corpus)
}

func TestImpactTool_FileMode(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		File     string           `json:"file"`
		Impacted []map[string]any `json:"impacted"`
	}
	callJSON(t, cs, "impact", map[string]any{"file": "db.go"}, &out)
	assert.Equal(t, "db.go", out.File)
	assert.NotEmpty(t, out.Impacted)
}

func TestImpactTool_RejectsBothAndNeither(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	for _, args := range []map[string]any{
		{},
		{"target": "queryDB", "file": "db.go"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "impact", Arguments: args})
		require.NoError(t, err)
		assert.True(t, res.IsError, "args %v should be rejected", args)
	}
}

func TestTraceTool_BackwardChain(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Chains     []string               `json:"chains"`
		Unresolved []graph.UnresolvedRef `json:"unresolved"`
	}
	callJSON(t, cs, "trace", map[string]any{"root": "queryDB", "direction": "backward"}, &out)

	require.Len(t, out.Chains, 1)
	assert.Contains(t, out.Chains[0], "fetchUser")
	assert.Contains(t, out.Chains[0], "queryDB")
	require.Len(t, out.Unresolved, 1)
}

func TestTraceTool_CarriesTrust(t *testing.T) {
	store, idx := fixture()
	data, err := graph.EncodeTrustStamp(graph.TrustStamp{
		Measured: true, Corpus: "chessleap", Cases: 12, Recall: 1.0, MeasuredAt: "2026-07-19T10:31:00Z",
	})
	require.NoError(t, err)
	store.meta = map[string]string{graph.TrustStampMetaKey: string(data)}
	cs := connect(t, store, idx)

	var out struct {
		Trust graph.TrustStamp `json:"trust"`
	}
	callJSON(t, cs, "trace", map[string]any{"root": "queryDB", "direction": "backward"}, &out)

	assert.True(t, out.Trust.Measured)
	assert.Equal(t, "chessleap", out.Trust.Corpus)
}

func TestContextTool_SummaryRollsUpPerFile(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Summary  bool             `json:"summary"`
		Files    []map[string]any `json:"files"`
		Upstream []map[string]any `json:"upstream"`
		Budget   map[string]any   `json:"budget"`
	}
	callJSON(t, cs, "context", map[string]any{"target": "getUser", "summary": true}, &out)

	assert.True(t, out.Summary)
	assert.NotEmpty(t, out.Files, "summary must carry file rollups")
	assert.Empty(t, out.Upstream, "summary must drop per-node detail")
	assert.Equal(t, "summary", out.Budget["level"])
}

func TestImpactTool_MaxTokensKeepsDetailWhenItFits(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Callers []map[string]any `json:"callers"`
		Budget  map[string]any   `json:"budget"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB", "max_tokens": 100000}, &out)

	assert.NotEmpty(t, out.Callers, "generous budget keeps per-node detail")
	assert.Equal(t, "detail", out.Budget["level"])
}

func TestImpactTool_TightMaxTokensRollsUp(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Summary bool             `json:"summary"`
		Files   []map[string]any `json:"files"`
		Budget  map[string]any   `json:"budget"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB", "max_tokens": 60}, &out)

	assert.True(t, out.Summary)
	assert.NotEmpty(t, out.Files)
	assert.Equal(t, "summary", out.Budget["level"])
}

// TestImpactTool_DefaultBudgetIsCompact verifies the MCP impact tool applies a
// compact token budget when the caller omits max_tokens: a small blast radius
// still returns full per-node detail, but the budget is stamped (proving the
// default is wired, not unlimited). This is what protects an agent's context
// from the verbose per-node dump on large radii.
func TestImpactTool_DefaultBudgetIsCompact(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Callers []map[string]any `json:"callers"`
		Budget  map[string]any   `json:"budget"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB"}, &out)

	assert.NotEmpty(t, out.Callers, "small radius fits the default budget: detail kept")
	require.NotNil(t, out.Budget, "default run must stamp a budget, not run unlimited")
	assert.Equal(t, "detail", out.Budget["level"])
	assert.Equal(t, float64(defaultImpactBudget), out.Budget["max_tokens"])
}

// TestImpactTool_NegativeMaxTokensIsUnlimited verifies a negative max_tokens
// opts out of the compact default: full detail with no budget cap applied.
func TestImpactTool_NegativeMaxTokensIsUnlimited(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Callers []map[string]any `json:"callers"`
		Budget  map[string]any   `json:"budget"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB", "max_tokens": -1}, &out)

	assert.NotEmpty(t, out.Callers, "unlimited keeps per-node detail")
	assert.Nil(t, out.Budget, "negative max_tokens means unlimited: no budget stamp")
}

func TestUnknownTargetIsToolError(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "context", Arguments: map[string]any{"target": "doesNotExist"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}

// ─── A.2 tests ───────────────────────────────────────────────────────────────

// fixtureWithVerification builds a graph with edges carrying verification states.
// fetchUser --verified--> getUser --candidate--> queryDB
func fixtureWithVerification() (*fakeStore, *graph.AdjacencyIndex) {
	nodes := []*graph.Node{
		{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10},
		{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "getUser", Service: "backend", File: "handler.go", Line: 20},
		{ID: "be:queryDB", Type: graph.NodeTypeFunction, Label: "queryDB", Service: "backend", File: "db.go", Line: 40},
	}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	idx.AddEdge(&graph.Edge{
		ID: "e1", From: "fe:fetchUser", To: "be:getUser",
		Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic,
		VerificationState: graph.StateVerified,
	})
	idx.AddEdge(&graph.Edge{
		ID: "e2", From: "be:getUser", To: "be:queryDB",
		Type:              graph.EdgeTypeCalls,
		VerificationState: graph.StateCandidate,
	})
	store := &fakeStore{nodes: nodes}
	return store, idx
}

// TestMinVerificationPasses_UnitTable covers the filter helper in isolation.
func TestMinVerificationPasses_UnitTable(t *testing.T) {
	cases := []struct {
		state  string
		filter string
		want   bool
	}{
		// "any" (default) passes everything
		{graph.StateVerified, "any", true},
		{graph.StateCandidate, "any", true},
		{graph.StateObservedOnlyGap, "any", true},
		{"", "any", true},
		// empty filter = "any"
		{graph.StateCandidate, "", true},
		// "verified" passes only verified
		{graph.StateVerified, "verified", true},
		{graph.StateCandidate, "verified", false},
		{graph.StateObservedOnlyGap, "verified", false},
		{"", "verified", false},
		// "declared" is equivalent to "verified" with current state set
		{graph.StateVerified, "declared", true},
		{graph.StateCandidate, "declared", false},
		// "observed" passes verified + observed_only_gap
		{graph.StateVerified, "observed", true},
		{graph.StateObservedOnlyGap, "observed", true},
		{graph.StateCandidate, "observed", false},
		{"", "observed", false},
	}
	for _, tc := range cases {
		got := minVerificationPasses(tc.state, tc.filter)
		assert.Equal(t, tc.want, got, "state=%q filter=%q", tc.state, tc.filter)
	}
}

// TestImpactTool_MinVerificationFiltersCallers verifies that min_verification="verified"
// removes callers reached via a candidate edge, and the summary still shows
// the pre-filter candidate count (filtered counts stay visible per spec).
//
// Graph: fetchUser --(verified)--> getUser --(candidate)--> queryDB
// Ancestors of queryDB: getUser (via candidate e2), fetchUser (via verified e1).
// After "verified" filter: getUser is removed, fetchUser survives.
func TestImpactTool_MinVerificationFiltersCallers(t *testing.T) {
	store, idx := fixtureWithVerification()
	cs := connect(t, store, idx)

	var out struct {
		Callers             []map[string]any          `json:"callers"`
		TotalCallers        int                       `json:"total_callers"`
		VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	}
	callJSON(t, cs, "impact", map[string]any{
		"target":           "queryDB",
		"min_verification": "verified",
	}, &out)

	// getUser (reached via candidate edge) must be removed; fetchUser (via verified) stays.
	require.Len(t, out.Callers, 1, "only caller via verified edge must survive")
	assert.Equal(t, "fe:fetchUser", out.Callers[0]["id"])
	assert.Equal(t, 1, out.TotalCallers)
	// summary still shows candidate=1 (pre-filter — filtered counts stay visible)
	assert.Equal(t, 1, out.VerificationSummary.Candidate, "summary must reflect unfiltered counts")
}

// TestImpactTool_MinVerificationAnyReturnsAll verifies the default returns all callers.
func TestImpactTool_MinVerificationAnyReturnsAll(t *testing.T) {
	store, idx := fixtureWithVerification()
	cs := connect(t, store, idx)

	var out struct {
		Callers []map[string]any `json:"callers"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB"}, &out)
	// queryDB has 2 ancestors: getUser (depth 1) and fetchUser (depth 2)
	assert.Equal(t, 2, len(out.Callers), "default any must return all callers")
}

// TestContextTool_MinVerificationFiltersNodes verifies upstream/downstream filtering.
func TestContextTool_MinVerificationFiltersNodes(t *testing.T) {
	store, idx := fixtureWithVerification()
	cs := connect(t, store, idx)

	var out struct {
		Upstream            []map[string]any          `json:"upstream"`
		Downstream          []map[string]any          `json:"downstream"`
		VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	}
	// getUser: upstream=fetchUser (verified), downstream=queryDB (candidate)
	callJSON(t, cs, "context", map[string]any{
		"target":           "getUser",
		"task":             "debug",
		"min_verification": "verified",
	}, &out)

	// downstream (candidate edge to queryDB) must be filtered
	assert.Equal(t, 0, len(out.Downstream), "candidate downstream must be filtered")
	// upstream (verified edge from fetchUser) must survive
	assert.Equal(t, 1, len(out.Upstream), "verified upstream must survive filter")
	// summary still counts the candidate edge
	assert.Equal(t, 1, out.VerificationSummary.Candidate)
}

// TestTraceTool_MinVerificationFiltersChains verifies chain filtering.
func TestTraceTool_MinVerificationFiltersChains(t *testing.T) {
	store, idx := fixtureWithVerification()
	cs := connect(t, store, idx)

	var out struct {
		Chains              []string                  `json:"chains"`
		VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	}
	// backward trace from queryDB: chain fetchUser->getUser->queryDB has a candidate hop
	callJSON(t, cs, "trace", map[string]any{
		"root":             "queryDB",
		"direction":        "backward",
		"min_verification": "verified",
	}, &out)

	// the chain contains a candidate edge → must be filtered out
	assert.Equal(t, 0, len(out.Chains), "chain with candidate hop must be filtered")
	// summary still shows the candidate count
	assert.GreaterOrEqual(t, out.VerificationSummary.Candidate, 1)
}

// TestToolDescriptionsContainSemanticsParagraph guards accidental regression of
// the semantics teaching text in the tool descriptions.
func TestToolDescriptionsContainSemanticsParagraph(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	semanticTools := map[string]bool{"context": false, "impact": false, "trace": false, "flows": false}
	for _, tool := range tools.Tools {
		if _, ok := semanticTools[tool.Name]; ok {
			assert.Contains(t, tool.Description, "verification_state",
				"tool %s description must contain semantics paragraph", tool.Name)
			assert.Contains(t, tool.Description, "candidate",
				"tool %s description must mention candidate state", tool.Name)
			assert.Contains(t, tool.Description, "observed_only_gap",
				"tool %s description must mention observed_only_gap state", tool.Name)
			semanticTools[tool.Name] = true
		}
	}
	for name, found := range semanticTools {
		assert.True(t, found, "tool %s not found in ListTools", name)
	}
}

// connectWithSearcher creates an MCP server backed by a real SQLite store so
// that a semantic.Searcher can be wired.  The store is pre-seeded with nodes.
func connectWithSearcher(t *testing.T, nodes []*graph.Node) (*mcp.ClientSession, *Server) {
	t.Helper()
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	for _, n := range nodes {
		require.NoError(t, store.UpsertNode(ctx, n))
		// Insert into entities_fts so the FTS arm can find this node.
		cardText := n.Label + " " + string(n.Type) + " " + n.Service + " " + n.File
		_, err = store.DB().ExecContext(ctx,
			`INSERT OR REPLACE INTO embeddings (entity_id, entity_type, content_hash, embedder_id, dims, vector, meta)
			 VALUES (?, 'node', 'hash', 'stub-v1', 4, X'00000000000000000000000000000000', '{}')`,
			n.ID)
		require.NoError(t, err)
		_, err = store.DB().ExecContext(ctx, `DELETE FROM entities_fts WHERE entity_id = ?`, n.ID)
		require.NoError(t, err)
		_, err = store.DB().ExecContext(ctx,
			`INSERT INTO entities_fts (entity_id, entity_type, text) VALUES (?, 'node', ?)`,
			n.ID, cardText)
		require.NoError(t, err)
	}

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	sem := semantic.NewStore(store.DB())
	sr := semantic.NewSearcher(sem, nil, nil) // nil embedder → FTS-only

	srv, handle := New(store, idx, "test", 0, true)
	handle.SetSearcher(sr)

	st, ct := mcp.NewInMemoryTransports()
	_, err = srv.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return cs, handle
}

// TestSearchTool_HybridRoundTrip verifies that when a Searcher is wired the
// search tool returns a semantic.Response (nodes/flows/docs sections) rather
// than the legacy []*graph.Node format.
func TestSearchTool_HybridRoundTrip(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "fn:getUser", Type: graph.NodeTypeFunction, Label: "getUser",
			Service: "backend", File: "user.go", Line: 10, Language: "go"},
		{ID: "fn:createUser", Type: graph.NodeTypeFunction, Label: "createUser",
			Service: "backend", File: "user.go", Line: 20, Language: "go"},
	}
	cs, _ := connectWithSearcher(t, nodes)

	var resp semantic.Response
	callJSON(t, cs, "search", map[string]any{"query": "getUser"}, &resp)

	require.NotEmpty(t, resp.Nodes, "hybrid search must return node hits")
	found := false
	for _, h := range resp.Nodes {
		if h.Entity.ID == "fn:getUser" {
			found = true
			assert.NotEmpty(t, h.Retrieval, "hit must have retrieval label")
			assert.Greater(t, h.Score, 0.0, "hit must have positive score")
		}
	}
	assert.True(t, found, "fn:getUser should appear in search results")
	// Semantic field: FTS-only searcher should carry a degradation note.
	assert.NotEmpty(t, resp.Semantic, "nil embedder must produce semantic degradation note")
}

// TestSearchTool_HybridDescription verifies the search tool description mentions
// natural language and flows (S.2 requirement).
func TestSearchTool_HybridDescription(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name == "search" {
			assert.Contains(t, tool.Description, "natural language",
				"search tool description must mention natural language")
			assert.Contains(t, tool.Description, "flows",
				"search tool description must mention flows")
			return
		}
	}
	t.Error("search tool not found")
}

// ── B.3 target resolution tests ───────────────────────────────────────────────

// fixtureAmbiguous creates a store with two exact-label matches for "Login"
// (server service Go function + ui service component).
func fixtureAmbiguous() (*fakeStore, *graph.AdjacencyIndex) {
	srvLogin := &graph.Node{ID: "srv:Login", Type: graph.NodeTypeFunction, Label: "Login", Service: "server", File: "api/session.go", Line: 10, Language: "go"}
	uiLogin := &graph.Node{ID: "ui:Login", Type: graph.NodeTypeComponent, Label: "Login", Service: "ui", File: "ui/src/Login.tsx", Line: 5, Language: "typescript"}
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(srvLogin)
	idx.AddNode(uiLogin)
	store := &fakeStore{nodes: []*graph.Node{uiLogin, srvLogin}} // ui returned first (ambiguous default)
	return store, idx
}

// TestImpactTool_TargetServiceFilter verifies that target_service pins resolution
// to the correct service and the result targets the server Login node.
func TestImpactTool_TargetServiceFilter(t *testing.T) {
	store, idx := fixtureAmbiguous()
	cs := connect(t, store, idx)

	var out struct {
		Target           *graph.Node             `json:"target"`
		TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	}
	callJSON(t, cs, "impact", map[string]any{
		"target":         "Login",
		"target_service": "server",
	}, &out)

	require.NotNil(t, out.Target)
	assert.Equal(t, "srv:Login", out.Target.ID, "target_service=server must pick the server node")
	// Two exact matches → candidates populated.
	assert.Len(t, out.TargetCandidates, 2, "two exact matches must populate target_candidates")
}

// TestImpactTool_AmbiguityInResponse verifies that without filters the response
// still populates target_candidates when >1 exact match exists.
func TestImpactTool_AmbiguityInResponse(t *testing.T) {
	store, idx := fixtureAmbiguous()
	cs := connect(t, store, idx)

	var out struct {
		Target           *graph.Node             `json:"target"`
		TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "Login"}, &out)

	require.NotNil(t, out.Target)
	// Two exact matches → candidates non-empty even without filter.
	assert.Len(t, out.TargetCandidates, 2, "ambiguous result must have target_candidates")
}

// TestImpactTool_UnambiguousEmptyCandidates verifies that a unique target has
// an empty target_candidates array ([] not absent).
func TestImpactTool_UnambiguousEmptyCandidates(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out struct {
		Target           *graph.Node             `json:"target"`
		TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	}
	callJSON(t, cs, "impact", map[string]any{"target": "queryDB"}, &out)

	require.NotNil(t, out.Target)
	assert.NotNil(t, out.TargetCandidates, "target_candidates must be present (never absent)")
	assert.Empty(t, out.TargetCandidates, "unambiguous target must have empty target_candidates")
}

// TestToolDescriptionsContainTargetCandidatesHint guards that all three query
// tools mention target_candidates in their description (B.3 MCP contract).
func TestToolDescriptionsContainTargetCandidatesHint(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	for _, tool := range tools.Tools {
		switch tool.Name {
		case "context", "impact", "trace", "flows":
			assert.Contains(t, tool.Description, "target_candidates",
				"tool %s description must mention target_candidates", tool.Name)
			assert.Contains(t, tool.Description, "target_service",
				"tool %s description must mention target_service", tool.Name)
		}
	}
}
