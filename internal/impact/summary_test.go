package impact_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

func TestSummarize_GroupsCallersByFile(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false)
	s := out.Summarize()

	assert.True(t, s.Summary)
	assert.Equal(t, "be:queryDB", s.Target.ID)
	assert.Equal(t, 2, s.TotalCallers)

	require.Len(t, s.Files, 2)
	// Sorted by min depth: handler.go (depth 1) before api.js (depth 2).
	assert.Equal(t, "handler.go", s.Files[0].File)
	assert.Equal(t, 1, s.Files[0].MinDepth)
	assert.Equal(t, []string{"calls"}, s.Files[0].EdgeTypes)
	assert.Equal(t, "api.js", s.Files[1].File)
	assert.Equal(t, 2, s.Files[1].MinDepth)
	assert.Equal(t, []string{"http_call"}, s.Files[1].EdgeTypes)

	// Entry points compact to strings.
	require.Len(t, s.EntryPoints, 1)
	assert.Equal(t, "fetchUser — api.js:10", s.EntryPoints[0])
	assert.ElementsMatch(t, []string{"frontend", "backend"}, s.ServicesAffected)
}

func TestApplyBudget_DetailWhenItFits(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false)
	out.AttachUnresolved(nil)

	res := out.ApplyBudget(100000, false)

	detail, ok := res.(*impact.Result)
	require.True(t, ok, "generous budget must keep per-node detail")
	require.NotNil(t, detail.Budget)
	assert.Equal(t, budget.LevelDetail, detail.Budget.Level)
}

func TestApplyBudget_SummaryWhenOverBudgetKeepsUnresolvedWhole(t *testing.T) {
	idx := fixtureIndex()
	// Fan in callers from many files so per-node detail blows a small budget
	// and the trimmed rollup still cannot keep every file.
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("be:caller%d", i)
		idx.AddNode(&graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: "caller", Service: "backend", File: fmt.Sprintf("pkg/caller_%02d.go", i), Line: 1})
		idx.AddEdge(&graph.Edge{ID: "fan" + id, From: id, To: "be:queryDB", Type: graph.EdgeTypeCalls})
	}
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false)
	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "db.go", Line: 41, Name: "dyn", Kind: "call_ref"},
	})

	res := out.ApplyBudget(200, false)

	s, ok := res.(*impact.Summary)
	require.True(t, ok, "tight budget must roll up to summary")
	assert.Equal(t, budget.LevelSummary, s.Budget.Level)
	assert.Positive(t, s.Budget.OmittedFiles)
	assert.NotEmpty(t, s.Files)
	// The recall gauge is never trimmed to save tokens.
	require.Len(t, s.Unresolved, 1)
}

func TestApplyBudget_ForceSummary(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false)
	out.AttachUnresolved(nil)

	s, ok := out.ApplyBudget(0, true).(*impact.Summary)
	require.True(t, ok)
	assert.Empty(t, s.Budget.Note, "explicitly requested summary carries no over-budget note")
}

func TestInlineSnippets_CopiesSharedNodes(t *testing.T) {
	idx := fixtureIndex()
	sharedTarget := idx.Nodes["be:queryDB"]
	sharedEntry := idx.Nodes["fe:fetchUser"]
	out := impact.Build(idx, sharedTarget, 10, "", false)

	out.InlineSnippets(t.TempDir(), 3) // no files there → best-effort empty

	assert.NotSame(t, sharedTarget, out.Target, "target must be copied before snippet mutation")
	require.Len(t, out.EntryPoints, 1)
	assert.NotSame(t, sharedEntry, out.EntryPoints[0], "entry points must be copied too")
	assert.Empty(t, sharedTarget.Snippet)
	assert.Empty(t, sharedEntry.Snippet)
}

func TestBuildFile_ApplyBudgetTrimsImpactedList(t *testing.T) {
	idx := fixtureIndex()
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("be:caller%d", i)
		idx.AddNode(&graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: "caller", Service: "backend", File: fmt.Sprintf("pkg/caller_%02d.go", i), Line: 1})
		idx.AddEdge(&graph.Edge{ID: "fan" + id, From: id, To: "be:queryDB", Type: graph.EdgeTypeCalls})
	}

	out, err := impact.BuildFile(idx, "", "db.go", "backward", 10)
	require.NoError(t, err)
	out.AttachUnresolved(nil)
	require.Greater(t, len(out.Impacted), 5)
	full := len(out.Impacted)

	out.ApplyBudget(150)

	assert.Less(t, len(out.Impacted), full, "tiny budget must trim the impacted list")
	assert.NotEmpty(t, out.Impacted)
	require.NotNil(t, out.Budget)
	assert.Equal(t, full-len(out.Impacted), out.Budget.OmittedFiles)
}

func TestBuildFile_UnbudgetedIsUntouched(t *testing.T) {
	idx := fixtureIndex()
	out, err := impact.BuildFile(idx, "", "db.go", "backward", 10)
	require.NoError(t, err)
	out.AttachUnresolved(nil)

	out.ApplyBudget(0)

	assert.Nil(t, out.Budget, "no budget set → no budget section")
	require.Len(t, out.Impacted, 2)
}

func TestBuildFile_MissingFileErrors(t *testing.T) {
	idx := fixtureIndex()
	_, err := impact.BuildFile(idx, "", "nope.go", "backward", 10)
	assert.Error(t, err)
}
