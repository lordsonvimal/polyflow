package pipeline_test

// FX.8.8 (2026-09-15): js_hoc — internal/linker/js_hoc.go's retired
// LinkJSHOC, migrated onto patterns/javascript/js_hoc.yaml +
// rules/javascript/js_hoc.dl, driven by the js_hoc_sites hub provider
// (internal/factpipe/hub_js_hoc.go). Real-parse tests (temp-dir fixture
// files), porting the retired Go test's fixtures verbatim — every case in
// js_hoc.go's retired test file has a twin here. `patch:` (not `mint:`) is
// asserted via res.Patches, since the target nodes here are pre-existing.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func jhActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_hoc")
	if fw == nil {
		t.Fatal("js_hoc framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func jhWriteFixture(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	paths := make(map[string]string, len(files))
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
	}
	return paths
}

func jhVarNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID: string(graph.NodeTypeVariable) + ":" + svc + ":" + file + ":" + label,
		Type: graph.NodeTypeVariable, Label: label, Service: svc, File: file, Line: line,
		Language: "javascript",
	}
}

func jhClassNode(svc, file, label string, line, endLine int) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":class:" + label, Type: graph.NodeTypeClass,
		Label: label, Service: svc, File: file, Line: line, EndLine: endLine, Language: "javascript",
	}
}

func jhFuncNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":function:" + label, Type: graph.NodeTypeFunction,
		Label: label, Service: svc, File: file, Line: line, Language: "javascript",
	}
}

func jhRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(jhActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func jhPatchByID(res pipeline.Result) map[string]int {
	m := make(map[string]int, len(res.Patches))
	for i, p := range res.Patches {
		m[p.ID] = i
	}
	return m
}

// TestJSHOCRule_InlineArrowBecomesComponent covers `const C = observer(arrow)`.
func TestJSHOCRule_InlineArrowBecomesComponent(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/Row.jsx": `import { observer } from "mobx-react";
const Row = observer((props) => {
  return <div>{props.grid.total}</div>;
});
export default Row;
`,
	})
	f := p["components/Row.jsx"]
	v := jhVarNode("svc", f, "Row", 2)

	res := jhRun(t, []graph.Node{v}, []string{f})
	patches := jhPatchByID(res)
	idx, ok := patches[v.ID]
	if !ok {
		t.Fatalf("Row not patched; patches=%+v", res.Patches)
	}
	got := res.Patches[idx]
	if got.Meta["hoc"] != "observer" || !got.Component || got.Meta["tier"] != "jcm6" {
		t.Errorf("Row patch = %+v, want hoc=observer component=true tier=jcm6", got)
	}
}

// TestJSHOCRule_IdentifierStampsDeclaration covers `C = observer(C)` and
// `export default observer(C)` where C is a real class/function declaration.
func TestJSHOCRule_IdentifierStampsDeclaration(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/Grid.jsx": `import { observer } from "mobx-react";
class Grid extends React.Component {
  render() { return null; }
}
Grid = observer(Grid);
export default Grid;
`,
		"components/Panel.jsx": `import { observer } from "mobx-react";
function Panel() { return null; }
export default observer(Panel);
`,
	})
	gf := p["components/Grid.jsx"]
	pf := p["components/Panel.jsx"]
	grid := jhClassNode("svc", gf, "Grid", 2, 4)
	panel := jhFuncNode("svc", pf, "Panel", 2)

	res := jhRun(t, []graph.Node{grid, panel}, []string{gf, pf})
	patches := jhPatchByID(res)
	gi, ok := patches[grid.ID]
	if !ok || res.Patches[gi].Meta["hoc"] != "observer" {
		t.Errorf("Grid class not patched: %+v", res.Patches)
	}
	pi, ok := patches[panel.ID]
	if !ok || res.Patches[pi].Meta["hoc"] != "observer" {
		t.Errorf("Panel function not patched: %+v", res.Patches)
	}
	if res.Patches[gi].Component {
		t.Errorf("Grid wrongly marked component=true")
	}
}

// TestJSHOCRule_NoHOCNoOutput guards against firing on plain code.
func TestJSHOCRule_NoHOCNoOutput(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/Plain.jsx": `const Plain = (props) => <div>{props.x}</div>;
export default Plain;
`,
	})
	f := p["components/Plain.jsx"]
	v := jhVarNode("svc", f, "Plain", 1)
	res := jhRun(t, []graph.Node{v}, []string{f})
	if len(res.Patches) != 0 || len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Errorf("plain component produced output: patches=%+v nodes=%+v edges=%+v", res.Patches, res.Nodes, res.Edges)
	}
}

// TestJSHOCRule_AppLocalWrapperDefaultExport covers SPA.1: an app-local HOC
// wrapping a react-redux connect() currying of an in-file component. The
// default export must become a synthetic component node labelled by the
// file basename, with a component_impl edge to the inner component.
func TestJSHOCRule_AppLocalWrapperDefaultExport(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/CDMTopLevel.jsx": `import { connect } from "react-redux";
import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class CDMTopLevelInner extends React.Component {
  render() { return <div/>; }
}
const mapStateToProps = s => s;
const mapDispatchToProps = d => ({});
export default ComponentWithAjaxStatus(
  connect(mapStateToProps, mapDispatchToProps)(CDMTopLevelInner)
);
`,
	})
	f := p["components/CDMTopLevel.jsx"]
	inner := jhClassNode("svc", f, "CDMTopLevelInner", 3, 5)

	res := jhRun(t, []graph.Node{inner}, []string{f})

	var synth *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Label == "CDMTopLevel" {
			synth = &res.Nodes[i]
		}
	}
	if synth == nil {
		t.Fatalf("no synthetic CDMTopLevel node; nodes=%+v", res.Nodes)
	}
	if synth.Meta["component"] != "true" || synth.Meta["hoc"] != "app_local" {
		t.Errorf("synth meta = %+v", synth.Meta)
	}

	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeComponentImpl && e.From == synth.ID && e.To == inner.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no component_impl %s -> %s; edges=%+v", synth.ID, inner.ID, res.Edges)
	}
}

// TestJSHOCRule_AppLocalInlineClass covers `const C = wrap(connect(...)(class
// extends React.Component {…}))` then `export default C` — the wrapped value
// is an anonymous class, so the C variable node itself must be patched.
func TestJSHOCRule_AppLocalInlineClass(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/CDMTopLevel.jsx": `import { connect } from "react-redux";
import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
const CDMTopLevel = ComponentWithAjaxStatus(
  connect(s => s, d => ({}))(
    class extends React.Component {
      render() { return <div>{this.props.x}</div>; }
    }
  )
);
export default CDMTopLevel;
`,
	})
	f := p["components/CDMTopLevel.jsx"]
	v := jhVarNode("svc", f, "CDMTopLevel", 3)

	res := jhRun(t, []graph.Node{v}, []string{f})
	if len(res.Nodes) != 0 {
		t.Errorf("unexpected synthetic nodes: %+v", res.Nodes)
	}
	patches := jhPatchByID(res)
	idx, ok := patches[v.ID]
	if !ok {
		t.Fatalf("CDMTopLevel not patched; patches=%+v", res.Patches)
	}
	if !res.Patches[idx].Component || res.Patches[idx].Meta["hoc"] != "app_local" {
		t.Errorf("CDMTopLevel patch = %+v", res.Patches[idx])
	}
}

// TestJSHOCRule_AppLocalWrapperConstBinding covers `const Wrapped =
// mywrap(Inner)` — the existing Wrapped variable node is patched, no
// synthetic node.
func TestJSHOCRule_AppLocalWrapperConstBinding(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"components/Panel.jsx": `import mywrap from "../common/mywrap";
class PanelInner extends React.Component { render() { return <div/>; } }
const Wrapped = mywrap(PanelInner);
export { Wrapped };
`,
	})
	f := p["components/Panel.jsx"]
	innerC := jhClassNode("svc", f, "PanelInner", 2, 2)
	wrapped := jhVarNode("svc", f, "Wrapped", 3)

	res := jhRun(t, []graph.Node{innerC, wrapped}, []string{f})
	if len(res.Nodes) != 0 {
		t.Errorf("unexpected synthetic nodes: %+v", res.Nodes)
	}
	patches := jhPatchByID(res)
	idx, ok := patches[wrapped.ID]
	if !ok || !res.Patches[idx].Component || res.Patches[idx].Meta["hoc"] != "app_local" {
		t.Errorf("Wrapped patch = %+v (ok=%v)", res.Patches, ok)
	}
}

// TestJSHOCRule_NotAComponentArg guards over-stamping: the wrapped identifier
// is a plain lowercase function, not a component → no patch, no node.
func TestJSHOCRule_NotAComponentArg(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"util/Totals.jsx": `import memoize from "lodash/memoize";
function computeTotals(rows) { return rows.length; }
export default memoize(computeTotals);
`,
	})
	f := p["util/Totals.jsx"]
	fn := jhFuncNode("svc", f, "computeTotals", 2)

	res := jhRun(t, []graph.Node{fn}, []string{f})
	if len(res.Patches) != 0 || len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Errorf("memoize(computeTotals) produced output: p=%+v n=%+v e=%+v", res.Patches, res.Nodes, res.Edges)
	}
}
