package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func writeReduxFixture(t *testing.T, files map[string]string) (string, map[string]string) {
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
	return dir, paths
}

// TestLinkJSRedux_FullChain wires a minimal Redux app and asserts the complete
// dispatch chain is reconstructed:
//
//	Container.onClick --calls--> AC.setFoo
//	  --references--> ActionTypes.SET_FOO
//	    --references--> FooReducer
func TestLinkJSRedux_FullChain(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"constants/ActionTypes.jsx": `import keyMirror from "keymirror";
export default keyMirror({ SET_FOO: null });
`,
		"actions/AC.jsx": `import ActionTypes from "../constants/ActionTypes";
import actionCreator from "redux-action-utils";
export default {
  setFoo: actionCreator(ActionTypes.SET_FOO, "payload"),
};
`,
		"reducers/FooReducer.jsx": `import ActionTypes from "../constants/ActionTypes";
export default function FooReducer(state, action) {
  switch (action.type) {
    case ActionTypes.SET_FOO:
      return state;
    default:
      return state;
  }
}
`,
		"components/Container.jsx": `import AC from "../actions/AC";
import { bindActionCreators } from "redux";
class Container extends React.Component {
  onClick = () => {
    this.props.actions.setFoo(1);
  };
}
function mapDispatchToProps(dispatch) {
  return { actions: bindActionCreators(AC, dispatch) };
}
`,
	})

	onClick := jsFuncNode("svc", p["components/Container.jsx"], "onClick", 4)
	reducer := jsFuncNode("svc", p["reducers/FooReducer.jsx"], "FooReducer", 2)
	nodes := []graph.Node{onClick, reducer}

	newNodes, edges := LinkJSRedux(nodes, map[string][]string{
		"svc": {
			p["constants/ActionTypes.jsx"], p["actions/AC.jsx"],
			p["reducers/FooReducer.jsx"], p["components/Container.jsx"],
		},
	})

	// action-type constant node was minted
	var setFooType string
	for _, n := range newNodes {
		if n.Label == "SET_FOO" && n.Meta["redux_action_type"] == "SET_FOO" {
			setFooType = n.ID
		}
	}
	if setFooType == "" {
		t.Fatalf("no synthetic action-type node for SET_FOO; nodes=%+v", newNodes)
	}

	has := func(typ graph.EdgeType, from, to string) bool {
		for _, e := range edges {
			if e.Type == typ && e.From == from && e.To == to && e.Meta["tier"] == "jcm3" {
				return true
			}
		}
		return false
	}

	// creator -> action type
	var setFooCreator string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReferences && e.To == setFooType && e.Meta["redux"] == "creator_type" {
			setFooCreator = e.From
		}
	}
	if setFooCreator == "" {
		t.Fatalf("no creator --references--> SET_FOO edge; edges=%+v", edges)
	}
	// action type -> reducer
	if !has(graph.EdgeTypeReferences, setFooType, reducer.ID) {
		t.Errorf("missing SET_FOO --references--> FooReducer; edges=%+v", edges)
	}
	// component handler -> creator
	if !has(graph.EdgeTypeCalls, onClick.ID, setFooCreator) {
		t.Errorf("missing onClick --calls--> setFoo creator (%s); edges=%+v", setFooCreator, edges)
	}
}

// TestLinkJSRedux_NoReduxNoEdges guards against false positives on a plain
// component file with a switch statement unrelated to action types.
func TestLinkJSRedux_NoReduxNoEdges(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"components/Plain.jsx": `function classify(kind) {
  switch (kind) {
    case "a":
      return 1;
    default:
      return 0;
  }
}
`,
	})
	newNodes, edges := LinkJSRedux(nil, map[string][]string{"svc": {p["components/Plain.jsx"]}})
	if len(newNodes) != 0 || len(edges) != 0 {
		t.Errorf("plain file must not produce redux nodes/edges; nodes=%+v edges=%+v", newNodes, edges)
	}
}
