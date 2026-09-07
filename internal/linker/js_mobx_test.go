package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func mobxMemberNode(svc, file, label string, line int, typ graph.NodeType, meta map[string]string) graph.Node {
	m := map[string]string{}
	for k, v := range meta {
		m[k] = v
	}
	return graph.Node{
		ID:       string(typ) + ":" + svc + ":" + file + ":" + label, // stable, unique per label
		Type:     typ,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "javascript",
		Meta:     m,
	}
}

// TestLinkJSMobx_AutoObservable checks MobX's default rule: data fields become
// observable, methods action, getters computed.
func TestLinkJSMobx_AutoObservable(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/Grid.js": `import { makeAutoObservable } from "mobx";
class Grid {
  constructor() {
    makeAutoObservable(this);
  }
  get double() {
    return this.count * 2;
  }
  inc() {
    this.count += 1;
  }
}
`,
	})
	f := p["stores/Grid.js"]
	cls := jsClassNode("svc", f, "Grid", 2, 13)
	count := mobxMemberNode("svc", f, "count", 5, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})
	double := mobxMemberNode("svc", f, "double", 6, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	inc := mobxMemberNode("svc", f, "inc", 9, graph.NodeTypeFunction, map[string]string{"class": "Grid", "member_kind": "method"})

	_, tagged, _ := LinkJSMobx([]graph.Node{cls, count, double, inc}, map[string][]string{"svc": {f}})

	want := map[string]string{count.ID: "observable", double.ID: "computed", inc.ID: "action"}
	got := map[string]string{}
	for _, n := range tagged {
		got[n.ID] = n.Meta["mobx"]
		if n.Meta["tier"] != "jcm4" {
			t.Errorf("node %s missing tier=jcm4: %+v", n.ID, n.Meta)
		}
	}
	for id, kind := range want {
		if got[id] != kind {
			t.Errorf("member %s: want mobx=%s, got %q (all: %+v)", id, kind, got[id], got)
		}
	}
}

// TestLinkJSMobx_MakeObservableMap covers the explicit annotation map, including
// minting a synthetic node for a field no parser captured.
func TestLinkJSMobx_MakeObservableMap(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/Store.js": `import { makeObservable, observable, action, computed } from "mobx";
class Store {
  constructor() {
    makeObservable(this, { x: observable, bump: action.bound, sum: computed });
  }
}
`,
	})
	f := p["stores/Store.js"]
	cls := jsClassNode("svc", f, "Store", 2, 6)

	newNodes, tagged, _ := LinkJSMobx([]graph.Node{cls}, map[string][]string{"svc": {f}})
	_ = tagged

	kinds := map[string]string{}
	for _, n := range newNodes {
		if n.Meta["class"] == "Store" && n.Meta["synthetic"] == "true" {
			kinds[n.Label] = n.Meta["mobx"]
		}
	}
	for label, want := range map[string]string{"x": "observable", "bump": "action", "sum": "computed"} {
		if kinds[label] != want {
			t.Errorf("synthetic %s: want mobx=%s, got %q (all: %+v)", label, want, kinds[label], kinds)
		}
	}
}

// TestLinkJSMobx_AutorunReactiveRead wires an autorun callback to the observable
// member it reads.
func TestLinkJSMobx_AutorunReactiveRead(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/Grid.js": `import { makeAutoObservable, autorun } from "mobx";
class Grid {
  count = 0;
  constructor() {
    makeAutoObservable(this);
    autorun(() => {
      console.log(this.count);
    });
  }
}
`,
	})
	f := p["stores/Grid.js"]
	cls := jsClassNode("svc", f, "Grid", 2, 10)
	ctor := mobxMemberNode("svc", f, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "member_kind": "method"})
	count := mobxMemberNode("svc", f, "count", 3, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})

	_, _, edges := LinkJSMobx([]graph.Node{cls, ctor, count}, map[string][]string{"svc": {f}})

	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReads && e.To == count.ID &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing reactive_read edge to count; edges=%+v", edges)
	}
}

// TestLinkJSMobx_ObserverRenderReactiveRead (JCM.7) wires an observer-wrapped
// component to the store observable its render body reads.
func TestLinkJSMobx_ObserverRenderReactiveRead(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/Grid.js": `import { makeAutoObservable } from "mobx";
class Grid {
  constructor() { makeAutoObservable(this); }
  get total() { return 3; }
}
`,
		"components/Row.jsx": `import { observer } from "mobx-react";
const Row = observer(class extends React.Component {
  render() {
    return this.props.grid.total;
  }
});
export default Row;
`,
	})
	sf := p["stores/Grid.js"]
	cf := p["components/Row.jsx"]
	cls := jsClassNode("svc", sf, "Grid", 2, 5)
	total := mobxMemberNode("svc", sf, "total", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	row := mobxMemberNode("svc", cf, "Row", 2, graph.NodeTypeVariable, nil)

	_, _, edges := LinkJSMobx([]graph.Node{cls, total, row}, map[string][]string{"svc": {sf, cf}})

	found := false
	for _, e := range edges {
		if e.From == row.ID && e.To == total.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["mobx_via"] == "observer_render" && e.Meta["tier"] == "jcm7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing observer_render reactive_read Row->total; edges=%+v", edges)
	}
}

// TestLinkJSMobx_ImportedStoreReactiveRead (JCM.8) resolves an autorun that
// reads an imported store singleton's observable.
func TestLinkJSMobx_ImportedStoreReactiveRead(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/gridStore.js": `import { makeAutoObservable } from "mobx";
class GridStore {
  rows = [];
  constructor() { makeAutoObservable(this); }
}
export default new GridStore();
`,
		"watch/Watcher.js": `import { autorun } from "mobx";
import gridStore from "../stores/gridStore";
class Watcher {
  constructor() {
    autorun(() => { const x = gridStore.rows; return x; });
  }
}
`,
	})
	sf := p["stores/gridStore.js"]
	wf := p["watch/Watcher.js"]
	gridCls := jsClassNode("svc", sf, "GridStore", 2, 5)
	rows := mobxMemberNode("svc", sf, "rows", 3, graph.NodeTypeVariable, map[string]string{"class": "GridStore", "scope": "class_field"})
	watchCls := jsClassNode("svc", wf, "Watcher", 3, 6)
	ctor := mobxMemberNode("svc", wf, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Watcher", "member_kind": "method"})

	_, _, edges := LinkJSMobx([]graph.Node{gridCls, rows, watchCls, ctor}, map[string][]string{"svc": {sf, wf}})

	found := false
	for _, e := range edges {
		if e.From == ctor.ID && e.To == rows.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm8" && e.Meta["mobx_resolve"] == "import" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing jcm8 import reactive_read constructor->rows; edges=%+v", edges)
	}
}

// TestLinkJSMobx_RootStoreReactiveRead (JCM.8) resolves a reaction tracking
// function that hops through a root store to a sub-store observable.
func TestLinkJSMobx_RootStoreReactiveRead(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"stores/Grid.js": `import { makeAutoObservable } from "mobx";
class Grid {
  constructor() { makeAutoObservable(this); }
  get total() { return 3; }
}
`,
		"stores/root.js": `class RootStore {}
export default new RootStore();
`,
		"watch/Watcher.js": `import { reaction } from "mobx";
import rootStore from "../stores/root";
class Watcher {
  constructor() {
    reaction(() => rootStore.grid.total, () => {});
  }
}
`,
	})
	gf := p["stores/Grid.js"]
	rf := p["stores/root.js"]
	wf := p["watch/Watcher.js"]
	gridCls := jsClassNode("svc", gf, "Grid", 2, 5)
	total := mobxMemberNode("svc", gf, "total", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	rootCls := jsClassNode("svc", rf, "RootStore", 1, 1)
	watchCls := jsClassNode("svc", wf, "Watcher", 3, 6)
	ctor := mobxMemberNode("svc", wf, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Watcher", "member_kind": "method"})

	_, _, edges := LinkJSMobx([]graph.Node{gridCls, total, rootCls, watchCls, ctor}, map[string][]string{"svc": {gf, rf, wf}})

	found := false
	for _, e := range edges {
		if e.From == ctor.ID && e.To == total.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm8" && e.Meta["mobx_resolve"] == "root_store" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing jcm8 root_store reactive_read constructor->total; edges=%+v", edges)
	}
}

// TestLinkJSMobx_NoMobxNoOutput guards against firing on a plain class.
func TestLinkJSMobx_NoMobxNoOutput(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"components/Plain.js": `class Plain {
  render() {
    switch (this.mode) {
      case "a":
        return 1;
      default:
        return 0;
    }
  }
}
`,
	})
	f := p["components/Plain.js"]
	cls := jsClassNode("svc", f, "Plain", 1, 10)
	render := mobxMemberNode("svc", f, "render", 2, graph.NodeTypeFunction, map[string]string{"class": "Plain"})
	nw, tagged, edges := LinkJSMobx([]graph.Node{cls, render}, map[string][]string{"svc": {f}})
	if len(nw) != 0 || len(tagged) != 0 || len(edges) != 0 {
		t.Errorf("plain class produced output: new=%+v tagged=%+v edges=%+v", nw, tagged, edges)
	}
}
