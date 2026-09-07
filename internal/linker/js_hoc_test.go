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

	out := LinkJSHOC([]graph.Node{v}, map[string][]string{"svc": {f}})
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

	out := LinkJSHOC([]graph.Node{grid, panel}, map[string][]string{"svc": {gf, pf}})
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
	if out := LinkJSHOC([]graph.Node{v}, map[string][]string{"svc": {f}}); len(out) != 0 {
		t.Errorf("plain component produced output: %+v", out)
	}
}
