package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ajaxStatusHOC is the minimal shape of a transport-forwarding HOC: a
// `Name(Wrapped)` function that renders `<Wrapped ajaxStatus={this} />` and
// declares a class whose `get(msg, url)` reaches `window.$.ajax` via `ajax`.
const ajaxStatusHOC = `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    get = (msg, url) => {
      const request = { method: "GET", url };
      return this.ajax(msg, request);
    };
    ajax = (msg, request) => {
      return window.$.ajax(request);
    };
    render() {
      return (
        <div>
          <WrappedComponent ajaxStatus={this} {...this.props} />
        </div>
      );
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`

func TestDetectPropClients_AjaxStatusShape(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
	})
	f := p["common/ComponentWithAjaxStatus.jsx"]
	src, root, _, ok := jsParse(f)
	if !ok {
		t.Fatal("parse failed")
	}
	spec, ok := detectPropClientSpec(root, src, "common/ComponentWithAjaxStatus.jsx")
	if !ok {
		t.Fatal("expected a prop-client spec")
	}
	if spec.InjectedProp != "ajaxStatus" {
		t.Errorf("InjectedProp = %q, want ajaxStatus", spec.InjectedProp)
	}
	if spec.HOCExport != "ComponentWithAjaxStatus" {
		t.Errorf("HOCExport = %q", spec.HOCExport)
	}
	g, ok := spec.Methods["get"]
	if !ok {
		t.Fatalf("no get method; methods=%+v", spec.Methods)
	}
	if g.Verb != "GET" || g.URLArgIndex != 1 || g.URLOptKey != "" {
		t.Errorf("get method = %+v, want {GET 1 \"\"}", g)
	}
}

func TestLinkJSPropClients_CallSiteResolves(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"grids/VariablesGridView.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class VariablesGridView extends React.Component {
  loadVariables() {
    this.props.ajaxStatus.get("loading", ` + "`/api/standards/${this.props.standardId}/variables`" + `);
  }
}
export default ComponentWithAjaxStatus(VariablesGridView);
`,
	})
	hoc := p["common/ComponentWithAjaxStatus.jsx"]
	grid := p["grids/VariablesGridView.jsx"]

	in := []graph.Node{
		{ID: "svc:grids/VariablesGridView.jsx:method:loadVariables:3", Type: graph.NodeTypeMethod,
			Label: "loadVariables", Service: "svc", File: "grids/VariablesGridView.jsx", Line: 3, Language: "javascript"},
	}
	nodes, edges, ledger := LinkJSPropClients(in, map[string][]string{"svc": {hoc, grid}})

	if len(ledger) != 0 {
		t.Errorf("unexpected ledger: %+v", ledger)
	}
	var hc *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			hc = &nodes[i]
		}
	}
	if hc == nil {
		t.Fatalf("no http_client node minted; nodes=%+v", nodes)
	}
	if hc.Meta["url"] != "/api/standards/*/variables" {
		t.Errorf("url = %q, want /api/standards/*/variables", hc.Meta["url"])
	}
	if hc.Meta["method"] != "GET" {
		t.Errorf("method = %q", hc.Meta["method"])
	}
	if !hasEdge(edges, graph.EdgeTypeCalls, "svc:grids/VariablesGridView.jsx:method:loadVariables:3", hc.ID) {
		t.Errorf("no calls edge from loadVariables; edges=%+v", edges)
	}
}

func TestLinkJSPropClients_BareUnrelatedGetNoNode(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"util/thing.jsx": `function doThing(store) {
  return store.get("a", "b");
}
`,
	})
	hoc := p["common/ComponentWithAjaxStatus.jsx"]
	thing := p["util/thing.jsx"]
	nodes, edges, _ := LinkJSPropClients(nil, map[string][]string{"svc": {hoc, thing}})
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			t.Errorf("unrelated .get() minted a node: %+v", n)
		}
	}
	if len(edges) != 0 {
		t.Errorf("unexpected edges: %+v", edges)
	}
}

func TestLinkJSPropClients_DynamicURLLedger(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"grids/G.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class G extends React.Component {
  load(type) {
    this.props.ajaxStatus.get("loading", getDataURL(type));
  }
}
export default ComponentWithAjaxStatus(G);
`,
	})
	hoc := p["common/ComponentWithAjaxStatus.jsx"]
	g := p["grids/G.jsx"]
	nodes, _, ledger := LinkJSPropClients(nil, map[string][]string{"svc": {hoc, g}})
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			t.Errorf("dynamic URL minted a node: %+v", n)
		}
	}
	found := false
	for _, u := range ledger {
		if u.Kind == "prop_client_dynamic_url" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected prop_client_dynamic_url ledger entry; got %+v", ledger)
	}
}
