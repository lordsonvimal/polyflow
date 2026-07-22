package mcpserver

import (
	"context"
	"encoding/json"
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
	srv, _ := New(store, idx, "test", 0)
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
	assert.ElementsMatch(t, []string{"search", "context", "impact", "trace"}, names)
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
		Chains []struct {
			Text string `json:"text"`
		} `json:"chains"`
		Unresolved []graph.UnresolvedRef `json:"unresolved"`
	}
	callJSON(t, cs, "trace", map[string]any{"root": "queryDB", "direction": "backward"}, &out)

	require.Len(t, out.Chains, 1)
	assert.Contains(t, out.Chains[0].Text, "fetchUser")
	assert.Contains(t, out.Chains[0].Text, "queryDB")
	require.Len(t, out.Unresolved, 1)
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
		Type: graph.EdgeTypeCalls,
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
		Upstream   []map[string]any          `json:"upstream"`
		Downstream []map[string]any          `json:"downstream"`
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
		Chains []struct {
			Text string `json:"text"`
		} `json:"chains"`
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

	semanticTools := map[string]bool{"context": false, "impact": false, "trace": false}
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

	srv, handle := New(store, idx, "test", 0)
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
