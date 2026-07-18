package context_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/budget"
	ctx "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestSummarize_GroupsNodesByFile(t *testing.T) {
	idx := fixtureIndex()
	// Second downstream callee in db.go so the rollup has something to group.
	idx.AddNode(&graph.Node{ID: "be:scanRows", Type: graph.NodeTypeFunction, Label: "scanRows", Service: "backend", File: "db.go", Line: 60, Language: "go"})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "be:getUser", To: "be:scanRows", Type: graph.EdgeTypeCalls})

	s := ctx.Build(idx, "be:getUser", "debug", 5, false).Summarize()

	assert.True(t, s.Summary)
	assert.Equal(t, "be:getUser", s.Target.ID)
	require.Len(t, s.Files, 2)

	// Sorted by min depth: upstream api.js (depth 1) ties with db.go (depth 1);
	// tie breaks on file name.
	assert.Equal(t, "api.js", s.Files[0].File)
	assert.Equal(t, "upstream", s.Files[0].Direction)
	assert.Equal(t, 1, s.Files[0].Nodes)
	assert.Equal(t, []string{"http_call"}, s.Files[0].EdgeTypes)

	assert.Equal(t, "db.go", s.Files[1].File)
	assert.Equal(t, "downstream", s.Files[1].Direction)
	assert.Equal(t, 2, s.Files[1].Nodes)
	assert.Equal(t, 1, s.Files[1].MinDepth)
	assert.Equal(t, []string{"calls"}, s.Files[1].EdgeTypes)
}

func TestSummarize_FileInBothDirectionsMarkedBoth(t *testing.T) {
	idx := fixtureIndex()
	// getUser also calls back into api.js, putting that file both up- and
	// downstream of the target.
	idx.AddNode(&graph.Node{ID: "fe:renderUser", Type: graph.NodeTypeFunction, Label: "renderUser", Service: "frontend", File: "api.js", Line: 30, Language: "javascript"})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "be:getUser", To: "fe:renderUser", Type: graph.EdgeTypeCalls})

	s := ctx.Build(idx, "be:getUser", "debug", 5, false).Summarize()

	var apiJS *ctx.FileRollup
	for i := range s.Files {
		if s.Files[i].File == "api.js" {
			apiJS = &s.Files[i]
		}
	}
	require.NotNil(t, apiJS)
	assert.Equal(t, "both", apiJS.Direction)
	assert.Equal(t, 2, apiJS.Nodes)
}

func TestApplyBudget_DetailWhenItFits(t *testing.T) {
	idx := fixtureIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false)
	r.AttachUnresolved(nil)

	out := r.ApplyBudget(100000, false)

	detail, ok := out.(*ctx.Result)
	require.True(t, ok, "generous budget must keep per-node detail")
	require.NotNil(t, detail.Budget)
	assert.Equal(t, budget.LevelDetail, detail.Budget.Level)
	assert.Positive(t, detail.Budget.EstimatedTokens)
}

func TestApplyBudget_SummaryWhenOverBudget(t *testing.T) {
	idx := fixtureIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false)
	r.AttachUnresolved(nil)

	out := r.ApplyBudget(120, false)

	s, ok := out.(*ctx.Summary)
	require.True(t, ok, "tight budget must roll up to summary")
	require.NotNil(t, s.Budget)
	assert.Equal(t, budget.LevelSummary, s.Budget.Level)
	assert.Contains(t, s.Budget.Note, "rolled up per file")
	assert.NotEmpty(t, s.Files)
}

func TestApplyBudget_ForceSummary(t *testing.T) {
	idx := fixtureIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false)
	r.AttachUnresolved(nil)

	out := r.ApplyBudget(0, true)

	s, ok := out.(*ctx.Summary)
	require.True(t, ok)
	assert.Equal(t, budget.LevelSummary, s.Budget.Level)
	// Explicitly requested: no over-budget note.
	assert.Empty(t, s.Budget.Note)
}

func TestApplyBudget_TrimsSummaryFilesAndCarriesUnresolvedWhole(t *testing.T) {
	idx := fixtureIndex()
	// Fan out callees across many files so the summary itself exceeds a tiny
	// budget and must trim its file list.
	for i := 0; i < 20; i++ {
		id := string(rune('a'+i)) + ":callee"
		file := "pkg/file_" + string(rune('a'+i)) + ".go"
		idx.AddNode(&graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: "callee", Service: "backend", File: file, Line: 1, Language: "go"})
		idx.AddEdge(&graph.Edge{ID: "fan" + id, From: "be:getUser", To: id, Type: graph.EdgeTypeCalls})
	}
	r := ctx.Build(idx, "be:getUser", "debug", 5, false)
	r.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 21, Name: "dyn", Kind: "call_ref"},
	})

	out := r.ApplyBudget(150, false)

	s, ok := out.(*ctx.Summary)
	require.True(t, ok)
	assert.Positive(t, s.Budget.OmittedFiles, "tiny budget must omit files")
	assert.NotEmpty(t, s.Files, "at least one file survives any budget")
	assert.Contains(t, s.Budget.Note, "omitted")
	// The recall gauge is never trimmed to save tokens.
	require.Len(t, s.Unresolved, 1)
	assert.Equal(t, s.UnresolvedNote, r.UnresolvedNote)
}

func TestInlineSnippets_CopiesTargetAndSkipsMissingFiles(t *testing.T) {
	idx := fixtureIndex()
	shared := idx.Nodes["be:getUser"]
	r := ctx.Build(idx, "be:getUser", "debug", 5, false)

	r.InlineSnippets(t.TempDir(), 3) // no files exist there → best-effort empty

	assert.Empty(t, r.Target.Snippet)
	assert.NotSame(t, shared, r.Target, "target must be copied before snippet mutation")
	assert.Empty(t, shared.Snippet, "index node must stay untouched")
}
