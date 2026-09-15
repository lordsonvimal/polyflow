package pipeline_test

// FX.8.9 (2026-09-15): js_mobx — internal/linker/js_mobx.go's retired
// LinkJSMobx, migrated onto patterns/javascript/js_mobx.yaml +
// rules/javascript/js_mobx.dl, driven by the js_mobx_sites hub provider
// (internal/factpipe/hub_js_mobx.go). Real-parse tests (temp-dir fixture
// files, reusing js_hoc_test.go's jhWriteFixture/jhClassNode helpers, same
// package), porting the retired Go test's fixtures verbatim.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func jmActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_mobx")
	if fw == nil {
		t.Fatal("js_mobx framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func jmMemberNode(svc, file, label string, line int, typ graph.NodeType, meta map[string]string) graph.Node {
	m := map[string]string{}
	for k, v := range meta {
		m[k] = v
	}
	return graph.Node{
		ID: string(typ) + ":" + svc + ":" + file + ":" + label,
		Type: typ, Label: label, Service: svc, File: file, Line: line,
		Language: "javascript", Meta: m,
	}
}

func jmRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(jmActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func jmPatchByID(res pipeline.Result) map[string]int {
	m := make(map[string]int, len(res.Patches))
	for i, p := range res.Patches {
		m[p.ID] = i
	}
	return m
}

// TestJSMobxRule_AutoObservable checks MobX's default rule: data fields
// become observable, methods action, getters computed.
func TestJSMobxRule_AutoObservable(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	cls := jhClassNode("svc", f, "Grid", 2, 13)
	count := jmMemberNode("svc", f, "count", 5, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})
	double := jmMemberNode("svc", f, "double", 6, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	inc := jmMemberNode("svc", f, "inc", 9, graph.NodeTypeFunction, map[string]string{"class": "Grid", "member_kind": "method"})

	res := jmRun(t, []graph.Node{cls, count, double, inc}, []string{f})
	patches := jmPatchByID(res)

	want := map[string]string{count.ID: "observable", double.ID: "computed", inc.ID: "action"}
	for id, kind := range want {
		idx, ok := patches[id]
		if !ok {
			t.Errorf("member %s not patched; patches=%+v", id, res.Patches)
			continue
		}
		got := res.Patches[idx]
		if got.Meta["mobx"] != kind {
			t.Errorf("member %s: want mobx=%s, got %q", id, kind, got.Meta["mobx"])
		}
		if got.Meta["tier"] != "jcm4" {
			t.Errorf("member %s missing tier=jcm4: %+v", id, got.Meta)
		}
	}
}

// TestJSMobxRule_MakeObservableMap covers the explicit annotation map,
// including minting a synthetic node for a field no parser captured.
func TestJSMobxRule_MakeObservableMap(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"stores/Store.js": `import { makeObservable, observable, action, computed } from "mobx";
class Store {
  constructor() {
    makeObservable(this, { x: observable, bump: action.bound, sum: computed });
  }
}
`,
	})
	f := p["stores/Store.js"]
	cls := jhClassNode("svc", f, "Store", 2, 6)

	res := jmRun(t, []graph.Node{cls}, []string{f})

	kinds := map[string]string{}
	for _, n := range res.Nodes {
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

// TestJSMobxRule_AutorunReactiveRead wires an autorun callback to the
// observable member it reads.
func TestJSMobxRule_AutorunReactiveRead(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	cls := jhClassNode("svc", f, "Grid", 2, 10)
	ctor := jmMemberNode("svc", f, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "member_kind": "method"})
	count := jmMemberNode("svc", f, "count", 3, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})

	res := jmRun(t, []graph.Node{cls, ctor, count}, []string{f})

	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeReads && e.To == count.ID &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing reactive_read edge to count; edges=%+v", res.Edges)
	}
}

// TestJSMobxRule_ObserverRenderReactiveRead (JCM.7) wires an observer-wrapped
// component to the store observable its render body reads.
func TestJSMobxRule_ObserverRenderReactiveRead(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	cls := jhClassNode("svc", sf, "Grid", 2, 5)
	total := jmMemberNode("svc", sf, "total", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	row := jmMemberNode("svc", cf, "Row", 2, graph.NodeTypeVariable, nil)

	res := jmRun(t, []graph.Node{cls, total, row}, []string{sf, cf})

	found := false
	for _, e := range res.Edges {
		if e.From == row.ID && e.To == total.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["mobx_via"] == "observer_render" && e.Meta["tier"] == "jcm7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing observer_render reactive_read Row->total; edges=%+v", res.Edges)
	}
}

// TestJSMobxRule_ImportedStoreReactiveRead (JCM.8) resolves an autorun that
// reads an imported store singleton's observable.
func TestJSMobxRule_ImportedStoreReactiveRead(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	gridCls := jhClassNode("svc", sf, "GridStore", 2, 5)
	rows := jmMemberNode("svc", sf, "rows", 3, graph.NodeTypeVariable, map[string]string{"class": "GridStore", "scope": "class_field"})
	watchCls := jhClassNode("svc", wf, "Watcher", 3, 6)
	ctor := jmMemberNode("svc", wf, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Watcher", "member_kind": "method"})

	res := jmRun(t, []graph.Node{gridCls, rows, watchCls, ctor}, []string{sf, wf})

	found := false
	for _, e := range res.Edges {
		if e.From == ctor.ID && e.To == rows.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm8" && e.Meta["mobx_resolve"] == "import" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing jcm8 import reactive_read constructor->rows; edges=%+v", res.Edges)
	}
}

// TestJSMobxRule_RootStoreReactiveRead (JCM.8) resolves a reaction tracking
// function that hops through a root store to a sub-store observable.
func TestJSMobxRule_RootStoreReactiveRead(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	gridCls := jhClassNode("svc", gf, "Grid", 2, 5)
	total := jmMemberNode("svc", gf, "total", 4, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	rootCls := jhClassNode("svc", rf, "RootStore", 1, 1)
	watchCls := jhClassNode("svc", wf, "Watcher", 3, 6)
	ctor := jmMemberNode("svc", wf, "constructor", 4, graph.NodeTypeFunction, map[string]string{"class": "Watcher", "member_kind": "method"})

	res := jmRun(t, []graph.Node{gridCls, total, rootCls, watchCls, ctor}, []string{gf, rf, wf})

	found := false
	for _, e := range res.Edges {
		if e.From == ctor.ID && e.To == total.ID && e.Type == graph.EdgeTypeReads &&
			e.Meta["mobx"] == "reactive_read" && e.Meta["tier"] == "jcm8" && e.Meta["mobx_resolve"] == "root_store" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing jcm8 root_store reactive_read constructor->total; edges=%+v", res.Edges)
	}
}

// TestJSMobxRule_ComputedDepAndDecorators (JCM.9) covers legacy decorator
// annotations plus computed -> observable dependency edges.
func TestJSMobxRule_ComputedDepAndDecorators(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"stores/Grid.js": `import { observable, computed, action } from "mobx";
class Grid {
  @observable count = 0;
  @observable.ref extra = 0;
  @computed get total() {
    return this.count + this.extra;
  }
  @action.bound inc() {
    this.count += 1;
  }
}
`,
	})
	f := p["stores/Grid.js"]
	cls := jhClassNode("svc", f, "Grid", 2, 12)
	count := jmMemberNode("svc", f, "count", 3, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})
	extra := jmMemberNode("svc", f, "extra", 4, graph.NodeTypeVariable, map[string]string{"class": "Grid", "scope": "class_field"})
	total := jmMemberNode("svc", f, "total", 5, graph.NodeTypeFunction, map[string]string{"class": "Grid", "js_accessor": "true"})
	inc := jmMemberNode("svc", f, "inc", 8, graph.NodeTypeFunction, map[string]string{"class": "Grid", "member_kind": "method"})

	res := jmRun(t, []graph.Node{cls, count, extra, total, inc}, []string{f})
	patches := jmPatchByID(res)

	for id, want := range map[string]string{count.ID: "observable", extra.ID: "observable", total.ID: "computed", inc.ID: "action"} {
		idx, ok := patches[id]
		if !ok || res.Patches[idx].Meta["mobx"] != want {
			t.Errorf("decorator tag %s: want %s, got patch=%+v (ok=%v)", id, want, res.Patches, ok)
		}
	}

	want := map[string]bool{count.ID: false, extra.ID: false}
	for _, e := range res.Edges {
		if e.From == total.ID && e.Type == graph.EdgeTypeReads && e.Meta["mobx"] == "computed_dep" && e.Meta["tier"] == "jcm9" {
			if _, ok := want[e.To]; ok {
				want[e.To] = true
			}
		}
	}
	for id, ok := range want {
		if !ok {
			t.Errorf("missing computed_dep edge total->%s; edges=%+v", id, res.Edges)
		}
	}
}

// TestJSMobxRule_NoMobxNoOutput guards against firing on a plain class.
func TestJSMobxRule_NoMobxNoOutput(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
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
	cls := jhClassNode("svc", f, "Plain", 1, 10)
	render := jmMemberNode("svc", f, "render", 2, graph.NodeTypeFunction, map[string]string{"class": "Plain"})
	res := jmRun(t, []graph.Node{cls, render}, []string{f})
	if len(res.Nodes) != 0 || len(res.Patches) != 0 || len(res.Edges) != 0 {
		t.Errorf("plain class produced output: nodes=%+v patches=%+v edges=%+v", res.Nodes, res.Patches, res.Edges)
	}
}
