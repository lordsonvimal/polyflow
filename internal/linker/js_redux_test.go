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

// TestLinkJSRedux_ReduxToolkit covers the path-independent Redux Toolkit
// shapes: createSlice (slice + reducer + per-key creator/type), configureStore
// ({reducer:{…}} → store + slices), and createAsyncThunk (creator + type).
func TestLinkJSRedux_ReduxToolkit(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"features/counter/counterSlice.js": `import { createSlice, createAsyncThunk } from "@reduxjs/toolkit";
export const fetchCount = createAsyncThunk("counter/fetchCount", async () => 1);
const counterSlice = createSlice({
  name: "counter",
  initialState: { value: 0 },
  reducers: {
    increment(state) { state.value += 1; },
    setValue: (state, action) => { state.value = action.payload; },
  },
});
export const { increment, setValue } = counterSlice.actions;
export default counterSlice.reducer;
`,
		"app/store.js": `import { configureStore } from "@reduxjs/toolkit";
import counterReducer from "../features/counter/counterSlice";
export const store = configureStore({ reducer: { counter: counterReducer } });
`,
		"features/counter/Counter.jsx": `import { useDispatch } from "react-redux";
import { increment } from "./counterSlice";
function Counter() {
  const dispatch = useDispatch();
  return dispatch(increment());
}
`,
	})
	sliceFile := p["features/counter/counterSlice.js"]
	counter := jsFuncNode("svc", p["features/counter/Counter.jsx"], "Counter", 3)

	newNodes, edges := LinkJSRedux([]graph.Node{counter}, map[string][]string{
		"svc": {sliceFile, p["app/store.js"], p["features/counter/Counter.jsx"]},
	})

	byRole := map[string]int{}
	for _, e := range edges {
		if r := e.Meta["redux"]; r != "" {
			byRole[r]++
		}
	}
	var haveSlice, haveStore bool
	for _, n := range newNodes {
		if n.ID == "redux_slice:counter" {
			haveSlice = true
		}
		if n.Meta["redux"] == "store" {
			haveStore = true
		}
	}
	if !haveSlice {
		t.Errorf("no redux_slice:counter node; nodes=%+v", newNodes)
	}
	if !haveStore {
		t.Errorf("no redux_store node; nodes=%+v", newNodes)
	}
	// increment + setValue → creator_type; fetchCount → creator_type = 3 total.
	if byRole["creator_type"] < 3 {
		t.Errorf("want >=3 creator_type edges, got %d (%+v)", byRole["creator_type"], edges)
	}
	if byRole["type_handled_by"] < 2 {
		t.Errorf("want >=2 type_handled_by edges, got %d", byRole["type_handled_by"])
	}
	if byRole["reducer_slice"] < 1 {
		t.Errorf("want configureStore reducer_slice edge, got %d", byRole["reducer_slice"])
	}
	// `dispatch(increment())` in the component resolves to the createSlice creator.
	var dispatchLinked bool
	for _, e := range edges {
		if e.From == counter.ID && e.Type == graph.EdgeTypeCalls &&
			(e.Meta["redux"] == "dispatch_call" || e.Meta["redux"] == "props_bound_dispatch") {
			dispatchLinked = true
		}
	}
	if !dispatchLinked {
		t.Errorf("dispatch(increment()) did not link to the slice creator; edges=%+v", edges)
	}
}

// TestLinkJSRedux_WholeStateSelector (JCM.10 follow-up): a `state => state` /
// `{ ...state }` selector depends on the entire store, not one slice — it must
// link to the combined `redux_store` node so a reverse trace from any slice
// still reaches it.
func TestLinkJSRedux_WholeStateSelector(t *testing.T) {
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
		"containers/WholeMSP.jsx": `function mapStateToProps(state) {
  return { ...state };
}
`,
		"components/WholeHook.jsx": `import { useSelector } from "react-redux";
function WholeHook() {
  return useSelector(s => s);
}
`,
	})
	msp := jsFuncNode("svc", p["containers/WholeMSP.jsx"], "mapStateToProps", 1)
	hook := jsFuncNode("svc", p["components/WholeHook.jsx"], "WholeHook", 2)

	newNodes, edges := LinkJSRedux([]graph.Node{msp, hook}, map[string][]string{
		"svc": {
			p["reducers/FooReducer.jsx"], p["containers/Root.jsx"],
			p["containers/WholeMSP.jsx"], p["components/WholeHook.jsx"],
		},
	})

	storeID := ""
	for _, n := range newNodes {
		if n.Meta["redux"] == "store" {
			storeID = n.ID
		}
	}
	if storeID == "" {
		t.Fatalf("no redux_store node; nodes=%+v", newNodes)
	}

	want := map[string]bool{"msp->store": false, "hook->store": false}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReads && e.To == storeID &&
			e.Meta["redux"] == "whole_state_read" && e.Meta["tier"] == "jcm10" {
			if e.From == msp.ID {
				want["msp->store"] = true
			}
			if e.From == hook.ID {
				want["hook->store"] = true
			}
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing %s; edges=%+v", k, edges)
		}
	}
}

// TestLinkJSRedux_ThunkAndConnectShorthand (JCM.11) covers a redux-thunk async
// creator whose inner dispatches are attributed to the outer creator, and a
// connect(null, { load }) container whose handler links to that creator.
func TestLinkJSRedux_ThunkAndConnectShorthand(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"constants/ActionTypes.jsx": `import keyMirror from "keymirror";
export default keyMirror({ SET_LOADING: null, LOADED: null });
`,
		"actions/AC.jsx": `import ActionTypes from "../constants/ActionTypes";
import actionCreator from "redux-action-utils";
export const setLoading = actionCreator(ActionTypes.SET_LOADING);
export const load = () => (dispatch) => {
  dispatch(setLoading());
  dispatch({ type: ActionTypes.LOADED });
};
`,
		"components/Panel.jsx": `import { connect } from "react-redux";
import { load } from "../actions/AC";
class Panel extends React.Component {
  onMount = () => {
    this.props.load();
  };
}
export default connect(null, { load })(Panel);
`,
	})
	setLoading := jsFuncNode("svc", p["actions/AC.jsx"], "setLoading", 3)
	loadC := jsFuncNode("svc", p["actions/AC.jsx"], "load", 4)
	onMount := jsFuncNode("svc", p["components/Panel.jsx"], "onMount", 4)

	newNodes, edges := LinkJSRedux([]graph.Node{setLoading, loadC, onMount}, map[string][]string{
		"svc": {p["constants/ActionTypes.jsx"], p["actions/AC.jsx"], p["components/Panel.jsx"]},
	})

	loadedID := ""
	for _, n := range newNodes {
		if n.Meta["redux_action_type"] == "LOADED" {
			loadedID = n.ID
		}
	}
	if loadedID == "" {
		t.Fatalf("no LOADED action-type node; nodes=%+v", newNodes)
	}

	want := map[string]bool{"load->setLoading": false, "load->LOADED": false, "onMount->load": false}
	for _, e := range edges {
		switch {
		case e.Type == graph.EdgeTypeCalls && e.From == loadC.ID && e.To == setLoading.ID &&
			e.Meta["redux"] == "thunk_dispatch":
			want["load->setLoading"] = true
		case e.Type == graph.EdgeTypeReferences && e.From == loadC.ID && e.To == loadedID &&
			e.Meta["redux"] == "thunk_dispatch":
			want["load->LOADED"] = true
		case e.Type == graph.EdgeTypeCalls && e.From == onMount.ID && e.To == loadC.ID &&
			e.Meta["redux"] == "props_bound_dispatch":
			want["onMount->load"] = true
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
