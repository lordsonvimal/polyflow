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
	srv, _ := New(store, idx, "test")
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

func TestUnknownTargetIsToolError(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "context", Arguments: map[string]any{"target": "doesNotExist"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
