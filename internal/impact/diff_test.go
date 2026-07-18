package impact_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// diffFixtureIndex builds:
//
//	backend:main (main.go:3) --calls--> backend:handleUser (handler.go:10-30, end_line)
//	backend:main             --calls--> backend:helper (handler.go:40, open-ended)
//	frontend:fetchUser (api.js:10) --http_call--> backend:handleUser
//	backend:cfg (variable, handler.go:5) with no edges
func diffFixtureIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "be:main", Type: graph.NodeTypeFunction, Label: "main", Service: "backend", File: "main.go", Line: 3, Meta: map[string]string{"end_line": "8"}})
	idx.AddNode(&graph.Node{ID: "be:handleUser", Type: graph.NodeTypeFunction, Label: "handleUser", Service: "backend", File: "handler.go", Line: 10, Meta: map[string]string{"end_line": "30"}})
	idx.AddNode(&graph.Node{ID: "be:helper", Type: graph.NodeTypeFunction, Label: "helper", Service: "backend", File: "handler.go", Line: 40})
	idx.AddNode(&graph.Node{ID: "be:cfg", Type: graph.NodeTypeVariable, Label: "cfg", Service: "backend", File: "handler.go", Line: 5})
	idx.AddNode(&graph.Node{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "be:main", To: "be:handleUser", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "be:main", To: "be:helper", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "fe:fetchUser", To: "be:handleUser", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic})
	return idx
}

func TestBuildDiff_BodyChangeSeedsEnclosingFunction(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 16}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	require.Len(t, out.Targets, 1)
	assert.Equal(t, "be:handleUser", out.Targets[0].Node.ID)
	assert.Equal(t, []gitdiff.Span{{Start: 15, End: 16}}, out.Targets[0].Spans)
	assert.Empty(t, out.Unmapped)

	// Blast radius: main (calls) and fetchUser (http_call), both depth 1.
	require.Equal(t, 2, out.TotalCallers)
	ids := []string{out.Callers[0].ID, out.Callers[1].ID}
	assert.ElementsMatch(t, []string{"be:main", "fe:fetchUser"}, ids)
	assert.ElementsMatch(t, []string{"backend", "frontend"}, out.ServicesAffected)
}

func TestBuildDiff_DeclarationLineSeedsPointNode(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 5, End: 5}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	require.Len(t, out.Targets, 1)
	assert.Equal(t, "be:cfg", out.Targets[0].Node.ID)
	assert.Equal(t, 0, out.TotalCallers)
	// A changed node with zero callers still marks its service as affected.
	assert.Equal(t, []string{"backend"}, out.ServicesAffected)
}

func TestBuildDiff_OpenEndedEnclosingFallback(t *testing.T) {
	idx := diffFixtureIndex()
	// Lines 45-46 are past helper's declaration (line 40, no end_line):
	// treated as inside its open-ended body.
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 45, End: 46}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	require.Len(t, out.Targets, 1)
	assert.Equal(t, "be:helper", out.Targets[0].Node.ID)
	assert.Empty(t, out.Unmapped)
}

func TestBuildDiff_UnmappedHunksAreReported(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		// File with nodes, but the span precedes them all (e.g. imports).
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 1, End: 2}}},
		// File the graph knows nothing about.
		{Path: "README.md", Spans: []gitdiff.Span{{Start: 1, End: 3}}},
		// Deleted file.
		{Path: "gone.go", Deleted: true},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	assert.Empty(t, out.Targets)
	require.Len(t, out.Unmapped, 3)
	assert.Equal(t, "handler.go", out.Unmapped[0].File)
	assert.Equal(t, "no node overlaps this span", out.Unmapped[0].Reason)
	assert.Equal(t, "README.md", out.Unmapped[1].File)
	assert.Equal(t, "file has no nodes in the graph", out.Unmapped[1].Reason)
	assert.Equal(t, "gone.go", out.Unmapped[2].File)
	assert.Nil(t, out.Unmapped[2].Span)
	assert.Contains(t, out.Unmapped[2].Reason, "deleted")
}

func TestBuildDiff_UnionKeepsMinDepthAndDeduplicates(t *testing.T) {
	idx := diffFixtureIndex()
	// helper (depth 1 from main) and handleUser (depth 1 from main too):
	// main must appear once at depth 1, not twice.
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 12, End: 12}, {Start: 45, End: 45}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	require.Len(t, out.Targets, 2)
	seen := map[string]int{}
	for _, c := range out.Callers {
		seen[c.ID]++
	}
	assert.Equal(t, 1, seen["be:main"])
	assert.Equal(t, 1, seen["fe:fetchUser"])
}

func TestBuildDiff_SeedsExcludedFromCallers(t *testing.T) {
	idx := diffFixtureIndex()
	// Both handleUser and its caller main changed: main is a target, so it
	// must not also appear in the caller list.
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 12, End: 12}}},
		{Path: "main.go", Spans: []gitdiff.Span{{Start: 4, End: 4}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	require.Len(t, out.Targets, 2)
	for _, c := range out.Callers {
		assert.NotEqual(t, "be:main", c.ID)
	}
	require.Equal(t, 1, out.TotalCallers)
	assert.Equal(t, "fe:fetchUser", out.Callers[0].ID)
}

func TestBuildDiff_ServiceFilter(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 15}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "backend", false)

	require.Equal(t, 1, out.TotalCallers)
	assert.Equal(t, "be:main", out.Callers[0].ID)
}

func TestBuildDiff_UnresolvedAndUnmappedDefaultToEmptyNotNull(t *testing.T) {
	idx := diffFixtureIndex()
	out := impact.BuildDiff(idx, nil, 10, "", false)

	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"unresolved":[]`)
	assert.Contains(t, string(data), `"unmapped_hunks":[]`)
	assert.Contains(t, string(data), `"targets":[]`)
}

func TestDiffResult_AttachUnresolvedScopedToChangedAndCallerFiles(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 15}}},
		{Path: "README.md", Spans: []gitdiff.Span{{Start: 1, End: 1}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)
	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 20, Name: "dynCall", Kind: "call_ref"},
		{Service: "backend", File: "README.md", Line: 1, Name: "docRef", Kind: "call_ref"},
		{Service: "backend", File: "unrelated.go", Line: 3, Name: "other", Kind: "call_ref"},
	})

	require.Len(t, out.Unresolved, 2)
	assert.Contains(t, out.UnresolvedNote, "verify these 2 unresolved references manually")
}

func TestDiffResult_ApplyBudgetRollsUpAndKeepsBlindSpots(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 15}}},
		{Path: "README.md", Spans: []gitdiff.Span{{Start: 1, End: 1}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)
	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 20, Name: "dynCall", Kind: "call_ref"},
	})

	budgeted := out.ApplyBudget(1, false) // absurdly small: forces the rollup
	s, ok := budgeted.(*impact.DiffSummary)
	require.True(t, ok)
	assert.True(t, s.Summary)
	// Blind spots survive any budget.
	assert.Len(t, s.Unresolved, 1)
	assert.Len(t, s.Unmapped, 1)
	assert.NotEmpty(t, s.Targets)
	assert.Equal(t, "summary", s.Budget.Level)
}

func TestDiffResult_ApplyBudgetKeepsDetailWhenItFits(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 15}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false)

	budgeted := out.ApplyBudget(100000, false)
	r, ok := budgeted.(*impact.DiffResult)
	require.True(t, ok)
	assert.Equal(t, "detail", r.Budget.Level)
}
