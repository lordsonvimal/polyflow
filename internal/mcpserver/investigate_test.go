package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

// investigateFixtureFiles backs the shared fixture()'s three files (api.js,
// handler.go, db.go) with real source on disk, in a temp dir chdir'd for the
// test, so budget.Snippet has something to read.
func investigateFixtureFiles(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// fixture()'s node Line values (fetchUser:10, getUser:20, queryDB:40) are
	// padded to with blank lines so budget.Snippet can read real source there.
	pad := func(n int) string {
		s := ""
		for i := 0; i < n; i++ {
			s += "\n"
		}
		return s
	}
	files := map[string]string{
		"api.js":     pad(9) + "function fetchUser() {\n  return fetch('/user');\n}\n",
		"handler.go": pad(19) + "func getUser() {\n\tqueryDB()\n}\n",
		"db.go":      pad(39) + "func queryDB() {\n\treturn nil\n}\n",
	}
	for name, src := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644))
	}
	t.Chdir(dir)
}

// investigateOut mirrors investigateOutput for decoding tool responses in tests.
type investigateOut struct {
	Root                graph.Node                `json:"root"`
	Snippet             string                    `json:"snippet"`
	Candidates          []resolveCandidate        `json:"candidates"`
	Ambiguous           []graph.TargetCandidate   `json:"target_candidates"`
	Callers             []investigateNode         `json:"callers"`
	Callees             []investigateNode         `json:"callees"`
	Flows               []investigateFlow         `json:"flows"`
	Unresolved          []graph.UnresolvedRef     `json:"coverage_unresolved"`
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	Trust               graph.TrustStamp          `json:"trust"`
	Epistemic           graph.Epistemic           `json:"epistemic"`
	Note                string                    `json:"note"`
	Budget              *struct {
		MaxTokens       int      `json:"max_tokens"`
		EstimatedTokens int      `json:"estimated_tokens"`
		Level           string   `json:"level"`
		Notes           []string `json:"notes,omitempty"`
	} `json:"budget"`
}

func TestInvestigateTool_ResolveAndAssemble(t *testing.T) {
	investigateFixtureFiles(t)
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "getUser"}, &out)

	require.Equal(t, "be:getUser", out.Root.ID)
	assert.NotEmpty(t, out.Snippet, "root snippet should inline the resolved node's source")
	assert.Empty(t, out.Ambiguous)

	require.Len(t, out.Callers, 1)
	assert.Equal(t, "fe:fetchUser", out.Callers[0].ID)
	require.Len(t, out.Callees, 1)
	assert.Equal(t, "be:queryDB", out.Callees[0].ID)

	// coverage_unresolved is scoped to the traversed files: db.go's unresolved
	// ref surfaces, unrelated.go's does not (mirrors TestContextTool_CarriesUnresolved).
	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynDispatch", out.Unresolved[0].Name)

	// getUser -> queryDB is a downstream flow chain.
	require.NotEmpty(t, out.Flows)
	found := false
	for _, f := range out.Flows {
		if len(f.Chain) == 2 && f.Chain[0].ID == "be:getUser" && f.Chain[1].ID == "be:queryDB" {
			found = true
			assert.NotEmpty(t, f.Chain[1].Snippet)
		}
	}
	assert.True(t, found, "expected a getUser -> queryDB flow chain, got %+v", out.Flows)
}

// TestInvestigateTool_DOMContractFlow_ReachesJSSite is the IA.5 worked
// example end to end: investigate resolves to the templ component, and its
// downstream flow must reach the JS clone site through the dom_contract edge
// in the same call — the property that makes the neighbourhood
// self-complete instead of stopping one file short (doc §0 step 3 / §1.4).
func TestInvestigateTool_DOMContractFlow_ReachesJSSite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "promotionbutton.templ"),
		[]byte("templ PromotionButtonForColor(color, piece string) {\n\t<button data-testid={ \"promotion-button-\" + piece }></button>\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "board.js"),
		[]byte("function applyDom(move) {\n  document.querySelector(`[data-testid=\"promotion-button-${move.promotion}\"]`);\n}\n"), 0o644))
	t.Chdir(dir)

	compNode := &graph.Node{
		ID:   "chessleap:promotionbutton.templ:component:PromotionButtonForColor:1",
		Type: graph.NodeTypeComponent, Label: "PromotionButtonForColor",
		Service: "chessleap", File: "promotionbutton.templ", Line: 1, Language: "templ",
		Meta: map[string]string{"dom_data_attrs": "data-testid=promotion-button-*@2"},
	}
	domTargetNode := &graph.Node{
		ID:   "chessleap:board.js:dom_target:query_selector:2",
		Type: graph.NodeTypeDOMTarget, Label: "querySelector", Service: "chessleap", File: "board.js", Line: 2, Language: "javascript",
		Meta: map[string]string{"fn": "querySelector", "selector": "`[data-testid=\"promotion-button-${move.promotion}\"]`"},
	}
	nodes := []*graph.Node{compNode, domTargetNode}

	_, contractEdges, _ := linker.LinkDOMContracts([]graph.Node{*compNode, *domTargetNode})
	require.Len(t, contractEdges, 1, "precondition: the linker must produce the dom_contract edge under test")

	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	for _, e := range contractEdges {
		ec := e
		idx.AddEdge(&ec)
	}
	store := &fakeStore{nodes: nodes}
	cs := connect(t, store, idx)

	var out investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "PromotionButtonForColor"}, &out)

	require.Equal(t, compNode.ID, out.Root.ID)
	require.NotEmpty(t, out.Callees, "the dom_contract edge should surface as a callee (context traverses all edge types)")
	assert.Equal(t, domTargetNode.ID, out.Callees[0].ID)

	found := false
	for _, f := range out.Flows {
		if len(f.Chain) == 2 && f.Chain[0].ID == compNode.ID && f.Chain[1].ID == domTargetNode.ID {
			found = true
		}
	}
	assert.True(t, found, "expected a component -> board.js dom_contract flow chain, got %+v", out.Flows)
}

func TestInvestigateTool_AmbiguousCandidatesSurfaced(t *testing.T) {
	investigateFixtureFiles(t)
	// Two exact-label matches for "getUser" in different services.
	nodes := []*graph.Node{
		{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "getUser", Service: "backend", File: "handler.go", Line: 3, Language: "go"},
		{ID: "be2:getUser", Type: graph.NodeTypeHTTPHandler, Label: "getUser", Service: "backend2", File: "handler.go", Line: 3, Language: "go"},
	}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	store := &fakeStore{nodes: nodes}
	cs := connect(t, store, idx)

	var out investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "getUser"}, &out)

	require.NotEmpty(t, out.Root.ID, "an ambiguous match still resolves to a best root, like context/impact/trace")
	require.Len(t, out.Ambiguous, 2)
	assert.Contains(t, out.Note, "target_service")
}

func TestInvestigateTool_BudgetCollapsesCandidatesThenFlowsThenSnippets(t *testing.T) {
	investigateFixtureFiles(t)
	store, idx := fixture()
	cs := connect(t, store, idx)

	var full investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "getUser"}, &full)
	require.NotEmpty(t, full.Callers[0].Snippet, "precondition: full detail carries neighbour snippets")

	var tight investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "getUser", "max_tokens": 1}, &tight)

	require.NotNil(t, tight.Budget)
	assert.Equal(t, "summary", tight.Budget.Level)
	assert.Empty(t, tight.Candidates, "candidates drop first")
	// coverage_unresolved and the root snippet survive even the tightest budget.
	assert.Equal(t, full.Unresolved, tight.Unresolved)
	assert.NotEmpty(t, tight.Snippet)
	// epistemic must survive the tightest budget too (EE.0), same as
	// coverage_unresolved/verification_summary/trust.
	assert.Equal(t, full.Epistemic, tight.Epistemic)
}

// TestInvestigateTool_CarriesEpistemic verifies the epistemic verdict (EE.0)
// round-trips over MCP, derived from this call's own verification_summary/
// trust/coverage_unresolved sections rather than recomputed independently.
func TestInvestigateTool_CarriesEpistemic(t *testing.T) {
	investigateFixtureFiles(t)
	store, idx := fixture()
	cs := connect(t, store, idx)

	var out investigateOut
	callJSON(t, cs, "investigate", map[string]any{"query": "getUser"}, &out)

	require.NotEmpty(t, out.Unresolved, "precondition: fixture() has a dynDispatch unresolved ref in scope")
	assert.False(t, out.Trust.Measured, "precondition: no trust stamp loaded in the test store")
	assert.Equal(t, graph.EpistemicLowerBound, out.Epistemic.Verdict)
	assert.Contains(t, out.Epistemic.Causes, graph.CauseUnresolvedReference)
	assert.Contains(t, out.Epistemic.Causes, graph.CauseUnmeasuredTrust)
}

func TestInvestigateTool_QueryRequired(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "investigate", Arguments: map[string]any{}})
	require.NoError(t, err)
	assert.True(t, res.IsError, "empty query should return a tool error")
}
