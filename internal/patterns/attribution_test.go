package patterns_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// A call site after a nested helper's body must attribute to the outer
// function, not the helper (regression: section→edgeRow, checkbox→…).
func TestMatchToGraph_EnclosingFunctionContainment(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var", // outer component: lines 10-100
			File:        "Filters.tsx",
			Line:        10,
			EndLine:     100,
			Captures:    map[string]string{"name": "Filters"},
		},
		{
			PatternName: "arrow_func_var", // nested helper: lines 12-17
			File:        "Filters.tsx",
			Line:        12,
			EndLine:     17,
			Captures:    map[string]string{"name": "checkbox"},
		},
		{
			PatternName: "fetch_call", // call site at line 68: inside Filters, outside checkbox
			File:        "Filters.tsx",
			Line:        68,
			Captures:    map[string]string{"url": `"/api/x"`},
		},
		{
			PatternName: "fetch_call", // call site at line 14: inside checkbox
			File:        "Filters.tsx",
			Line:        14,
			Captures:    map[string]string{"url": `"/api/y"`},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("web", results)

	byID := map[string]graph.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	fromLabelByToLine := map[int]string{}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			fromLabelByToLine[byID[e.To].Line] = byID[e.From].Label
		}
	}
	assert.Equal(t, "Filters", fromLabelByToLine[68],
		"call after the nested helper's end must attribute to the outer function")
	assert.Equal(t, "checkbox", fromLabelByToLine[14],
		"call inside the nested helper must attribute to the helper")
}

// Call-reference results (component_fn_call) use the same containment rule.
func TestMatchToGraph_CallRefContainment(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        90,
			EndLine:     500,
			Captures:    map[string]string{"name": "Detail"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        389,
			EndLine:     414,
			Captures:    map[string]string{"name": "section"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        171,
			EndLine:     190,
			Captures:    map[string]string{"name": "edgeRow"},
		},
		{
			PatternName: "component_fn_call", // edgeRow(...) at line 435: inside Detail only
			File:        "Detail.tsx",
			Line:        435,
			Captures:    map[string]string{"callee": "edgeRow"},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("web", results)

	labelByID := map[string]string{}
	for _, n := range nodes {
		labelByID[n.ID] = n.Label
	}
	require.Len(t, edges, 1)
	assert.Equal(t, "Detail", labelByID[edges[0].From])
	assert.Equal(t, "edgeRow", labelByID[edges[0].To])
}

// Two sibling scopes each declaring their own same-named local closure
// (`var el = function(id){...}`, a common throwaway-selector-helper idiom)
// and calling it locally must resolve to their OWN declaration, not collide
// on the flat file+name map's last-write-wins pick — the juniper corpus
// hit this for real: app-config.js declares `var el = ...` in two different
// outer functions, and one of the two was permanently starved of its very
// real, very local call site, reading as dead code.
func TestMatchToGraph_SiblingScopesSameLocalNameDoNotCollide(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var",
			File:        "app-config.js",
			Line:        1,
			EndLine:     20,
			Captures:    map[string]string{"name": "outerA"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "app-config.js",
			Line:        5,
			EndLine:     7,
			Captures:    map[string]string{"name": "el"},
		},
		{
			PatternName: "component_fn_call", // el(...) at line 10: inside outerA only
			File:        "app-config.js",
			Line:        10,
			Captures:    map[string]string{"callee": "el"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "app-config.js",
			Line:        30,
			EndLine:     50,
			Captures:    map[string]string{"name": "outerB"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "app-config.js",
			Line:        35,
			EndLine:     37,
			Captures:    map[string]string{"name": "el"},
		},
		{
			PatternName: "component_fn_call", // el(...) at line 40: inside outerB only
			File:        "app-config.js",
			Line:        40,
			Captures:    map[string]string{"callee": "el"},
		},
	}
	nodes, edges, unresolved := patterns.MatchToGraph("web", results)
	assert.Empty(t, unresolved)

	idByLine := map[int]string{}
	labelByID := map[string]string{}
	for _, n := range nodes {
		idByLine[n.Line] = n.ID
		labelByID[n.ID] = n.Label
	}
	require.Len(t, edges, 2)
	byFrom := map[string]string{}
	for _, e := range edges {
		byFrom[labelByID[e.From]] = e.To
	}
	assert.Equal(t, idByLine[5], byFrom["outerA"], "outerA's call must resolve to its OWN el (line 5), not outerB's")
	assert.Equal(t, idByLine[35], byFrom["outerB"], "outerB's call must resolve to its OWN el (line 35), not outerA's")
}

// Module-level JSX usage (render(<App/>) in index.tsx) gets a synthetic
// per-file module node as its caller instead of being dropped.
func TestMatchToGraph_ModuleLevelRender(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "jsx_component_self_closing",
			File:        "src/index.tsx",
			Line:        7,
			Captures:    map[string]string{"name": "App"},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("web", results)

	var moduleNode *graph.Node
	for i := range nodes {
		if nodes[i].Label == "(module)" {
			moduleNode = &nodes[i]
		}
	}
	require.NotNil(t, moduleNode, "expected a synthetic module node")
	assert.Equal(t, "src/index.tsx", moduleNode.File)

	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeRenders, edges[0].Type)
	assert.Equal(t, moduleNode.ID, edges[0].From)
}

// Non-JS files must not grow module nodes: Go has no top-level statements.
func TestMatchToGraph_NoModuleNodeForGo(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "http_get",
			File:        "client.go",
			Line:        20,
			Captures:    map[string]string{"url": `"/x"`},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)
	for _, n := range nodes {
		assert.NotEqual(t, "(module)", n.Label)
	}
	assert.Empty(t, edges)
}

// Top-level cobra RunE references in Go files attribute to the file's main
// (regression: every CLI subcommand appeared as a root node).
func TestMatchToGraph_GoTopLevelCallRefFallsBackToMain(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "cmd/app/main.go",
			Line:        29,
			EndLine:     40,
			Captures:    map[string]string{"name": "main"},
		},
		{
			PatternName: "func_decl",
			File:        "cmd/app/main.go",
			Line:        214,
			EndLine:     260,
			Captures:    map[string]string{"name": "runIndex"},
		},
		{
			PatternName: "cobra_run", // RunE: runIndex inside a package-level var block
			File:        "cmd/app/main.go",
			Line:        211,
			Captures:    map[string]string{"callee": "runIndex"},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

	labelByID := map[string]string{}
	for _, n := range nodes {
		labelByID[n.ID] = n.Label
		assert.NotEqual(t, "(module)", n.Label, "Go files must not grow module nodes")
	}
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeCalls, edges[0].Type)
	assert.Equal(t, "main", labelByID[edges[0].From])
	assert.Equal(t, "runIndex", labelByID[edges[0].To])
}

// Without main, top-level Go call refs fall back to init; a ref to init
// itself must not self-edge.
func TestMatchToGraph_GoTopLevelCallRefInitFallback(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "cmd/app/root.go",
			Line:        10,
			EndLine:     20,
			Captures:    map[string]string{"name": "init"},
		},
		{
			PatternName: "func_decl",
			File:        "cmd/app/root.go",
			Line:        30,
			EndLine:     60,
			Captures:    map[string]string{"name": "runRoot"},
		},
		{
			PatternName: "cobra_run",
			File:        "cmd/app/root.go",
			Line:        25,
			Captures:    map[string]string{"callee": "runRoot"},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

	labelByID := map[string]string{}
	for _, n := range nodes {
		labelByID[n.ID] = n.Label
	}
	require.Len(t, edges, 1)
	assert.Equal(t, "init", labelByID[edges[0].From])
	assert.Equal(t, "runRoot", labelByID[edges[0].To])
}

// A Go file with neither main nor init still drops top-level call refs, but
// the drop lands in the unresolved ledger instead of vanishing.
func TestMatchToGraph_GoTopLevelCallRefNoScopeDropped(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "cmd/app/other.go",
			Line:        30,
			EndLine:     60,
			Captures:    map[string]string{"name": "runOther"},
		},
		{
			PatternName: "cobra_run",
			File:        "cmd/app/other.go",
			Line:        25,
			Captures:    map[string]string{"callee": "runOther"},
		},
	}
	_, edges, unresolved := patterns.MatchToGraph("svc", results)
	assert.Empty(t, edges)
	require.Len(t, unresolved, 1)
	assert.Equal(t, "runOther", unresolved[0].Name)
	assert.Equal(t, "call_ref", unresolved[0].Kind)
	assert.Equal(t, 25, unresolved[0].Line)
}

// Unknown callees enter the unresolved ledger; JS runtime builtins do not.
func TestMatchToGraph_UnresolvedCallRefs(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var",
			File:        "app.tsx",
			Line:        3,
			EndLine:     20,
			Captures:    map[string]string{"name": "App"},
		},
		{
			PatternName: "component_fn_call", // imported/unknown function
			File:        "app.tsx",
			Line:        5,
			Captures:    map[string]string{"callee": "fetchWarnings"},
		},
		{
			PatternName: "component_fn_call", // runtime builtin: not a blind spot
			File:        "app.tsx",
			Line:        6,
			Captures:    map[string]string{"callee": "setTimeout"},
		},
	}
	_, _, unresolved := patterns.MatchToGraph("web", results)
	require.Len(t, unresolved, 1)
	assert.Equal(t, "fetchWarnings", unresolved[0].Name)
	assert.Equal(t, "web", unresolved[0].Service)
}

// global_member_call is scoped to the window.* namespace idiom: a
// window.maple.X() callee lands in the unresolved ledger under its full dotted
// path (for LinkJSGlobals to resolve against the global symbol table), while
// a non-window member call (Utils.format, this.foo) — which the YAML query's
// #match? predicate already filters at the tree-sitter level, but that gate
// only exists in the .yaml file, not here — is dropped by the same check
// matcher.go applies in Go, so a MatchResult built directly (bypassing the
// query) still can't leak one through.
func TestMatchToGraph_GlobalMemberCallScopedToWindow(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var",
			File:        "app.js",
			Line:        3,
			EndLine:     20,
			Captures:    map[string]string{"name": "init"},
		},
		{
			PatternName: "global_member_call",
			File:        "app.js",
			Line:        5,
			Captures:    map[string]string{"callee": "window.maple.queueToast"},
		},
		{
			PatternName: "global_member_call",
			File:        "app.js",
			Line:        6,
			Captures:    map[string]string{"callee": "Utils.format"},
		},
	}
	_, _, unresolved := patterns.MatchToGraph("web", results)
	require.Len(t, unresolved, 1)
	assert.Equal(t, "window.maple.queueToast", unresolved[0].Name)
	assert.Equal(t, "call_ref", unresolved[0].Kind)
}

// object_value_ref stamps MetaReferencedAsValue on the SAME-file function it
// names, not a new node or edge — a dispatch-table entry
// (`{ parse: parseDepPrefix }`) is only ever invoked indirectly
// (`entry.parse(x)`), the JS tree-sitter-pattern equivalent of Go's SSA
// referencedIDs.
func TestMatchToGraph_ObjectValueRefStampsTarget(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "function_decl",
			File:        "search-parser.js",
			Line:        10,
			EndLine:     12,
			Captures:    map[string]string{"name": "parseDepPrefix"},
		},
		{
			PatternName: "object_value_ref",
			File:        "search-parser.js",
			Line:        20,
			Captures:    map[string]string{"ref": "parseDepPrefix"},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("web", results)
	assert.Empty(t, edges, "object_value_ref must not create an edge — it stamps meta on the existing node")

	var found bool
	for _, n := range nodes {
		if n.Label == "parseDepPrefix" {
			found = true
			assert.Equal(t, "true", n.Meta[graph.MetaReferencedAsValue])
		}
	}
	assert.True(t, found)
}

// Anonymous goroutines produce a worker node spawned by the enclosing function.
func TestMatchToGraph_AnonGoroutineSpawns(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "main.go",
			Line:        340,
			EndLine:     380,
			Captures:    map[string]string{"name": "watchDB"},
		},
		{
			PatternName: "goroutine_anon",
			File:        "main.go",
			Line:        351,
			Captures:    map[string]string{},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

	var worker *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeWorker {
			worker = &nodes[i]
		}
	}
	require.NotNil(t, worker)
	assert.Equal(t, "go func()", worker.Label)

	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeSpawns, edges[0].Type)
	assert.Equal(t, worker.ID, edges[0].To)
}

// Pattern nodes and call refs inside a goroutine body attribute to the worker
// node (its span encloses them), while the worker itself is spawned by the
// outer function — so workers have outgoing flow.
func TestMatchToGraph_WorkerEnclosesBody(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "hub.go",
			Line:        100,
			EndLine:     140,
			Captures:    map[string]string{"name": "Run"},
		},
		{
			PatternName: "func_decl",
			File:        "hub.go",
			Line:        150,
			EndLine:     160,
			Captures:    map[string]string{"name": "flush"},
		},
		{
			PatternName: "goroutine_anon", // go func() {…} spanning 107-120
			File:        "hub.go",
			Line:        107,
			EndLine:     120,
			Captures:    map[string]string{},
		},
		{
			PatternName: "http_get", // inside the goroutine body
			File:        "hub.go",
			Line:        110,
			Captures:    map[string]string{"url": `"/ping"`},
		},
		{
			PatternName: "component_fn_call", // flush() inside the goroutine body
			File:        "hub.go",
			Line:        112,
			Captures:    map[string]string{"callee": "flush"},
		},
		{
			PatternName: "http_get", // after the goroutine body: back to Run
			File:        "hub.go",
			Line:        130,
			Captures:    map[string]string{"url": `"/done"`},
		},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

	byID := map[string]graph.Node{}
	var worker *graph.Node
	for i := range nodes {
		byID[nodes[i].ID] = nodes[i]
		if nodes[i].Type == graph.NodeTypeWorker {
			worker = &nodes[i]
		}
	}
	require.NotNil(t, worker)

	fromLabel := func(e graph.Edge) string { return byID[e.From].Label }
	var workerOut, spawnsIn int
	for _, e := range edges {
		to := byID[e.To]
		switch {
		case e.Type == graph.EdgeTypeSpawns && e.To == worker.ID:
			assert.Equal(t, "Run", fromLabel(e), "worker must be spawned by Run, not itself")
			spawnsIn++
		case e.From == worker.ID:
			workerOut++
			// Targets: the http_client node inside the body (line 110) and the
			// flush function declaration (line 150) via the body's call ref.
			assert.Contains(t, []int{110, 150}, to.Line, "worker outflow must be body http_get + flush declaration")
		case to.Line == 130:
			assert.Equal(t, "Run", fromLabel(e), "call after goroutine body attributes to Run")
		case to.Line == 110:
			t.Fatalf("body node at line %d attributed to %s, want worker", to.Line, fromLabel(e))
		}
	}
	assert.Equal(t, 1, spawnsIn, "exactly one spawns edge into the worker")
	assert.Equal(t, 2, workerOut, "http_get + flush call ref flow out of the worker")
}

// URL-builder helpers: fetch(mermaidURL(...)) resolves through the helper's
// returned template-literal prefix.
func TestMatchToGraph_URLBuilderFunctionResolution(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "fn_return_template_prefix",
			File:        "export.ts",
			Line:        14,
			Captures:    map[string]string{"name": "mermaidURL", "value": "`/api/export/mermaid?${sp.toString()}`"},
		},
		{
			PatternName: "func_decl",
			File:        "export.ts",
			Line:        30,
			EndLine:     34,
			Captures:    map[string]string{"name": "fetchMermaid"},
		},
		{
			PatternName: "fetch_call",
			File:        "export.ts",
			Line:        31,
			Captures:    map[string]string{"url": "mermaidURL(level, scope)"},
		},
	}
	nodes, _, _ := patterns.MatchToGraph("web", results)

	var client *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			client = &nodes[i]
		}
	}
	require.NotNil(t, client)
	assert.Equal(t, "/api/export/mermaid?${sp.toString()}", client.Meta["url"])
	assert.Equal(t, "inferred", client.Meta["url_confidence"])
}

// Event-handler assignment nodes get a readable label, not the pattern name.
func TestMatchToGraph_HandlerAssignLabel(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "ws_onmessage_assign",
			File:        "Notification.tsx",
			Line:        24,
			Captures:    map[string]string{"prop": "onmessage", "handler": "(evt) => {}"},
		},
	}
	nodes, _, _ := patterns.MatchToGraph("web", results)

	var sub *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeSubscriber {
			sub = &nodes[i]
		}
	}
	require.NotNil(t, sub)
	assert.Equal(t, "onmessage handler", sub.Label)
}
