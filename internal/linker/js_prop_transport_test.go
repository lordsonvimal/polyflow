package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// gridParent owns the transport: postToServer's third parameter is the URL, and
// the function is handed to <ScheduleGrid> as a prop. The transport call is on
// line 3 — that is the prop_client_dynamic_url ledger site.
const gridParent = `export default class ScheduleTopLevel extends React.Component {
  postToServer = (transport, data, updateURL) => {
    return transport.ajax("Saving...", { url: updateURL, data, method: "PUT" });
  };
  render() {
    return <ScheduleGrid postToServer={this.postToServer} />;
  }
}
`

func propTransportNodes(parentAbs, childAbs string) []graph.Node {
	return []graph.Node{
		{ID: "svc:p:class:ScheduleTopLevel:1", Type: graph.NodeTypeClass, Label: "ScheduleTopLevel",
			Service: "svc", File: parentAbs, Line: 1, Language: "javascript"},
		{ID: "svc:p:method:postToServer:2", Type: graph.NodeTypeMethod, Label: "postToServer",
			Service: "svc", File: parentAbs, Line: 2, Language: "javascript"},
		{ID: "svc:c:class:ScheduleGrid:1", Type: graph.NodeTypeClass, Label: "ScheduleGrid",
			Service: "svc", File: childAbs, Line: 1, Language: "javascript"},
	}
}

func TestLinkJSPropTransport_WorkedExample(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": gridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.transport, this.state.data, ` + "`/api/schedules/${this.props.id}`" + `);
  };
  reset = () => {
    this.props.postToServer(this.props.transport, {}, "/api/schedules/reset");
  };
}
`,
	})
	parent := p["ScheduleTopLevel.jsx"]
	child := p["ScheduleGrid.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url", Name: "(dynamic)"},
	}
	files := map[string][]string{"svc": {parent, child}}

	nodes, edges, out, retract := LinkJSPropTransport(propTransportNodes(parent, child), ledger, files, nil)

	if len(out) != 0 {
		t.Fatalf("unexpected ledger: %+v", out)
	}
	got := map[string]bool{}
	for _, n := range nodes {
		got[n.Meta["url"]] = true
		if n.Meta["method"] != "PUT" || n.Meta["spa"] != "prop_transport" || n.Meta["via"] != "postToServer" {
			t.Errorf("bad meta: %+v", n.Meta)
		}
		if n.Meta["producer"] == "" || n.Meta["caller"] == "" {
			t.Errorf("missing provenance: %+v", n.Meta)
		}
		if n.File != parent || n.Line != 3 {
			t.Errorf("node not minted at the ledger site: %s:%d", n.File, n.Line)
		}
	}
	if len(got) != 2 || !got["/api/schedules/*"] || !got["/api/schedules/reset"] {
		t.Fatalf("urls = %v", got)
	}
	if !retract[PropURLRetractKey(parent, 3)] {
		t.Errorf("site not retracted: %v", retract)
	}
	if len(edges) != 2 {
		t.Errorf("want 2 calls edges, got %d", len(edges))
	}
}

// A child call site whose URL argument is itself unreadable must ledger, and it
// must not suppress the sibling that resolved.
func TestLinkJSPropTransport_UnresolvableArgLedgers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": gridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.transport, {}, "/api/schedules/save");
  };
  other = () => {
    this.props.postToServer(this.props.transport, {}, this.props.dynamicUrl);
  };
}
`,
	})
	parent := p["ScheduleTopLevel.jsx"]
	child := p["ScheduleGrid.jsx"]
	ledger := []graph.UnresolvedRef{{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"}}
	files := map[string][]string{"svc": {parent, child}}

	nodes, _, out, retract := LinkJSPropTransport(propTransportNodes(parent, child), ledger, files, nil)

	if len(nodes) != 1 || nodes[0].Meta["url"] != "/api/schedules/save" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !retract[PropURLRetractKey(parent, 3)] {
		t.Errorf("site should retract once a URL resolved")
	}
	var un *graph.UnresolvedRef
	for i := range out {
		if out[i].Kind == ledgerPropTransportUnresolved {
			un = &out[i]
		}
	}
	if un == nil || un.Name != "member_expression" || un.Targets != "postToServer" {
		t.Fatalf("want one prop_transport_unresolved(member_expression), got %+v", out)
	}
}

// The URL argument is a local binding in the enclosing function, not a literal
// at the call site — resolved through Tier UL's walk.
func TestLinkJSPropTransport_LocalBindingArg(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": gridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    const endpoint = ` + "`/api/schedules/${this.props.id}/publish`" + `;
    this.props.postToServer(this.props.transport, {}, endpoint);
  };
}
`,
	})
	parent := p["ScheduleTopLevel.jsx"]
	child := p["ScheduleGrid.jsx"]
	ledger := []graph.UnresolvedRef{{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"}}
	files := map[string][]string{"svc": {parent, child}}

	nodes, _, out, _ := LinkJSPropTransport(propTransportNodes(parent, child), ledger, files, nil)
	if len(nodes) != 1 || nodes[0].Meta["url"] != "/api/schedules/*/publish" {
		t.Fatalf("nodes = %+v, ledger = %+v", nodes, out)
	}
}

// The URL argument is not a parameter of the wrapper — that is UB.2 / UL
// territory and this pass must leave the row untouched.
func TestLinkJSPropTransport_SkipsNonParamURL(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"Widget.jsx": `export default class Widget extends React.Component {
  save = () => {
    const { transport } = this.props;
    const url = "/api/widgets";
    transport.ajax("Saving...", { url, method: "POST" });
  };
}
`,
	})
	f := p["Widget.jsx"]
	ledger := []graph.UnresolvedRef{{Service: "svc", File: f, Line: 5, Kind: "prop_client_dynamic_url"}}
	files := map[string][]string{"svc": {f}}

	nodes, _, out, retract := LinkJSPropTransport(
		[]graph.Node{{ID: "svc:w:class:Widget", Type: graph.NodeTypeClass, Label: "Widget", Service: "svc", File: f, Line: 1, Language: "javascript"}},
		ledger, files, nil)
	if len(nodes) != 0 || len(out) != 0 || len(retract) != 0 {
		t.Fatalf("expected no action, got nodes=%+v out=%+v retract=%v", nodes, out, retract)
	}
}
