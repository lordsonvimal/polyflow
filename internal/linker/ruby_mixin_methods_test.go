package linker

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mixinClass builds a class/module node with a body spanning [line, endLine].
func mixinClass(svc, file, label string, line, endLine int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":class:" + label,
		Type:     graph.NodeTypeClass,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta:     map[string]string{"end_line": itoa(endLine)},
	}
}

// mixinMethod builds a method node owned by class `owner`.
func mixinMethod(svc, file, owner, name string, line, endLine int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":function:" + name + ":" + itoa(line),
		Type:     graph.NodeTypeFunction,
		Label:    name,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta: map[string]string{
			"class":          owner,
			"end_line":       itoa(endLine),
			"qualified_name": owner + "#" + name,
		},
	}
}

func inheritsEdge(from, to, via string) graph.Edge {
	return graph.Edge{
		ID:    "inherits:" + from + "->" + to,
		From:  from,
		To:    to,
		Type:  graph.EdgeTypeInherits,
		Meta:  map[string]string{"via": via},
		Label: via,
	}
}

func callRef(svc, file string, line int, name string) graph.UnresolvedRef {
	return graph.UnresolvedRef{Service: svc, File: file, Line: line, Name: name, Kind: "call_ref"}
}


func edgeTargets(edges []graph.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.From+" -> "+e.To)
	}
	sort.Strings(out)
	return out
}

// dxFixture is the shape this pass exists for: a module in lib/dx.rb defining
// logger_context, and a controller in another file that mixes it in and calls it
// from an action.
func dxFixture(svc string) (nodes []graph.Node, edges []graph.Edge) {
	dxFile := "/repo/lib/dx.rb"
	ctrlFile := "/repo/app/controllers/files_controller.rb"

	dx := mixinClass(svc, dxFile, "Dx", 5, 80)
	loggerContext := mixinMethod(svc, dxFile, "Dx", "logger_context", 31, 33)
	leanBacktrace := mixinMethod(svc, dxFile, "Dx", "lean_backtrace", 49, 59)

	ctrl := mixinClass(svc, ctrlFile, "FilesController", 3, 60)
	show := mixinMethod(svc, ctrlFile, "FilesController", "show", 10, 20)

	return []graph.Node{dx, loggerContext, leanBacktrace, ctrl, show},
		[]graph.Edge{inheritsEdge(ctrl.ID, dx.ID, "mixin")}
}

// TestMixinMethods_ResolvesThroughInclude is the base case: 2210 of the fleet's
// 7941 call_ref entries are exactly this.
func TestMixinMethods_ResolvesThroughInclude(t *testing.T) {
	t.Parallel()
	nodes, edges := dxFixture("orion")
	refs := []graph.UnresolvedRef{
		callRef("orion", "/repo/app/controllers/files_controller.rb", 12, "logger_context"),
	}

	got, resolved, ledger, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 1)
	assert.Equal(t, []string{
		"orion:/repo/app/controllers/files_controller.rb:function:show:10 -> " +
			"orion:/repo/lib/dx.rb:function:logger_context:31",
	}, edgeTargets(got))
	assert.Equal(t, graph.EdgeTypeCalls, got[0].Type)
	assert.Equal(t, graph.ConfidenceInferred, got[0].Confidence)
	assert.Equal(t, "mixin_method", got[0].Meta["via"])
	assert.Equal(t, "1", got[0].Meta["depth"])
	assert.Empty(t, ledger)

	// The ledger entry must be suppressed, or the fix is invisible to the agent
	// reading the "verify these N references" footer.
	assert.True(t, resolved[RubyCallRefKey("/repo/app/controllers/files_controller.rb", 12, "logger_context")])
}

// TestMixinMethods_ViewResolvesThroughHelperModule is DC.12: a `.erb` view has
// no enclosing class/inherits edge for ix.lookup's ancestor walk to use, but
// Rails auto-includes every app/helpers/*.rb module into every view — a flat
// name lookup against helperMethods, not the ancestor walk.
func TestMixinMethods_ViewResolvesThroughHelperModule(t *testing.T) {
	t.Parallel()
	svc := "orion"
	helperFile := "app/helpers/task_lists_helper.rb"
	viewFile := "app/views/task_lists/_filters.html.erb"

	helperMod := mixinClass(svc, helperFile, "TaskListsHelper", 1, 20)
	uniqueNames := mixinMethod(svc, helperFile, "TaskListsHelper", "unique_names", 5, 8)

	nodes := []graph.Node{helperMod, uniqueNames}
	refs := []graph.UnresolvedRef{
		callRef(svc, viewFile, 12, "unique_names"),
	}

	got, resolved, ledger, _ := LinkRubyMixinMethods(nodes, nil, refs)
	require.Len(t, got, 1)
	assert.Equal(t, []string{
		svc + ":" + viewFile + ":" + string(graph.NodeTypeFile) + " -> " + uniqueNames.ID,
	}, edgeTargets(got))
	assert.Equal(t, graph.EdgeTypeCalls, got[0].Type)
	assert.Equal(t, "view_helper", got[0].Meta["via"])
	assert.Empty(t, ledger)
	assert.True(t, resolved[RubyCallRefKey(viewFile, 12, "unique_names")])
}

// TestMixinMethods_ViewHelperUnrelatedNameStaysUnresolved pins the negative:
// a name no helper module in the service declares must not resolve to
// anything (e.g. a framework/ActionView builtin the parser already excludes
// from the ledger, or a genuine typo).
func TestMixinMethods_ViewHelperUnrelatedNameStaysUnresolved(t *testing.T) {
	t.Parallel()
	svc := "orion"
	helperFile := "app/helpers/task_lists_helper.rb"
	viewFile := "app/views/task_lists/_filters.html.erb"

	nodes := []graph.Node{
		mixinClass(svc, helperFile, "TaskListsHelper", 1, 20),
		mixinMethod(svc, helperFile, "TaskListsHelper", "unique_names", 5, 8),
	}
	refs := []graph.UnresolvedRef{
		callRef(svc, viewFile, 12, "some_undefined_helper"),
	}

	got, _, _, _ := LinkRubyMixinMethods(nodes, nil, refs)
	assert.Empty(t, got)
}

// TestMixinMethods_NeverCrossesAServiceBoundary is the vendored-copy guard, and
// the acceptance criterion C.4 names explicitly. Four repos in the fleet each
// ship a lib/dx.rb defining logger_context; a name-keyed resolver would bind
// orion's 2210 call sites to all four.
func TestMixinMethods_NeverCrossesAServiceBoundary(t *testing.T) {
	t.Parallel()
	aNodes, aEdges := dxFixture("orion")
	bNodes, bEdges := dxFixture("orion-vega")
	// Same file paths would collide in the index; give svc-b its own tree.
	for i := range bNodes {
		bNodes[i].File = "/other" + bNodes[i].File
		bNodes[i].ID = "orion-vega:" + bNodes[i].File + ":" +
			string(bNodes[i].Type) + ":" + bNodes[i].Label
	}
	bEdges = []graph.Edge{inheritsEdge(bNodes[3].ID, bNodes[0].ID, "mixin")}

	nodes := append(aNodes, bNodes...)
	edges := append(aEdges, bEdges...)
	refs := []graph.UnresolvedRef{
		callRef("orion", "/repo/app/controllers/files_controller.rb", 12, "logger_context"),
	}

	got, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 1, "a call site must bind to exactly its own service's copy")

	svcOf := map[string]string{}
	for _, n := range nodes {
		svcOf[n.ID] = n.Service
	}
	for _, e := range got {
		assert.Equal(t, svcOf[e.From], svcOf[e.To],
			"cross-service calls edge: %s -> %s", e.From, e.To)
	}
}

// TestMixinMethods_ResolvesThroughTheSuperclassChain. A controller three levels
// down from the one that writes `include Dx` still calls the method; that is
// where most of orion's call sites sit.
func TestMixinMethods_ResolvesThroughTheSuperclassChain(t *testing.T) {
	t.Parallel()
	svc := "orion"
	dx := mixinClass(svc, "/repo/lib/dx.rb", "Dx", 5, 80)
	logger := mixinMethod(svc, "/repo/lib/dx.rb", "Dx", "logger_context", 31, 33)
	base := mixinClass(svc, "/repo/app/controllers/application_controller.rb", "ApplicationController", 1, 40)
	mid := mixinClass(svc, "/repo/app/controllers/secured_controller.rb", "SecuredController", 1, 30)
	leaf := mixinClass(svc, "/repo/app/controllers/studies_controller.rb", "StudiesController", 1, 50)
	index := mixinMethod(svc, "/repo/app/controllers/studies_controller.rb", "StudiesController", "index", 5, 15)

	nodes := []graph.Node{dx, logger, base, mid, leaf, index}
	edges := []graph.Edge{
		inheritsEdge(base.ID, dx.ID, "mixin"),
		inheritsEdge(mid.ID, base.ID, "superclass"),
		inheritsEdge(leaf.ID, mid.ID, "superclass"),
	}
	refs := []graph.UnresolvedRef{
		callRef(svc, "/repo/app/controllers/studies_controller.rb", 7, "logger_context"),
	}

	got, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 1)
	assert.Equal(t, logger.ID, got[0].To)
	assert.Equal(t, "3", got[0].Meta["depth"])
}

// TestMixinMethods_NearestDefinitionWins. A concern that overrides a name the
// grandparent also defines is what actually runs, and depth is enough to say so
// without needing the include order.
func TestMixinMethods_NearestDefinitionWins(t *testing.T) {
	t.Parallel()
	svc := "orion"
	far := mixinClass(svc, "/repo/lib/dx.rb", "Dx", 1, 50)
	farM := mixinMethod(svc, "/repo/lib/dx.rb", "Dx", "log_it", 10, 12)
	near := mixinClass(svc, "/repo/app/controllers/concerns/logging.rb", "Logging", 1, 20)
	nearM := mixinMethod(svc, "/repo/app/controllers/concerns/logging.rb", "Logging", "log_it", 5, 7)
	ctrl := mixinClass(svc, "/repo/app/controllers/x_controller.rb", "XController", 1, 30)
	act := mixinMethod(svc, "/repo/app/controllers/x_controller.rb", "XController", "show", 4, 10)

	nodes := []graph.Node{far, farM, near, nearM, ctrl, act}
	edges := []graph.Edge{
		inheritsEdge(ctrl.ID, near.ID, "mixin"),
		inheritsEdge(near.ID, far.ID, "mixin"),
	}
	refs := []graph.UnresolvedRef{callRef(svc, "/repo/app/controllers/x_controller.rb", 6, "log_it")}

	got, _, ledger, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 1)
	assert.Equal(t, nearM.ID, got[0].To)
	assert.Empty(t, ledger, "one winner is not a collision")
}

// TestMixinMethods_TiedDefinitionsFanOut. Two mixins at the same depth both
// define the name. `inherits` edges carry no source order, so Ruby's
// last-include-wins rule is unavailable — emit both and say so, rather than pick.
func TestMixinMethods_TiedDefinitionsFanOut(t *testing.T) {
	t.Parallel()
	svc := "orion"
	a := mixinClass(svc, "/repo/lib/a.rb", "A", 1, 20)
	aM := mixinMethod(svc, "/repo/lib/a.rb", "A", "log_it", 5, 7)
	b := mixinClass(svc, "/repo/lib/b.rb", "B", 1, 20)
	bM := mixinMethod(svc, "/repo/lib/b.rb", "B", "log_it", 5, 7)
	ctrl := mixinClass(svc, "/repo/app/x.rb", "X", 1, 30)
	act := mixinMethod(svc, "/repo/app/x.rb", "X", "show", 4, 10)

	nodes := []graph.Node{a, aM, b, bM, ctrl, act}
	edges := []graph.Edge{
		inheritsEdge(ctrl.ID, a.ID, "mixin"),
		inheritsEdge(ctrl.ID, b.ID, "mixin"),
	}
	refs := []graph.UnresolvedRef{callRef(svc, "/repo/app/x.rb", 6, "log_it")}

	got, _, ledger, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 2)
	for _, e := range got {
		assert.Equal(t, "true", e.Meta["ambiguous"])
	}
	require.Len(t, ledger, 1)
	assert.Equal(t, "mixin_method_collision", ledger[0].Kind)
}

// TestMixinMethods_UnrelatedClassStaysUnresolved. A class that does not mix the
// module in must keep its blind spot. This is the whole reason resolution walks
// edges instead of looking a name up.
func TestMixinMethods_UnrelatedClassStaysUnresolved(t *testing.T) {
	t.Parallel()
	nodes, edges := dxFixture("orion")
	other := mixinClass("orion", "/repo/app/models/thing.rb", "Thing", 1, 30)
	otherM := mixinMethod("orion", "/repo/app/models/thing.rb", "Thing", "save!", 4, 10)
	nodes = append(nodes, other, otherM)

	refs := []graph.UnresolvedRef{callRef("orion", "/repo/app/models/thing.rb", 6, "logger_context")}
	got, resolved, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	assert.Empty(t, got)
	assert.Empty(t, resolved)
}

// TestMixinMethods_ClassBodyCallIsAttributedToTheClass. `include`d DSL runs at
// load time, outside any method, and the class is where it runs.
func TestMixinMethods_ClassBodyCallIsAttributedToTheClass(t *testing.T) {
	t.Parallel()
	nodes, edges := dxFixture("orion")
	// Line 5 is inside FilesController (3..60) but before `show` (10..20).
	refs := []graph.UnresolvedRef{
		callRef("orion", "/repo/app/controllers/files_controller.rb", 5, "logger_context"),
	}
	got, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	require.Len(t, got, 1)
	assert.Equal(t, "orion:/repo/app/controllers/files_controller.rb:class:FilesController", got[0].From)
}

// TestMixinMethods_CyclicAncestorsTerminate. A reconstructed chain is not
// guaranteed acyclic — two concerns that include each other are legal Ruby.
func TestMixinMethods_CyclicAncestorsTerminate(t *testing.T) {
	t.Parallel()
	svc := "orion"
	a := mixinClass(svc, "/repo/lib/a.rb", "A", 1, 20)
	b := mixinClass(svc, "/repo/lib/b.rb", "B", 1, 20)
	ctrl := mixinClass(svc, "/repo/app/x.rb", "X", 1, 30)
	act := mixinMethod(svc, "/repo/app/x.rb", "X", "show", 4, 10)

	nodes := []graph.Node{a, b, ctrl, act}
	edges := []graph.Edge{
		inheritsEdge(ctrl.ID, a.ID, "mixin"),
		inheritsEdge(a.ID, b.ID, "mixin"),
		inheritsEdge(b.ID, a.ID, "mixin"),
	}
	refs := []graph.UnresolvedRef{callRef(svc, "/repo/app/x.rb", 6, "nope")}

	got, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	assert.Empty(t, got)
}

// TestMixinMethods_Deterministic. Rule 2: the same input must produce the same
// edge list, including when the ancestor set comes out of a map.
func TestMixinMethods_Deterministic(t *testing.T) {
	t.Parallel()
	nodes, edges := dxFixture("orion")
	refs := []graph.UnresolvedRef{
		callRef("orion", "/repo/app/controllers/files_controller.rb", 14, "lean_backtrace"),
		callRef("orion", "/repo/app/controllers/files_controller.rb", 12, "logger_context"),
	}

	first, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
	for i := 0; i < 20; i++ {
		got, _, _, _ := LinkRubyMixinMethods(nodes, edges, refs)
		require.Equal(t, edgeIDs(first), edgeIDs(got))
	}
}

func edgeIDs(edges []graph.Edge) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.ID
	}
	return out
}
