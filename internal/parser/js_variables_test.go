package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

const jsVarFixture = `const config = { url: "/api" };
let counter = 0;
const NAME = "polyflow";

function bump() {
  counter += 1;
  return config.url;
}

function makeAdder() {
  let total = 0;
  return () => {
    total += 1;
    return total;
  };
}

function report(cfg) {}

report(config);
report(NAME);

class Store {
  items = [];
  add(x) {}
}
`

func parseJSVarFixture(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "app.js")
	if err := os.WriteFile(file, []byte(jsVarFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, edges, _, _ := extractJSVariables(file, "web", "javascript", "javascript", src)
	return nodes, edges
}

func jsNode(nodes []graph.Node, typ graph.NodeType, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

func jsEdge(edges []graph.Edge, typ graph.EdgeType, fromSub, toSub string) *graph.Edge {
	for i := range edges {
		e := &edges[i]
		if e.Type == typ && contains(e.From, fromSub) && contains(e.To, toSub) {
			return e
		}
	}
	return nil
}

// Two sibling functions each declaring their own `var el = function(id){...}`
// and calling it locally: the pattern matcher's own node-name index never
// sees these nodes (no YAML pattern captures a non-arrow, non-top-level `var
// x = function(){}`), so without emitNestedLocalCallEdge both reads as
// zero-caller dead code regardless of the obvious local call sites — the
// real bug hit in the juniper corpus (app-config.js).
const jsNestedLocalCallFixture = `function outerA() {
  var el = function (id) {
    return document.getElementById(id);
  };
  el("a");
}

function outerB() {
  var el = function (id) {
    return document.getElementById(id);
  };
  el("b");
}
`

func TestJSVariables_NestedLocalCallEdgesDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.js")
	if err := os.WriteFile(file, []byte(jsNestedLocalCallFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, edges, unresolved, _ := extractJSVariables(file, "web", "javascript", "javascript", src)
	_ = unresolved

	var elNodes []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && n.Label == "el" {
			elNodes = append(elNodes, n)
		}
	}
	if len(elNodes) != 2 {
		t.Fatalf("el nodes = %d, want 2 (one per outer scope)", len(elNodes))
	}

	outerA := jsNode(nodes, graph.NodeTypeFunction, "outerA")
	outerB := jsNode(nodes, graph.NodeTypeFunction, "outerB")
	if outerA == nil || outerB == nil {
		t.Fatalf("missing outerA/outerB nodes")
	}

	callsFrom := func(fromID string) string {
		for _, e := range edges {
			if e.Type == graph.EdgeTypeCalls && e.From == fromID {
				return e.To
			}
		}
		return ""
	}
	toFromA := callsFrom(outerA.ID)
	toFromB := callsFrom(outerB.ID)
	if toFromA == "" || toFromB == "" {
		t.Fatalf("missing calls edge: outerA->%q outerB->%q", toFromA, toFromB)
	}
	if toFromA == toFromB {
		t.Fatalf("outerA and outerB's el(...) calls resolved to the SAME node %q — sibling scopes collided", toFromA)
	}
	// Each edge must land on the el declared inside that SAME outer function.
	var elA, elB *graph.Node
	for i := range elNodes {
		if elNodes[i].ID == toFromA {
			elA = &elNodes[i]
		}
		if elNodes[i].ID == toFromB {
			elB = &elNodes[i]
		}
	}
	if elA == nil || elB == nil {
		t.Fatalf("calls edges did not land on the two el nodes: got %q and %q", toFromA, toFromB)
	}
}

// A `get`/`set` accessor is invoked by property read/write syntax
// (`obj.value`), never a call expression — no pattern anywhere in the graph
// can produce a `calls` edge to one, so it must be stamped js_accessor=true
// for indexer.go's classifyRoot to route it to root_kind=callback instead of
// the "unreachable" default (the real bug: snapshot-init.js's `get value()`
// read as permanently dead code).
const jsAccessorFixture = `function makeBox() {
  return {
    get value() {
      return 1;
    },
    set value(v) {
      this._v = v;
    },
    plainMethod() {
      return 2;
    },
  };
}
`

func TestJSVariables_AccessorMethodsStampedJSAccessor(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.js")
	if err := os.WriteFile(file, []byte(jsAccessorFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, _, _, _ := extractJSVariables(file, "web", "javascript", "javascript", src)

	var getters, setters, plain int
	for _, n := range nodes {
		if n.Type != graph.NodeTypeFunction || n.Label != "value" {
			if n.Type == graph.NodeTypeFunction && n.Label == "plainMethod" {
				if n.Meta["js_accessor"] == "true" {
					t.Errorf("plainMethod must not be stamped js_accessor")
				}
				plain++
			}
			continue
		}
		if n.Meta["js_accessor"] != "true" {
			t.Errorf("accessor node at line %d missing js_accessor=true (meta=%v)", n.Line, n.Meta)
		}
		if n.Line == 3 {
			getters++
		} else if n.Line == 6 {
			setters++
		}
	}
	if getters != 1 || setters != 1 || plain != 1 {
		t.Fatalf("getters=%d setters=%d plain=%d, want 1/1/1", getters, setters, plain)
	}
}

func TestJSVariables_ModuleDeclarations(t *testing.T) {
	t.Parallel()
	nodes, _ := parseJSVarFixture(t)

	cfg := jsNode(nodes, graph.NodeTypeVariable, "config")
	if cfg == nil {
		t.Fatalf("missing variable node for config; nodes: %+v", nodes)
	}
	if cfg.Meta["data_type"] != "object" || cfg.Meta["kind"] != "const" || cfg.Meta["mutable"] != "false" {
		t.Errorf("config meta wrong: %+v", cfg.Meta)
	}

	counter := jsNode(nodes, graph.NodeTypeVariable, "counter")
	if counter == nil || counter.Meta["mutable"] != "true" || counter.Meta["data_type"] != "number" {
		t.Errorf("counter node wrong: %+v", counter)
	}
}

func TestJSVariables_WritesAndReads(t *testing.T) {
	t.Parallel()
	nodes, edges := parseJSVarFixture(t)
	_ = nodes

	w := jsEdge(edges, graph.EdgeTypeWrites, "function:bump", "variable:counter")
	if w == nil {
		t.Fatalf("missing writes edge bump -> counter; edges: %+v", edges)
	}
	if w.Confidence != graph.ConfidenceInferred {
		t.Errorf("structural writes must be inferred, got %q", w.Confidence)
	}
	if jsEdge(edges, graph.EdgeTypeReads, "function:bump", "variable:config") == nil {
		t.Errorf("missing reads edge bump -> config")
	}
}

func TestJSVariables_ClosureCapture(t *testing.T) {
	t.Parallel()
	nodes, edges := parseJSVarFixture(t)

	total := jsNode(nodes, graph.NodeTypeVariable, "total")
	if total == nil {
		t.Fatalf("missing captured variable node for total")
	}
	if total.Meta["scope"] != "captured" {
		t.Errorf("total scope should be captured, got %s", total.Meta["scope"])
	}
	cap := jsEdge(edges, graph.EdgeTypeCaptures, "function:makeAdder", "variable:total")
	if cap == nil {
		t.Fatalf("missing captures edge makeAdder -> total; edges: %+v", edges)
	}
	if cap.Confidence != graph.ConfidenceInferred || cap.Meta["by"] != "ref" {
		t.Errorf("same-file capture should be inferred confidence by ref: %+v", cap)
	}
	if jsEdge(edges, graph.EdgeTypeWrites, "function:makeAdder", "variable:total") == nil {
		t.Errorf("missing closure write makeAdder -> total")
	}
}

func TestJSVariables_FlowsTo(t *testing.T) {
	t.Parallel()
	_, edges := parseJSVarFixture(t)

	obj := jsEdge(edges, graph.EdgeTypeFlowsTo, "variable:config", "function:report")
	if obj == nil {
		t.Fatalf("missing flows_to config -> report; edges: %+v", edges)
	}
	if obj.Meta["mode"] != "ref" {
		t.Errorf("object argument should flow by ref, got %s", obj.Meta["mode"])
	}
	prim := jsEdge(edges, graph.EdgeTypeFlowsTo, "variable:NAME", "function:report")
	if prim == nil || prim.Meta["mode"] != "value" {
		t.Errorf("string argument should flow by value: %+v", prim)
	}
}

func TestJSVariables_Class(t *testing.T) {
	t.Parallel()
	nodes, _ := parseJSVarFixture(t)

	cls := jsNode(nodes, graph.NodeTypeClass, "Store")
	if cls == nil {
		t.Fatalf("missing class node for Store")
	}
	if !contains(cls.Meta["methods"], "add") {
		t.Errorf("class methods missing add: %+v", cls.Meta)
	}
	if !contains(cls.Meta["fields"], "items") {
		t.Errorf("class fields missing items: %+v", cls.Meta)
	}
}

// A named function *expression* that closes over an outer local must get a
// backing function node. The chessleap `createPostMoveQueue` shape
// (`return function enqueue(fn) { … chain … }`) emitted a captures edge whose
// `from` had no node, failing the edges."from" FK during indexing.
func TestJSVariables_NamedFunctionExpressionCapture(t *testing.T) {
	t.Parallel()
	src := []byte(`export function createPostMoveQueue() {
  let chain = Promise.resolve();
  return function enqueue(fn) {
    const next = chain.then(() => fn()).catch(() => {});
    chain = next;
    return next;
  };
}
`)
	nodes, edges, _, _ := extractJSVariables("move-sync.js", "web", "javascript", "javascript", src)

	if jsNode(nodes, graph.NodeTypeFunction, "enqueue") == nil {
		t.Fatalf("named function expression enqueue has no backing node; nodes: %+v", nodes)
	}
	if jsEdge(edges, graph.EdgeTypeCaptures, "function:enqueue", "variable:chain") == nil {
		t.Fatalf("missing captures edge enqueue -> chain; edges: %+v", edges)
	}

	// The FK invariant: every edge endpoint must resolve to an emitted node.
	ids := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		ids[n.ID] = true
	}
	for _, e := range edges {
		if !ids[e.From] {
			t.Errorf("edge %s has dangling From %q", e.ID, e.From)
		}
		if !ids[e.To] {
			t.Errorf("edge %s has dangling To %q", e.ID, e.To)
		}
	}
}

// TypeScript type annotations become data_type verbatim.
func TestJSVariables_TSTypeAnnotation(t *testing.T) {
	t.Parallel()
	src := []byte("const layouts: string[] = [];\nexport const port: number = 4;\n")
	nodes, _, _, _ := extractJSVariables("x.ts", "web", "typescript", "typescript", src)

	l := jsNode(nodes, graph.NodeTypeVariable, "layouts")
	if l == nil || l.Meta["data_type"] != "string[]" {
		t.Errorf("layouts data_type: %+v", l)
	}
	p := jsNode(nodes, graph.NodeTypeVariable, "port")
	if p == nil || p.Meta["data_type"] != "number" {
		t.Errorf("exported port not tracked or wrong type: %+v", p)
	}
}
