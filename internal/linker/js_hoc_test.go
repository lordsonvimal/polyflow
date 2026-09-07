package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func hocVarNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID:       string(graph.NodeTypeVariable) + ":" + svc + ":" + file + ":" + label,
		Type:     graph.NodeTypeVariable,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "javascript",
	}
}

func hocUpdatedByID(nodes []graph.Node) map[string]graph.Node {
	m := make(map[string]graph.Node, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}

// TestLinkJSHOC_InlineArrowBecomesComponent covers `const C = observer(arrow)`:
// the wrapper variable node is retagged as a component so js_link can resolve
// <C/> render sites to it.
func TestLinkJSHOC_InlineArrowBecomesComponent(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"components/Row.jsx": `import { observer } from "mobx-react";
const Row = observer((props) => {
  return <div>{props.grid.total}</div>;
});
export default Row;
`,
	})
	f := p["components/Row.jsx"]
	v := hocVarNode("svc", f, "Row", 2)

	out, _, _ := LinkJSHOC([]graph.Node{v}, map[string][]string{"svc": {f}})
	got := hocUpdatedByID(out)[v.ID]
	if got.ID == "" {
		t.Fatalf("Row not updated; out=%+v", out)
	}
	if got.Meta["hoc"] != "observer" || got.Meta["component"] != "true" || got.Meta["tier"] != "jcm6" {
		t.Errorf("Row meta = %+v, want hoc=observer component=true tier=jcm6", got.Meta)
	}
}

// TestLinkJSHOC_IdentifierStampsDeclaration covers `C = observer(C)` and
// `export default observer(C)` where C is a real class/function declaration.
func TestLinkJSHOC_IdentifierStampsDeclaration(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
	grid := jsClassNode("svc", gf, "Grid", 2, 4)
	panel := jsFuncNode("svc", pf, "Panel", 2)

	out, _, _ := LinkJSHOC([]graph.Node{grid, panel}, map[string][]string{"svc": {gf, pf}})
	by := hocUpdatedByID(out)
	if by[grid.ID].Meta["hoc"] != "observer" {
		t.Errorf("Grid class not stamped: %+v", by[grid.ID].Meta)
	}
	if by[panel.ID].Meta["hoc"] != "observer" {
		t.Errorf("Panel function not stamped: %+v", by[panel.ID].Meta)
	}
	// A declaration stamp is not a component retag.
	if by[grid.ID].Meta["component"] == "true" {
		t.Errorf("Grid wrongly marked component=true")
	}
}

// TestLinkJSHOC_NoHOCNoOutput guards against firing on plain code.
func TestLinkJSHOC_NoHOCNoOutput(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"components/Plain.jsx": `const Plain = (props) => <div>{props.x}</div>;
export default Plain;
`,
	})
	f := p["components/Plain.jsx"]
	v := hocVarNode("svc", f, "Plain", 1)
	out, newNodes, edges := LinkJSHOC([]graph.Node{v}, map[string][]string{"svc": {f}})
	if len(out) != 0 || len(newNodes) != 0 || len(edges) != 0 {
		t.Errorf("plain component produced output: updated=%+v new=%+v edges=%+v", out, newNodes, edges)
	}
}

// TestLinkJSHOC_AppLocalWrapperDefaultExport covers SPA.1: an app-local HOC
// (`ComponentWithAjaxStatus`) wrapping a react-redux `connect(...)` currying of
// an in-file component. The default export must become a component labelled by
// the file basename, with a component_impl edge to the inner component.
func TestLinkJSHOC_AppLocalWrapperDefaultExport(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
	inner := jsClassNode("svc", f, "CDMTopLevelInner", 3, 5)

	updated, newNodes, edges := LinkJSHOC([]graph.Node{inner}, map[string][]string{"svc": {f}})
	_ = updated

	var synth *graph.Node
	for i := range newNodes {
		if newNodes[i].Label == "CDMTopLevel" {
			synth = &newNodes[i]
		}
	}
	if synth == nil {
		t.Fatalf("no synthetic CDMTopLevel node; new=%+v", newNodes)
	}
	if synth.Meta["component"] != "true" || synth.Meta["hoc"] != "app_local" {
		t.Errorf("synth meta = %+v", synth.Meta)
	}
	if synth.Meta["hoc_inner"] != inner.ID {
		t.Errorf("hoc_inner = %q, want %q", synth.Meta["hoc_inner"], inner.ID)
	}
	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeComponentImpl && e.From == synth.ID && e.To == inner.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no component_impl %s -> %s; edges=%+v", synth.ID, inner.ID, edges)
	}
}

// TestLinkJSHOC_AppLocalInlineClass covers cedar's real shape:
// `const CDMTopLevel = withAjax(connect(...)(class extends React.Component {…}))`
// then `export default CDMTopLevel` — the wrapped value is an anonymous class,
// so the CDMTopLevel variable node itself must be stamped.
func TestLinkJSHOC_AppLocalInlineClass(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
	v := hocVarNode("svc", f, "CDMTopLevel", 3)

	updated, newNodes, _ := LinkJSHOC([]graph.Node{v}, map[string][]string{"svc": {f}})
	if len(newNodes) != 0 {
		t.Errorf("unexpected synthetic nodes: %+v", newNodes)
	}
	got := hocUpdatedByID(updated)[v.ID]
	if got.Meta["component"] != "true" || got.Meta["hoc"] != "app_local" {
		t.Errorf("CDMTopLevel meta = %+v", got.Meta)
	}
}

// TestLinkJSHOC_AppLocalWrapperConstBinding covers `const Wrapped = mywrap(Inner)`
// — the existing Wrapped variable node is stamped, no synthetic node.
func TestLinkJSHOC_AppLocalWrapperConstBinding(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"components/Panel.jsx": `import mywrap from "../common/mywrap";
class PanelInner extends React.Component { render() { return <div/>; } }
const Wrapped = mywrap(PanelInner);
export { Wrapped };
`,
	})
	f := p["components/Panel.jsx"]
	innerC := jsClassNode("svc", f, "PanelInner", 2, 2)
	wrapped := hocVarNode("svc", f, "Wrapped", 3)

	updated, newNodes, _ := LinkJSHOC([]graph.Node{innerC, wrapped}, map[string][]string{"svc": {f}})
	if len(newNodes) != 0 {
		t.Errorf("unexpected synthetic nodes: %+v", newNodes)
	}
	got := hocUpdatedByID(updated)[wrapped.ID]
	if got.ID == "" || got.Meta["component"] != "true" || got.Meta["hoc"] != "app_local" {
		t.Errorf("Wrapped meta = %+v", got.Meta)
	}
}

// TestLinkJSHOC_NotAComponentArg guards over-stamping: the wrapped identifier
// is a plain function (lowercase, not a component) → no stamp, no node.
func TestLinkJSHOC_NotAComponentArg(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"util/Totals.jsx": `import memoize from "lodash/memoize";
function computeTotals(rows) { return rows.length; }
export default memoize(computeTotals);
`,
	})
	f := p["util/Totals.jsx"]
	fn := jsFuncNode("svc", f, "computeTotals", 2)

	updated, newNodes, edges := LinkJSHOC([]graph.Node{fn}, map[string][]string{"svc": {f}})
	if len(updated) != 0 || len(newNodes) != 0 || len(edges) != 0 {
		t.Errorf("memoize(computeTotals) produced output: u=%+v n=%+v e=%+v", updated, newNodes, edges)
	}
}
