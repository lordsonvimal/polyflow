package linker

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// whereUsedConsumer is a child component that reads its endpoint from the
// `dataURL` prop and issues the request through a prop-injected transport.
const whereUsedConsumer = `export default class WhereUsed extends React.Component {
  load = () => {
    const { ajaxStatus, dataURL } = this.props;
    ajaxStatus.get("Loading...", dataURL);
  };
}
`

func propURLConsumerNodes(consumerAbs string) []graph.Node {
	return []graph.Node{
		{ID: "svc:c:class:WhereUsed:1", Type: graph.NodeTypeClass, Label: "WhereUsed",
			Service: "svc", File: consumerAbs, Line: 1, Language: "javascript"},
		{ID: "svc:c:method:load:2", Type: graph.NodeTypeMethod, Label: "load",
			Service: "svc", File: consumerAbs, Line: 2, Language: "javascript"},
	}
}

func urlSet(nodes []graph.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Meta["url"])
	}
	sort.Strings(out)
	return out
}

func TestLinkJSPropURLs_WorkedExample(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"WhereUsed.jsx": whereUsedConsumer,
		"WidgetPane.jsx": `export function WidgetPane({ ajaxStatus, id, kind }) {
  let url = ` + "`/api/widgets/${id}/usage`" + `;
  if (kind === "group") {
    url = ` + "`/api/widget_groups/${id}/usage`" + `;
  }
  return <WhereUsed ajaxStatus={ajaxStatus} dataURL={url} />;
}
`,
		"GadgetPane.jsx": `export const GadgetPane = props => (
  <WhereUsed ajaxStatus={props.ajaxStatus} dataURL={` + "`/api/gadgets/${props.id}/usage`" + `} />
);
`,
	})
	consumer := p["WhereUsed.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url", Name: "(dynamic)"},
	}
	files := map[string][]string{"svc": {consumer, p["WidgetPane.jsx"], p["GadgetPane.jsx"]}}

	nodes, edges, out, retract := LinkJSPropURLs(propURLConsumerNodes(consumer), ledger, files, nil)

	if len(out) != 0 {
		t.Fatalf("unexpected ledger: %+v", out)
	}
	got := urlSet(nodes)
	want := []string{"/api/gadgets/*/usage", "/api/widget_groups/*/usage", "/api/widgets/*/usage"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("urls = %v, want %v", got, want)
	}
	for _, n := range nodes {
		if n.Meta["method"] != "GET" || n.Meta["prop"] != "dataURL" || n.Meta["spa"] != "prop_url" {
			t.Errorf("bad meta: %+v", n.Meta)
		}
		if n.Meta["producer"] == "" {
			t.Errorf("no producer provenance: %+v", n.Meta)
		}
	}
	if !retract[PropURLRetractKey(consumer, 4)] {
		t.Errorf("site not retracted: %v", retract)
	}
	if len(edges) != 3 {
		t.Errorf("want 3 calls edges, got %d", len(edges))
	}
}

// The anti-abstain test: an unresolvable producer must not suppress its
// siblings — the three that resolve still resolve, and the fourth ledgers.
func TestLinkJSPropURLs_UnresolvableProducerDoesNotAbstain(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"WhereUsed.jsx": whereUsedConsumer,
		"WidgetPane.jsx": `export function WidgetPane({ id }) {
  return <WhereUsed dataURL={` + "`/api/widgets/${id}/usage`" + `} />;
}
`,
		"SchemaPane.jsx": `export function SchemaPane({ schema }) {
  return <WhereUsed dataURL={schema.usage_url} />;
}
`,
	})
	consumer := p["WhereUsed.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"},
	}
	files := map[string][]string{"svc": {consumer, p["WidgetPane.jsx"], p["SchemaPane.jsx"]}}

	nodes, _, out, retract := LinkJSPropURLs(propURLConsumerNodes(consumer), ledger, files, nil)

	if len(nodes) != 1 || nodes[0].Meta["url"] != "/api/widgets/*/usage" {
		t.Fatalf("nodes = %v", urlSet(nodes))
	}
	if !retract[PropURLRetractKey(consumer, 4)] {
		t.Errorf("site should retract once a URL resolved")
	}
	var un *graph.UnresolvedRef
	for i := range out {
		if out[i].Kind == ledgerPropURLUnresolved {
			un = &out[i]
		}
	}
	if un == nil || un.Targets != "member_expression" {
		t.Fatalf("want one prop_url_unresolved(member_expression), got %+v", out)
	}
}

// The join is on component label, not prop name alone: a producer for one
// component must not leak into another that happens to use the same prop name.
func TestLinkJSPropURLs_ComponentCollision(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"AModal.jsx": `export default class AModal extends React.Component {
  save = () => {
    const { ajaxStatus, createUrl } = this.props;
    ajaxStatus.post("Saving...", { url: createUrl, method: "POST" });
  };
}
`,
		"BModal.jsx": `export default class BModal extends React.Component {
  save = () => {
    const { ajaxStatus, createUrl } = this.props;
    ajaxStatus.post("Saving...", { url: createUrl, method: "POST" });
  };
}
`,
		"Producers.jsx": `export function Panel() {
  return <AModal createUrl="/api/widgets" />;
}
`,
	})
	a := p["AModal.jsx"]
	b := p["BModal.jsx"]
	nodes := []graph.Node{
		{ID: "svc:a:class:AModal", Type: graph.NodeTypeClass, Label: "AModal", Service: "svc", File: a, Line: 1, Language: "javascript"},
		{ID: "svc:b:class:BModal", Type: graph.NodeTypeClass, Label: "BModal", Service: "svc", File: b, Line: 1, Language: "javascript"},
	}
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: a, Line: 4, Kind: "prop_client_dynamic_url"},
		{Service: "svc", File: b, Line: 4, Kind: "prop_client_dynamic_url"},
	}
	files := map[string][]string{"svc": {a, b, p["Producers.jsx"]}}

	got, _, _, retract := LinkJSPropURLs(nodes, ledger, files, nil)

	if len(got) != 1 || got[0].File != a || got[0].Meta["url"] != "/api/widgets" {
		t.Fatalf("nodes = %+v", got)
	}
	if retract[PropURLRetractKey(b, 4)] {
		t.Errorf("BModal must not resolve from AModal's producer")
	}
}

func TestLinkJSPropURLs_HighFanoutLedgers(t *testing.T) {
	t.Parallel()
	fixture := map[string]string{"WhereUsed.jsx": whereUsedConsumer}
	var producer string
	for i := 0; i < maxPropURLFanout+1; i++ {
		producer += "  <WhereUsed dataURL={\"/api/r" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\"} />\n"
	}
	fixture["Producers.jsx"] = "export function Panel() {\n  return (<div>\n" + producer + "  </div>);\n}\n"
	_, p := writeReduxFixture(t, fixture)
	consumer := p["WhereUsed.jsx"]
	ledger := []graph.UnresolvedRef{{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"}}
	files := map[string][]string{"svc": {consumer, p["Producers.jsx"]}}

	nodes, _, out, retract := LinkJSPropURLs(propURLConsumerNodes(consumer), ledger, files, nil)
	if len(nodes) != 0 {
		t.Fatalf("want no nodes above the cap, got %d", len(nodes))
	}
	if len(retract) != 0 {
		t.Errorf("must not retract a capped site")
	}
	if len(out) != 1 || out[0].Kind != ledgerPropURLHighFanout {
		t.Fatalf("want one prop_url_high_fanout, got %+v", out)
	}
}
