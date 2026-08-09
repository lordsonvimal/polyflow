package impact_test

// Tests for D.3 blast-radius shape: depth honesty, the local-variable drop,
// directionality, and the default token budget. The theme is that a blast
// radius is a claim about causation, and each of these is a way the output
// used to overstate or misstate that claim.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// shapeIndex builds a graph with every feature the shape rules act on:
//
//	svc --contains--> file:svc.go --contains--> handler --calls--> mid --calls--> leaf
//	closure --captures--> local   (scope=captured — a local)
//	closure --captures--> shared  (scope=package — shared state)
//	leaf    --calls--> closure
//	other   --instantiates--> Thing --contains--> leaf   (the Ruby-shaped path)
func shapeIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	add := func(id string, t graph.NodeType, file string, meta map[string]string) {
		idx.AddNode(&graph.Node{ID: id, Type: t, Label: id, Service: "svc", File: file, Meta: meta})
	}
	add("svc", graph.NodeTypeService, "", nil)
	add("file:svc.go", graph.NodeTypeFile, "svc.go", nil)
	add("handler", graph.NodeTypeHTTPHandler, "svc.go", nil)
	add("mid", graph.NodeTypeFunction, "mid.go", nil)
	add("leaf", graph.NodeTypeFunction, "leaf.go", nil)
	add("closure", graph.NodeTypeFunction, "closure.go", nil)
	add("local", graph.NodeTypeVariable, "closure.go", map[string]string{"scope": "captured"})
	add("shared", graph.NodeTypeVariable, "closure.go", map[string]string{"scope": "package"})
	add("Thing", graph.NodeTypeStruct, "thing.go", nil)
	add("other", graph.NodeTypeFunction, "other.go", nil)

	e := func(id, from, to string, t graph.EdgeType) {
		idx.AddEdge(&graph.Edge{ID: id, From: from, To: to, Type: t})
	}
	e("c1", "svc", "file:svc.go", graph.EdgeTypeContains)
	e("c2", "file:svc.go", "handler", graph.EdgeTypeContains)
	e("k1", "handler", "mid", graph.EdgeTypeCalls)
	e("k2", "mid", "leaf", graph.EdgeTypeCalls)
	e("k3", "leaf", "closure", graph.EdgeTypeCalls)
	e("p1", "closure", "local", graph.EdgeTypeCaptures)
	e("p2", "closure", "shared", graph.EdgeTypeCaptures)
	e("c3", "Thing", "leaf", graph.EdgeTypeContains)
	e("i1", "other", "Thing", graph.EdgeTypeInstantiates)
	return idx
}

func labels(cs []impact.Caller) map[string]int {
	m := make(map[string]int, len(cs))
	for _, c := range cs {
		m[c.Label] = c.Depth
	}
	return m
}

// TestDepthIsHopCount is the plan's "assert that depth-N results are genuinely
// N hops". BFS reports the SHORTEST path, so the number is only meaningful if
// it matches the hop count on that path — and a depth cap must cut exactly
// there, not one hop early or late.
func TestDepthIsHopCount(t *testing.T) {
	idx := shapeIndex()
	root := idx.Nodes["closure"]

	got := labels(impact.Build(idx, root, impact.Options{Depth: 10}).Callers)
	assert.Equal(t, 1, got["leaf"], "leaf calls closure directly")
	assert.Equal(t, 2, got["mid"])
	assert.Equal(t, 3, got["handler"])
	assert.Equal(t, 4, got["file:svc.go"], "the containing file is one hop past the declaration")
	assert.Equal(t, 5, got["svc"])

	// A depth cap admits every node at or below it and nothing above — and
	// each node keeps the same hop number it had in the uncapped walk, so a
	// capped answer is a prefix of the full one rather than a reshuffle.
	for depth := 1; depth <= 5; depth++ {
		capped := labels(impact.Build(idx, root, impact.Options{Depth: depth}).Callers)
		want := 0
		for label, d := range got {
			if d > depth {
				assert.NotContains(t, capped, label, "depth=%d admitted a hop-%d node", depth, d)
				continue
			}
			want++
			assert.Equal(t, d, capped[label], "depth=%d moved %s", depth, label)
		}
		assert.Len(t, capped, want)
	}
}

// TestDropLocals_KeepsSharedState is the plan's captures rule. The
// discriminator is the variable's own scope, not the edge type: `captures`
// reaches both a throwaway local and package-level shared state, and only the
// first is a useless answer.
func TestDropLocals_KeepsSharedState(t *testing.T) {
	idx := shapeIndex()
	root := idx.Nodes["closure"]
	opts := impact.Options{Depth: 10, Direction: "forward", Policy: graph.BlastRadiusPolicy()}

	got := labels(impact.Build(idx, root, opts).Callers)
	assert.NotContains(t, got, "local", "a closure-captured local is a correct edge and a useless answer")
	assert.Contains(t, got, "shared", "package-level state is genuinely impacted")

	// Opting back in restores it — the edge is not deleted, only unasked-for.
	opts.Policy.DropLocals = false
	assert.Contains(t, labels(impact.Build(idx, root, opts).Callers), "local")
}

// TestDirection_AnswersOppositeQuestions: "what breaks if I change this" and
// "what do I need to read" are opposite walks, and the output must say which
// one it did. Reporting a callee as a caller inverts the causal claim.
func TestDirection_AnswersOppositeQuestions(t *testing.T) {
	idx := shapeIndex()
	root := idx.Nodes["leaf"]

	back := impact.Build(idx, root, impact.Options{Depth: 10, Direction: "backward"})
	assert.Equal(t, "backward", back.Direction)
	assert.Contains(t, labels(back.Callers), "mid", "mid calls leaf")
	assert.NotContains(t, labels(back.Callers), "closure")

	fwd := impact.Build(idx, root, impact.Options{Depth: 10, Direction: "forward"})
	assert.Equal(t, "forward", fwd.Direction)
	assert.Contains(t, labels(fwd.Callers), "closure", "leaf calls closure")
	assert.NotContains(t, labels(fwd.Callers), "mid")

	both := impact.Build(idx, root, impact.Options{Depth: 10, Direction: "both"})
	assert.Equal(t, "both", both.Direction)
	bl := labels(both.Callers)
	assert.Contains(t, bl, "mid")
	assert.Contains(t, bl, "closure")

	// Default is backward, and it is stated rather than left blank: an absent
	// direction reads as "backward" to some consumers and "both" to others.
	assert.Equal(t, "backward", impact.Build(idx, root, impact.Options{Depth: 10}).Direction)
}

// TestDirectionBoth_DoesNotDoubleCount — a node reachable both ways appears
// once, keeping its backward depth.
func TestDirectionBoth_DoesNotDoubleCount(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	for _, id := range []string{"a", "b"} {
		idx.AddNode(&graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: id, File: id + ".go"})
	}
	idx.AddEdge(&graph.Edge{ID: "f", From: "a", To: "b", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "r", From: "b", To: "a", Type: graph.EdgeTypeCalls})

	out := impact.Build(idx, idx.Nodes["a"], impact.Options{Depth: 10, Direction: "both"})
	assert.Len(t, out.Callers, 1, "b is reachable both ways but is one node")
	assert.Equal(t, 1, out.TotalCallers)
}

// TestContainmentTerminal_IsOptIn pins the recall/precision trade-off measured
// in D.3. The default expands containment because in lobsters the ONLY path
// from a Ruby method to its verified caller runs
// `method ←contains— Thing ←instantiates— other`; suppressing it drops that
// repo's recall 0.944→0.833. --stop-at-containers is the tighter shape.
func TestContainmentTerminal_IsOptIn(t *testing.T) {
	idx := shapeIndex()
	root := idx.Nodes["leaf"]

	def := labels(impact.Build(idx, root, impact.Options{
		Depth: 10, Policy: graph.BlastRadiusPolicy()}).Callers)
	require.Contains(t, def, "Thing")
	assert.Contains(t, def, "other",
		"the default must keep the containment path — it is load-bearing where call resolution is weak")

	tight := labels(impact.Build(idx, root, impact.Options{
		Depth: 10, Policy: graph.ContainmentTerminal()}).Callers)
	assert.Contains(t, tight, "Thing", "the container itself is useful context and is still reported")
	assert.NotContains(t, tight, "other", "but the walk must not continue out of it")
}

// TestBuildDiff_HonoursPolicy — the diff blast radius is the union of per-seed
// ancestor walks, so it needs the same shaping. Without it, `impact --diff`
// and `impact --target` on the same changed function disagree about what the
// blast radius is.
func TestBuildDiff_HonoursPolicy(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "fn", Type: graph.NodeTypeFunction, Label: "fn",
		Service: "svc", File: "handler.go", Line: 10, Meta: map[string]string{"end_line": "20"}})
	// A callback held in a closure-captured local: a real edge into fn, and a
	// useless answer to "what breaks if I change fn".
	idx.AddNode(&graph.Node{ID: "cb", Type: graph.NodeTypeVariable, Label: "cb",
		Service: "svc", File: "wire.go", Line: 4, Meta: map[string]string{"scope": "captured"}})
	idx.AddNode(&graph.Node{ID: "caller", Type: graph.NodeTypeFunction, Label: "caller",
		Service: "svc", File: "caller.go", Line: 7})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "cb", To: "fn", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "caller", To: "fn", Type: graph.EdgeTypeCalls})

	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 12, End: 12}}},
	}

	got := labels(impact.BuildDiff(idx, changes, impact.Options{
		Depth: 10, Policy: graph.BlastRadiusPolicy()}).Callers)
	assert.Contains(t, got, "caller")
	assert.NotContains(t, got, "cb")

	// The zero Options stay the raw walk, so nothing that did not ask for the
	// shape silently changes answers.
	assert.Contains(t, labels(impact.BuildDiff(idx, changes, impact.Options{Depth: 10}).Callers), "cb")
}

// impactedFiles indexes a file-granularity answer by path.
func impactedFiles(es []graph.FileImpactEntry) map[string]bool {
	m := make(map[string]bool, len(es))
	for _, e := range es {
		m[e.File] = true
	}
	return m
}

// TestBuildFile_HonoursPolicy — `impact --file` is a blast radius too, and the
// rollup hides which nodes were counted, so a file reached ONLY through a
// closure-captured local is indistinguishable from one reached by a call
// unless the shape rule is applied to this path as well.
func TestBuildFile_HonoursPolicy(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	add := func(id string, ty graph.NodeType, file string, meta map[string]string) {
		idx.AddNode(&graph.Node{ID: id, Type: ty, Label: id, Service: "svc", File: file, Meta: meta})
	}
	add("producer", graph.NodeTypeFunction, "seed.go", nil)
	add("local", graph.NodeTypeVariable, "local.go", map[string]string{"scope": "captured"})
	add("shared", graph.NodeTypeVariable, "shared.go", map[string]string{"scope": "package"})
	idx.AddEdge(&graph.Edge{ID: "p1", From: "producer", To: "local", Type: graph.EdgeTypeCaptures})
	idx.AddEdge(&graph.Edge{ID: "p2", From: "producer", To: "shared", Type: graph.EdgeTypeCaptures})

	opts := impact.Options{Depth: 10, Direction: "forward", Policy: graph.BlastRadiusPolicy()}
	out, err := impact.BuildFile(idx, "seed.go", opts)
	require.NoError(t, err)
	got := impactedFiles(out.Impacted)
	assert.NotContains(t, got, "local.go")
	assert.Contains(t, got, "shared.go")

	opts.Policy.DropLocals = false
	out, err = impact.BuildFile(idx, "seed.go", opts)
	require.NoError(t, err)
	assert.Contains(t, impactedFiles(out.Impacted), "local.go")
}

// TestBuildFile_DirectionDefaultsToBackward — the file path used to depend on
// each caller normalising "" itself, and graph.FileImpact reads an unknown
// direction as FORWARD. An unset direction must not silently invert the
// question.
func TestBuildFile_DirectionDefaultsToBackward(t *testing.T) {
	idx := shapeIndex()
	out, err := impact.BuildFile(idx, "leaf.go", impact.Options{Depth: 10})
	require.NoError(t, err)
	assert.Equal(t, "backward", out.Direction)
	got := impactedFiles(out.Impacted)
	assert.Contains(t, got, "mid.go", "mid calls leaf — backward")
	assert.NotContains(t, got, "closure.go", "leaf calls closure — that is forward")
}
