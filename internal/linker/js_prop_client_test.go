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

// urlsOf returns the Meta["url"] of every http_client node minted.
func urlsOf(nodes []graph.Node) []string {
	var out []string
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			out = append(out, nodes[i].Meta["url"])
		}
	}
	return out
}

func TestDynamicURLBuilder_TemplateReturn(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"grids/T.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
const getDataURL = (t) => ` + "`/api/${t}s`" + `;
class T extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(T);
`,
	})
	nodes, _, ledger := LinkJSPropClients(nil, map[string][]string{"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["grids/T.jsx"]}})
	if got := urlsOf(nodes); len(got) != 1 || got[0] != "/api/*s" {
		t.Errorf("urls = %v, want [/api/*s]; ledger=%+v", got, ledger)
	}
}

func TestDynamicURLBuilder_SwitchReturns(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"grids/S.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
function getDataURL(t) {
  switch (t) {
    case "a": return "/api/a";
    case "b": return "/api/b";
  }
}
class S extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(S);
`,
	})
	nodes, _, _ := LinkJSPropClients(nil, map[string][]string{"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["grids/S.jsx"]}})
	got := urlsOf(nodes)
	want := map[string]bool{"/api/a": true, "/api/b": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("urls = %v, want /api/a + /api/b", got)
	}
}

func TestDynamicURLBuilder_Opaque(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"grids/O.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
const getDataURL = (t) => buildIt(t);
class O extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(O);
`,
	})
	nodes, _, ledger := LinkJSPropClients(nil, map[string][]string{"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["grids/O.jsx"]}})
	if got := urlsOf(nodes); len(got) != 0 {
		t.Errorf("opaque builder minted nodes: %v", got)
	}
	found := false
	for _, u := range ledger {
		if u.Kind == "dynamic_url_builder" && u.Name == "getDataURL" {
			found = true
		}
	}
	if !found {
		t.Errorf("want dynamic_url_builder ledger naming getDataURL; got %+v", ledger)
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
		if u.Kind == "dynamic_url_builder" && u.Name == "getDataURL" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dynamic_url_builder ledger entry for getDataURL; got %+v", ledger)
	}
}

// ── Tier CW: a plain-parameter receiver ─────────────────────────────────────

// TestLinkJSPropClients_ParamReceiverResolves is CW's core case. The file has no
// `this.props` anywhere — it is a module-level helper handed the transport as an
// argument — so SPA.4's corroboration gate saw a bare `ajaxStatus.get(...)` with
// no evidence and dropped it. 23 of the 70 files calling this transport on the
// audit corpus were shaped this way and had zero http_client nodes between them.
func TestLinkJSPropClients_ParamReceiverResolves(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/config.jsx": `function fetchConfig(ajaxStatus) {
  if (!ajaxStatus || !ajaxStatus.get) return;
  ajaxStatus.get("Loading...", "/api/prefs/assistant_config");
}
export default fetchConfig;
`,
	})
	in := []graph.Node{
		{ID: "svc:utils/config.jsx:function:fetchConfig:1", Type: graph.NodeTypeFunction,
			Label: "fetchConfig", Service: "svc", File: "utils/config.jsx", Line: 1, Language: "javascript"},
	}
	nodes, edges, ledger := LinkJSPropClients(in, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/config.jsx"]},
	})
	if len(ledger) != 0 {
		t.Errorf("unexpected ledger: %+v", ledger)
	}
	if got := urlsOf(nodes); len(got) != 1 || got[0] != "/api/prefs/assistant_config" {
		t.Fatalf("urls = %v, want [/api/prefs/assistant_config]", got)
	}
	var id string
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			id = nodes[i].ID
		}
	}
	if !hasEdge(edges, graph.EdgeTypeCalls, "svc:utils/config.jsx:function:fetchConfig:1", id) {
		t.Errorf("no calls edge from the enclosing helper; edges=%+v", edges)
	}
}

// TestLinkJSPropClients_ParamNameMustMatchASpec is the guard, and it is the half
// of CW that can go wrong quietly. `get` is one of the commonest method names in
// any codebase and this pass has no types; the only thing standing between it and
// a flood of phantom clients is that the parameter has to be named after a
// transport the service actually injects. A parameter called anything else stays
// invisible even though the call shape is identical.
func TestLinkJSPropClients_ParamNameMustMatchASpec(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/cache.jsx": `function readThrough(store) {
  return store.get("Loading...", "/api/prefs/assistant_config");
}
`,
	})
	nodes, edges, ledger := LinkJSPropClients(nil, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/cache.jsx"]},
	})
	if got := urlsOf(nodes); len(got) != 0 {
		t.Errorf("a parameter matching no spec minted %v", got)
	}
	if len(edges) != 0 || len(ledger) != 0 {
		t.Errorf("edges=%+v ledger=%+v, want both empty", edges, ledger)
	}
}

// TestLinkJSPropClients_ParamReceiverDynamicURLLedgers: the guard says an
// unreadable URL is ledgered, never guessed.
func TestLinkJSPropClients_ParamReceiverDynamicURLLedgers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/load.jsx": `function load(ajaxStatus, kind) {
  ajaxStatus.get("Loading...", buildUrl(kind));
}
`,
	})
	nodes, edges, ledger := LinkJSPropClients(nil, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/load.jsx"]},
	})
	if got := urlsOf(nodes); len(got) != 0 {
		t.Errorf("dynamic URL minted %v", got)
	}
	if len(edges) != 0 {
		t.Errorf("unexpected edges: %+v", edges)
	}
	// Kind is dynamic_url_builder rather than prop_client_dynamic_url because the
	// URL argument names a function: the existing SPA.5 classifier is what says
	// so, and CW changes only which call sites reach it.
	if len(ledger) != 1 || ledger[0].Kind != "dynamic_url_builder" ||
		ledger[0].Name != "buildUrl" || ledger[0].Line != 2 {
		t.Fatalf("ledger = %+v, want one dynamic_url_builder for buildUrl at line 2", ledger)
	}
}

// TestLinkJSPropClients_MissingURLArgLedgers covers the case that made this gap
// invisible for a whole tier: a recognised transport call whose argument list
// does not reach the URL position at all. It cannot mint a node, and before CW it
// also said nothing, so the graph carried no trace of a dropped flow. A ledger
// row is the minimum honest answer.
func TestLinkJSPropClients_MissingURLArgLedgers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/ping.jsx": `function ping(ajaxStatus) {
  ajaxStatus.get("Loading...");
}
`,
	})
	nodes, _, ledger := LinkJSPropClients(nil, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/ping.jsx"]},
	})
	if got := urlsOf(nodes); len(got) != 0 {
		t.Errorf("a call with no URL argument minted %v", got)
	}
	if len(ledger) != 1 || ledger[0].Kind != "prop_client_dynamic_url" || ledger[0].Line != 2 {
		t.Fatalf("ledger = %+v, want one prop_client_dynamic_url at line 2", ledger)
	}
}

// TestLinkJSPropClients_ParamScopeDoesNotLeak. The widened set is a property of
// one function's subtree, not of the file: a sibling function that never received
// the transport must not inherit the vouching. Otherwise CW would quietly restore
// the file-wide credulity the SPA.4 gate was written to prevent.
func TestLinkJSPropClients_ParamScopeDoesNotLeak(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/two.jsx": `function withTransport(ajaxStatus) {
  ajaxStatus.get("Loading...", "/api/one");
}
function withoutTransport(ajaxStatus2) {
  const ajaxStatus = makeSomethingElse();
  ajaxStatus.get("Loading...", "/api/two");
}
`,
	})
	nodes, _, _ := LinkJSPropClients(nil, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/two.jsx"]},
	})
	got := urlsOf(nodes)
	if len(got) != 1 || got[0] != "/api/one" {
		t.Errorf("urls = %v, want only [/api/one]", got)
	}
}

// TestLinkJSPropClients_DestructuredParamReceiver covers the three spellings of
// "the transport arrived from the caller" that a function component uses instead
// of `this.props.<name>`. All three were silent misses on the audit corpus after
// the plain-parameter case was closed, and all three are the same fact: the
// object being unpacked is provably a parameter, so its contents came from
// outside. Table-driven because the guard is identical in each and the risk is
// covering one spelling and calling the shape done.
func TestLinkJSPropClients_DestructuredParamReceiver(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"object pattern parameter", `const Table = ({ ajaxStatus, canEdit }) => {
  ajaxStatus.get("Loading...", "/api/vlms");
};
`},
		{"destructured from a props parameter", `const Listing = props => {
  const { allowBlank, ajaxStatus } = props;
  ajaxStatus.get("Loading...", "/api/vlms");
};
`},
		{"destructured from a parameter's props", `function loadData(component) {
  const { ajaxStatus, i18n } = component.props;
  ajaxStatus.get("Loading...", "/api/vlms");
}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, p := writeReduxFixture(t, map[string]string{
				"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
				"components/T.jsx":                   tc.src,
			})
			nodes, _, _ := LinkJSPropClients(nil, map[string][]string{
				"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["components/T.jsx"]},
			})
			if got := urlsOf(nodes); len(got) != 1 || got[0] != "/api/vlms" {
				t.Errorf("urls = %v, want [/api/vlms]", got)
			}
		})
	}
}

// TestLinkJSPropClients_DestructureFromACallIsNotVouched. The rule is "provably
// from a parameter", not "destructured from something". A transport unpacked out
// of a function's return value has an origin this pass cannot see through, and
// admitting it would let any `const { ajaxStatus } = anything()` mint clients.
// It stays a miss — but a ledgered one, never a silent one.
func TestLinkJSPropClients_DestructureFromACallIsNotVouched(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC,
		"utils/listener.jsx": `function createListener(getProps) {
  const { actions, ajaxStatus } = getProps();
  ajaxStatus.get("Loading...", "/api/flags");
}
`,
	})
	nodes, _, _ := LinkJSPropClients(nil, map[string][]string{
		"svc": {p["common/ComponentWithAjaxStatus.jsx"], p["utils/listener.jsx"]},
	})
	if got := urlsOf(nodes); len(got) != 0 {
		t.Errorf("a call-return destructure minted %v", got)
	}
}
