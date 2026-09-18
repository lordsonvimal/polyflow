package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
)

// TestLinkJSPropURLs_SchemaProducerResolves is MS.3's URL-crosses-a-prop
// shape: the producer expression the value engine crosses to is itself a
// read of a discovered data asset (`config.detailUrl`, with `config` pinned
// to an entity earlier in the same producer function) — a member expression,
// which javascript.yaml correctly declares opaque. vgSchemaProducerFallback
// recovers it through the same generic schemaurl.Resolver the non-crossing
// schema passes already use, rather than leaving it a permanent
// prop_url_unresolved(member_expression) row.
func TestLinkJSPropURLs_SchemaProducerResolves(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"WhereUsed.jsx": whereUsedConsumer,
		"ConfigPane.jsx": `export function ConfigPane({ id }) {
  const config = appConfig.entities.widget;
  return <WhereUsed dataURL={config.detailUrl} />;
}
`,
	})
	consumer := p["WhereUsed.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"},
	}
	files := map[string][]string{"svc": {consumer, p["ConfigPane.jsx"]}}

	tbl := &schemaurl.Table{
		File: "assets/app-config.json",
		ByEntity: map[string]map[string]schemaurl.Entry{
			"widget": {
				"detailUrl": {
					Raw:  "/api/widgets/${id}/detail",
					Path: "/api/widgets/*/detail",
					Key:  "detailUrl",
				},
			},
		},
	}
	resolver := schemaurl.NewResolver(map[string]*schemaurl.Table{"svc": tbl}, files)

	nodes, _, out, retract := LinkJSPropURLs(propURLConsumerNodes(consumer), ledger, files, resolver)

	if len(out) != 0 {
		t.Fatalf("unexpected ledger: %+v", out)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v, want 1", nodes)
	}
	n := nodes[0]
	if n.Meta["url"] != "/api/widgets/*/detail" {
		t.Errorf("url = %q", n.Meta["url"])
	}
	if n.Meta["url_origin"] != "schema_asset" {
		t.Errorf("url_origin = %q, want schema_asset", n.Meta["url_origin"])
	}
	if n.Meta["schema_entity"] != "widget" || n.Meta["schema_key"] != "detailUrl" {
		t.Errorf("schema provenance = %+v", n.Meta)
	}
	if n.Meta["schema_file"] != "assets/app-config.json" {
		t.Errorf("schema_file = %q", n.Meta["schema_file"])
	}
	if !retract[PropURLRetractKey(consumer, 4)] {
		t.Errorf("site not retracted: %v", retract)
	}
}

// The mirror pass: the URL argument the child hands back to a prop-injected
// transport is itself a schema-asset read.
func TestLinkJSPropTransport_SchemaProducerResolves(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": gridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    const config = appConfig.entities.gadget;
    this.props.postToServer(this.props.transport, {}, config.saveUrl);
  };
}
`,
	})
	parent := p["ScheduleTopLevel.jsx"]
	child := p["ScheduleGrid.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"},
	}
	files := map[string][]string{"svc": {parent, child}}

	tbl := &schemaurl.Table{
		File: "assets/app-config.json",
		ByEntity: map[string]map[string]schemaurl.Entry{
			"gadget": {
				"saveUrl": {
					Raw:  "/api/gadgets/${id}",
					Path: "/api/gadgets/*",
					Key:  "saveUrl",
				},
			},
		},
	}
	resolver := schemaurl.NewResolver(map[string]*schemaurl.Table{"svc": tbl}, files)

	nodes, _, out, retract := LinkJSPropTransport(propTransportNodes(parent, child), ledger, files, resolver)

	if len(out) != 0 {
		t.Fatalf("unexpected ledger: %+v", out)
	}
	if len(nodes) != 1 || nodes[0].Meta["url"] != "/api/gadgets/*" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if nodes[0].Meta["url_origin"] != "schema_asset" || nodes[0].Meta["schema_entity"] != "gadget" {
		t.Errorf("schema provenance = %+v", nodes[0].Meta)
	}
	if !retract[PropURLRetractKey(parent, 3)] {
		t.Errorf("site not retracted: %v", retract)
	}
}

// A nil resolver (the pipeline's state before schema_url_tables runs, and
// every pre-MS.3 caller) must behave exactly as before: ledger, don't panic.
func TestLinkJSPropURLs_NilResolverStillLedgers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"WhereUsed.jsx": whereUsedConsumer,
		"ConfigPane.jsx": `export function ConfigPane({ config }) {
  return <WhereUsed dataURL={config.detailUrl} />;
}
`,
	})
	consumer := p["WhereUsed.jsx"]
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"},
	}
	files := map[string][]string{"svc": {consumer, p["ConfigPane.jsx"]}}

	nodes, _, out, _ := LinkJSPropURLs(propURLConsumerNodes(consumer), ledger, files, nil)

	if len(nodes) != 0 {
		t.Fatalf("want no nodes with a nil resolver, got %+v", nodes)
	}
	if len(out) != 1 || out[0].Kind != ledgerPropURLUnresolved || out[0].Targets != "member_expression" {
		t.Fatalf("want one prop_url_unresolved(member_expression), got %+v", out)
	}
}
