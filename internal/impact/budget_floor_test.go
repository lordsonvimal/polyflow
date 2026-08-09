package impact_test

// D.3 turned the token budget on by default. These tests pin the two ways
// that change nearly shipped an answer with no answer in it: the unresolved
// caveat outbidding the file list for the budget, and a single node's meta
// blob eating a third of it.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// wideIndex builds a blast radius of n callers, each in its own file.
func wideIndex(n int) *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "root", Type: graph.NodeTypeFunction, Label: "root", Service: "svc", File: "root.go"})
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		idx.AddNode(&graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: id, Service: "svc",
			File: fmt.Sprintf("pkg/caller_%02d.go", i)})
		idx.AddEdge(&graph.Edge{ID: "e" + id, From: id, To: "root", Type: graph.EdgeTypeCalls})
	}
	return idx
}

func manyRefs(n int) []graph.UnresolvedRef {
	refs := make([]graph.UnresolvedRef, 0, n)
	for i := range n {
		refs = append(refs, graph.UnresolvedRef{
			Service: "svc", File: "root.go", Line: i + 1,
			Name: fmt.Sprintf("some_unresolved_reference_%02d", i), Kind: "call_ref",
		})
	}
	return refs
}

// TestBudget_CaveatDoesNotEvictTheAnswer is the measured D.3 bug. The
// unresolved list was exempt from budgeting so that blind spots could never be
// hidden to save tokens — sound while a budget was opt-in, but with a default
// budget it inverted: on fleet-datascience `impact --target JobMessage` came
// back with 1 of 25 files because 6 KB of unresolved refs had first claim.
func TestBudget_CaveatDoesNotEvictTheAnswer(t *testing.T) {
	out := impact.Build(wideIndex(25), wideIndex(25).Nodes["root"], impact.Options{Depth: 10})
	out.AttachUnresolved(manyRefs(120))
	require.Len(t, out.Unresolved, 120)

	s, ok := out.ApplyBudget(impact.DefaultBudget, false).(*impact.Summary)
	require.True(t, ok, "a 25-file radius with 120 refs must exceed the default budget")

	assert.Greater(t, len(s.Files), 15,
		"the file list is the answer and must not be squeezed out by the caveat; got %d of 25", len(s.Files))
	assert.Less(t, len(s.Unresolved), 120, "the caveat detail yields first")

	// The blind-spot SIGNAL survives even when its detail does not: an agent
	// must never read a trimmed list as a complete one.
	assert.Contains(t, s.UnresolvedNote, "120",
		"the note must still report the true total, not the trimmed length")
	assert.Contains(t, s.Budget.Note, "unresolved references omitted")
}

// TestBudget_NegativeIsUnlimited — the default is a number now, so there has
// to be a way back to the full answer.
func TestBudget_NegativeIsUnlimited(t *testing.T) {
	idx := wideIndex(25)
	out := impact.Build(idx, idx.Nodes["root"], impact.Options{Depth: 10})
	out.AttachUnresolved(manyRefs(120))

	got := out.ApplyBudget(-1, false)
	r, ok := got.(*impact.Result)
	require.True(t, ok, "negative max-tokens must return full per-node detail")
	assert.Len(t, r.Callers, 25)
	assert.Len(t, r.Unresolved, 120, "nothing is trimmed when unlimited")
}

// TestBudget_SmallResultKeepsFullDetail — the budget picks a shape, it is not
// a tax. A blast radius that fits must come back whole.
func TestBudget_SmallResultKeepsFullDetail(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	got := out.ApplyBudget(impact.DefaultBudget, false)

	r, ok := got.(*impact.Result)
	require.True(t, ok, "a two-node radius fits the default budget")
	assert.Len(t, r.Callers, 2)
}

// TestSummarize_DropsOversizedMeta — a 27-field Go struct carries a 2.4 KB
// serialised `fields` meta. In the rollup shape that is 30% of the default
// budget spent describing the node the caller just named.
func TestSummarize_DropsOversizedMeta(t *testing.T) {
	idx := fixtureIndex()
	target := idx.Nodes["be:queryDB"]
	target.Meta = map[string]string{
		"fields":   strings.Repeat("x", 3000),
		"end_line": "97",
	}

	s := impact.Build(idx, target, impact.Options{Depth: 10}).Summarize()

	assert.NotContains(t, s.Target.Meta, "fields")
	assert.Equal(t, "97", s.Target.Meta["end_line"], "small metas are what make the target identifiable")
	assert.Equal(t, "fields", s.Target.Meta["meta_omitted"], "the drop must be declared, not silent")

	// The node aliases the shared adjacency index; mutating in place would
	// corrupt every later query in a long-lived process (the MCP server).
	assert.Len(t, idx.Nodes["be:queryDB"].Meta["fields"], 3000, "the index must be untouched")
}
