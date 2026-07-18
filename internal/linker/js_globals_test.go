package linker

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkJSGlobals_BasicResolution: call_ref for "save" resolves to window.save
// defined in another file via global symbol table.
func TestLinkJSGlobals_BasicResolution(t *testing.T) {
	libFile := "/svc/lib.js"
	mainFile := "/svc/main.js"

	// Function node stamped with global_symbol (simulates stampGlobalSymbols output).
	saveFn := graph.Node{
		ID:      "svc:" + libFile + ":function:save:3",
		Type:    graph.NodeTypeFunction,
		Label:   "save",
		Service: "svc",
		File:    libFile,
		Line:    3,
		Meta:    map[string]string{"global_symbol": "save"},
	}
	callerFn := graph.Node{
		ID:      "svc:" + mainFile + ":function:caller:1",
		Type:    graph.NodeTypeFunction,
		Label:   "caller",
		Service: "svc",
		File:    mainFile,
		Line:    1,
		Meta:    map[string]string{"end_line": "3"},
	}

	unresolved := []graph.UnresolvedRef{
		{Service: "svc", File: mainFile, Line: 2, Name: "save", Kind: "call_ref"},
	}
	svcFiles := map[string][]string{"svc": {libFile, mainFile}}

	edges, resolved, collisions := LinkJSGlobals(
		[]graph.Node{saveFn, callerFn}, unresolved, nil, svcFiles)

	require.Len(t, edges, 1, "one edge: caller → save")
	assert.Equal(t, callerFn.ID, edges[0].From)
	assert.Equal(t, saveFn.ID, edges[0].To)
	assert.Equal(t, graph.EdgeTypeCalls, edges[0].Type)
	assert.Equal(t, graph.ConfidenceInferred, edges[0].Confidence)
	assert.Equal(t, "global", edges[0].Meta["via"])

	assert.True(t, resolved[mainFile+"\x00save"], "save@mainFile marked resolved")
	assert.Empty(t, collisions)
}

// TestLinkJSGlobals_Collision: same global name in two files →
// two candidate edges + one global_collision ledger entry.
func TestLinkJSGlobals_Collision(t *testing.T) {
	fileA := "/svc/a.js"
	fileB := "/svc/b.js"
	mainFile := "/svc/main.js"

	saveA := graph.Node{
		ID: "svc:" + fileA + ":function:save:1", Type: graph.NodeTypeFunction,
		Label: "save", Service: "svc", File: fileA, Line: 1,
		Meta: map[string]string{"global_symbol": "save"},
	}
	saveB := graph.Node{
		ID: "svc:" + fileB + ":function:save:1", Type: graph.NodeTypeFunction,
		Label: "save", Service: "svc", File: fileB, Line: 1,
		Meta: map[string]string{"global_symbol": "save"},
	}
	callerFn := graph.Node{
		ID: "svc:" + mainFile + ":function:caller:1", Type: graph.NodeTypeFunction,
		Label: "caller", Service: "svc", File: mainFile, Line: 1,
		Meta: map[string]string{"end_line": "3"},
	}

	unresolved := []graph.UnresolvedRef{
		{Service: "svc", File: mainFile, Line: 2, Name: "save", Kind: "call_ref"},
	}
	svcFiles := map[string][]string{"svc": {fileA, fileB, mainFile}}

	edges, resolved, collisions := LinkJSGlobals(
		[]graph.Node{saveA, saveB, callerFn}, unresolved, nil, svcFiles)

	require.Len(t, edges, 2, "fan-out: two candidate edges")
	assert.Equal(t, "global_ambiguous", edges[0].Meta["via"])
	assert.Equal(t, "global_ambiguous", edges[1].Meta["via"])

	toSet := map[string]bool{edges[0].To: true, edges[1].To: true}
	assert.True(t, toSet[saveA.ID])
	assert.True(t, toSet[saveB.ID])

	assert.True(t, resolved[mainFile+"\x00save"])

	require.Len(t, collisions, 1)
	assert.Equal(t, "global_collision", collisions[0].Kind)
	assert.Equal(t, "save", collisions[0].Name)
}

// TestLinkJSGlobals_InlineHandler: dom_target with handler="save()" resolves
// to a global function — the inline handler path.
func TestLinkJSGlobals_InlineHandler(t *testing.T) {
	libFile := "/svc/lib.js"
	htmlFile := "/svc/index.html"

	saveFn := graph.Node{
		ID: "svc:" + libFile + ":function:save:3", Type: graph.NodeTypeFunction,
		Label: "save", Service: "svc", File: libFile, Line: 3,
		Meta: map[string]string{"global_symbol": "save"},
	}
	// dom_target produced by patterns/html/events.yaml dom_event_attr.
	listener := graph.Node{
		ID: "svc:" + htmlFile + ":dom_target:dom_event_attr:5", Type: graph.NodeTypeDOMTarget,
		Label: "onclick handler", Service: "svc", File: htmlFile, Line: 5,
		Meta: map[string]string{"handler": "save()", "prop": "onclick", "pattern": "dom_event_attr"},
	}

	svcFiles := map[string][]string{"svc": {libFile, htmlFile}}

	edges, _, collisions := LinkJSGlobals([]graph.Node{saveFn, listener}, nil, nil, svcFiles)

	require.Len(t, edges, 1, "listener → save function")
	assert.Equal(t, listener.ID, edges[0].From)
	assert.Equal(t, saveFn.ID, edges[0].To)
	assert.Equal(t, graph.EdgeTypeCalls, edges[0].Type)
	assert.Equal(t, graph.ConfidenceInferred, edges[0].Confidence)
	assert.Equal(t, "global", edges[0].Meta["via"])
	assert.Empty(t, collisions)
}

// TestLinkJSGlobals_ImportsFirst: a name explained by an import is NOT
// resolved via globals even when a global with the same name exists.
func TestLinkJSGlobals_ImportsFirst(t *testing.T) {
	libFile := "/svc/lib.js"
	mainFile := "/svc/main.js"

	saveFn := graph.Node{
		ID: "svc:" + libFile + ":function:save:1", Type: graph.NodeTypeFunction,
		Label: "save", Service: "svc", File: libFile, Line: 1,
		Meta: map[string]string{"global_symbol": "save"},
	}
	callerFn := graph.Node{
		ID: "svc:" + mainFile + ":function:caller:1", Type: graph.NodeTypeFunction,
		Label: "caller", Service: "svc", File: mainFile, Line: 1,
		Meta: map[string]string{"end_line": "5"},
	}
	unresolved := []graph.UnresolvedRef{
		{Service: "svc", File: mainFile, Line: 2, Name: "save", Kind: "call_ref"},
	}
	importedNames := map[string]bool{mainFile + "\x00save": true}
	svcFiles := map[string][]string{"svc": {libFile, mainFile}}

	edges, resolved, _ := LinkJSGlobals(
		[]graph.Node{saveFn, callerFn}, unresolved, importedNames, svcFiles)

	assert.Empty(t, edges, "import-explained name must not get global fallback")
	assert.False(t, resolved[mainFile+"\x00save"])
}

// TestLinkJSGlobals_Determinism: two runs on identical input produce identical edge order.
func TestLinkJSGlobals_Determinism(t *testing.T) {
	fileA := "/svc/a.js"
	fileB := "/svc/b.js"
	mainFile := "/svc/main.js"

	makeNodes := func() []graph.Node {
		return []graph.Node{
			// Deliberately reversed order to verify sort stability.
			{ID: "svc:" + fileB + ":function:save:1", Type: graph.NodeTypeFunction,
				Label: "save", Service: "svc", File: fileB, Line: 1,
				Meta: map[string]string{"global_symbol": "save"}},
			{ID: "svc:" + fileA + ":function:save:1", Type: graph.NodeTypeFunction,
				Label: "save", Service: "svc", File: fileA, Line: 1,
				Meta: map[string]string{"global_symbol": "save"}},
			{ID: "svc:" + mainFile + ":function:caller:1", Type: graph.NodeTypeFunction,
				Label: "caller", Service: "svc", File: mainFile, Line: 1,
				Meta: map[string]string{"end_line": "3"}},
		}
	}
	makeRefs := func() []graph.UnresolvedRef {
		return []graph.UnresolvedRef{
			{Service: "svc", File: mainFile, Line: 2, Name: "save", Kind: "call_ref"},
		}
	}
	svcFiles := map[string][]string{"svc": {fileA, fileB, mainFile}}

	edges1, _, _ := LinkJSGlobals(makeNodes(), makeRefs(), nil, svcFiles)
	edges2, _, _ := LinkJSGlobals(makeNodes(), makeRefs(), nil, svcFiles)

	require.Equal(t, len(edges1), len(edges2))
	ids1 := make([]string, len(edges1))
	ids2 := make([]string, len(edges2))
	for i := range edges1 {
		ids1[i] = edges1[i].ID
	}
	for i := range edges2 {
		ids2[i] = edges2[i].ID
	}
	sort.Strings(ids1)
	sort.Strings(ids2)
	assert.Equal(t, ids1, ids2, "two runs produce identical edge IDs")
}

// TestExtractHandlerCallee: unit tests for the inline handler callee extractor.
func TestExtractHandlerCallee(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"save()", "save"},
		{"App.submit(this)", "App"},
		{"submitForm()", "submitForm"},
		{"save", "save"},
		{"", ""},
		{"123bad()", ""},       // leading digit is not an identifier
		{"   save() ", "save"}, // leading whitespace stripped
	}
	for _, tc := range cases {
		got := extractHandlerCallee(tc.input)
		assert.Equal(t, tc.want, got, "input=%q", tc.input)
	}
}
