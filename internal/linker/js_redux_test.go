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

// TestLinkJSRedux_SpreadBoundDispatch covers `this.props.setFoo(1)` where the
// container spreads bindActionCreators output straight into props (no `actions:`
// namespace). The creator name must still resolve to the AC module.
func TestLinkJSRedux_SpreadBoundDispatch(t *testing.T) {
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
		"components/Widget.jsx": `import AC from "../actions/AC";
import { bindActionCreators } from "redux";
class Widget extends React.Component {
  onClick = () => {
    this.props.setFoo(1);
  };
}
function mapDispatchToProps(dispatch) {
  return { ...bindActionCreators(AC, dispatch) };
}
`,
	})
	onClick := jsFuncNode("svc", p["components/Widget.jsx"], "onClick", 4)
	_, edges := LinkJSRedux([]graph.Node{onClick}, map[string][]string{
		"svc": {p["constants/ActionTypes.jsx"], p["actions/AC.jsx"], p["components/Widget.jsx"]},
	})
	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == onClick.ID &&
			e.Meta["redux"] == "props_bound_dispatch" && e.Meta["tier"] == "jcm3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing props_bound_dispatch edge from onClick; edges=%+v", edges)
	}
}

// TestLinkJSRedux_DispatchCall covers the explicit `dispatch(AC.x(…))` shape.
func TestLinkJSRedux_DispatchCall(t *testing.T) {
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
		"components/Panel.jsx": `import AC from "../actions/AC";
class Panel extends React.Component {
  onClick = () => {
    this.props.dispatch(AC.setFoo(1));
  };
}
`,
	})
	onClick := jsFuncNode("svc", p["components/Panel.jsx"], "onClick", 3)
	_, edges := LinkJSRedux([]graph.Node{onClick}, map[string][]string{
		"svc": {p["constants/ActionTypes.jsx"], p["actions/AC.jsx"], p["components/Panel.jsx"]},
	})
	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == onClick.ID &&
			e.Meta["redux"] == "dispatch_call" && e.Meta["tier"] == "jcm3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing dispatch_call edge from onClick; edges=%+v", edges)
	}
}

// TestLinkJSRedux_DispatchObjectLiteral covers `dispatch({ type: ActionTypes.X })`
// (the common hooks-era shape) linking the dispatch site to the action type even
// when the reducer lives outside a reducers/ directory.
func TestLinkJSRedux_DispatchObjectLiteral(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"constants/ActionTypes.jsx": `import keyMirror from "keymirror";
export default keyMirror({ SET_FOO: null });
`,
		"hooks/useThing.jsx": `import ActionTypes from "../constants/ActionTypes";
function setFoo(dispatch, v) {
  dispatch({ type: ActionTypes.SET_FOO, payload: v });
}
`,
	})
	site := jsFuncNode("svc", p["hooks/useThing.jsx"], "setFoo", 2)
	newNodes, edges := LinkJSRedux([]graph.Node{site}, map[string][]string{
		"svc": {p["constants/ActionTypes.jsx"], p["hooks/useThing.jsx"]},
	})
	var typeID string
	for _, n := range newNodes {
		if n.Label == "SET_FOO" {
			typeID = n.ID
		}
	}
	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReferences && e.From == site.ID && e.To == typeID &&
			e.Meta["redux"] == "dispatch_type" && e.Meta["tier"] == "jcm3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing dispatch_type edge setFoo -> SET_FOO; edges=%+v", edges)
	}
}

// TestLinkJSRedux_CombineReducers covers the slice-map → store containment,
// including the `combineReducers(mapVar)` indirection.
func TestLinkJSRedux_CombineReducers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"reducers/FooReducer.jsx": `export default function FooReducer(state, action) {
  return state;
}
`,
		"containers/Root.jsx": `import { combineReducers } from "redux";
import FooReducer from "../reducers/FooReducer";
const reducersMap = { foo: FooReducer };
export default combineReducers(reducersMap);
`,
	})
	reducer := jsFuncNode("svc", p["reducers/FooReducer.jsx"], "FooReducer", 1)
	newNodes, edges := LinkJSRedux([]graph.Node{reducer}, map[string][]string{
		"svc": {p["reducers/FooReducer.jsx"], p["containers/Root.jsx"]},
	})
	var storeID string
	for _, n := range newNodes {
		if n.Label == "redux_store" && n.Meta["redux"] == "store" {
			storeID = n.ID
		}
	}
	if storeID == "" {
		t.Fatalf("no redux_store node minted; nodes=%+v", newNodes)
	}
	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeContains && e.From == storeID && e.To == reducer.ID &&
			e.Meta["redux"] == "reducer_slice" && e.Meta["tier"] == "jcm3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing redux_store --contains--> FooReducer; edges=%+v", edges)
	}
}

// TestLinkJSRedux_SelectorReadSide (JCM.10) covers the read direction: a
// combineReducers key becomes a redux_slice node --contains--> its reducer, and
// mapStateToProps / useSelector consumers get --reads--> that slice.
func TestLinkJSRedux_SelectorReadSide(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"reducers/FooReducer.jsx": `export default function FooReducer(state, action) {
  return state;
}
`,
		"containers/Root.jsx": `import { combineReducers } from "redux";
import FooReducer from "../reducers/FooReducer";
export default combineReducers({ fooSlice: FooReducer });
`,
		"containers/Container.jsx": `function mapStateToProps(state) {
  return { foo: state.fooSlice.value };
}
`,
		"components/Hooks.jsx": `import { useSelector } from "react-redux";
function Hooks() {
  const v = useSelector(s => s.fooSlice.value);
  return v;
}
`,
	})
	reducer := jsFuncNode("svc", p["reducers/FooReducer.jsx"], "FooReducer", 1)
	msp := jsFuncNode("svc", p["containers/Container.jsx"], "mapStateToProps", 1)
	hooks := jsFuncNode("svc", p["components/Hooks.jsx"], "Hooks", 2)

	newNodes, edges := LinkJSRedux([]graph.Node{reducer, msp, hooks}, map[string][]string{
		"svc": {
			p["reducers/FooReducer.jsx"], p["containers/Root.jsx"],
			p["containers/Container.jsx"], p["components/Hooks.jsx"],
		},
	})

	sliceID := ""
	for _, n := range newNodes {
		if n.ID == "redux_slice:fooSlice" && n.Meta["redux"] == "slice" {
			sliceID = n.ID
		}
	}
	if sliceID == "" {
		t.Fatalf("no redux_slice:fooSlice node; nodes=%+v", newNodes)
	}

	want := map[string]bool{"slice->reducer": false, "msp->slice": false, "hooks->slice": false}
	for _, e := range edges {
		if e.Meta["tier"] != "jcm10" {
			continue
		}
		if e.Type == graph.EdgeTypeContains && e.From == sliceID && e.To == reducer.ID {
			want["slice->reducer"] = true
		}
		if e.Type == graph.EdgeTypeReads && e.To == sliceID && e.Meta["redux"] == "selector_read" {
			if e.From == msp.ID {
				want["msp->slice"] = true
			}
			if e.From == hooks.ID {
				want["hooks->slice"] = true
			}
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing %s; edges=%+v", k, edges)
		}
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
