package pipeline

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier RC.3 (docs/js-declarative-composition-cluster-plan.md) — ports
// internal/linker/js_prop_urls_test.go / js_prop_transport_test.go /
// js_prop_urls_schema_test.go's fixtures verbatim through pipeline.Run,
// replacing direct calls to the retired LinkJSPropURLs/LinkJSPropTransport.

const jpxWhereUsedConsumer = `export default class WhereUsed extends React.Component {
  load = () => {
    const { ajaxStatus, dataURL } = this.props;
    ajaxStatus.get("Loading...", dataURL);
  };
}
`

const jpxGridParent = `export default class ScheduleTopLevel extends React.Component {
  postToServer = (transport, data, updateURL) => {
    return transport.ajax("Saving...", { url: updateURL, data, method: "PUT" });
  };
  render() {
    return <ScheduleGrid postToServer={this.postToServer} />;
  }
}
`

func jpxWriteFixture(t *testing.T, files map[string]string) (map[string]string, []string) {
	t.Helper()
	dir := t.TempDir()
	paths := make(map[string]string, len(files))
	var list []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
		list = append(list, p)
	}
	return paths, list
}

func jpxPropURLConsumerNodes(consumerAbs string) []graph.Node {
	return []graph.Node{
		{ID: "svc:c:class:WhereUsed:1", Type: graph.NodeTypeClass, Label: "WhereUsed",
			Service: "svc", File: consumerAbs, Line: 1, Language: "javascript"},
		{ID: "svc:c:method:load:2", Type: graph.NodeTypeMethod, Label: "load",
			Service: "svc", File: consumerAbs, Line: 2, Language: "javascript"},
	}
}

func jpxPropTransportNodes(parentAbs, childAbs string) []graph.Node {
	return []graph.Node{
		{ID: "svc:p:class:ScheduleTopLevel:1", Type: graph.NodeTypeClass, Label: "ScheduleTopLevel",
			Service: "svc", File: parentAbs, Line: 1, Language: "javascript"},
		{ID: "svc:p:method:postToServer:2", Type: graph.NodeTypeMethod, Label: "postToServer",
			Service: "svc", File: parentAbs, Line: 2, Language: "javascript"},
		{ID: "svc:c:class:ScheduleGrid:1", Type: graph.NodeTypeClass, Label: "ScheduleGrid",
			Service: "svc", File: childAbs, Line: 1, Language: "javascript"},
	}
}

func jpxRun(t *testing.T, nodes []graph.Node, ledger []graph.UnresolvedRef, files []string) Result {
	t.Helper()
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	fw := reg.ByName("js_prop_crossings")
	if fw == nil {
		t.Fatal("js_prop_crossings framework not embedded")
	}
	res, err := Run([]*Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: files, Unresolved: ledger})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func jpxRetracted(res Result, svc, file string, line int) bool {
	key := svc + "\x00" + file + "\x00" + strconv.Itoa(line)
	for _, r := range res.Resolved {
		if r == key {
			return true
		}
	}
	return false
}

func jpxURLSet(nodes []graph.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.Meta["url"])
	}
	sort.Strings(out)
	return out
}

func TestJPX_PropURL_WorkedExample(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"WhereUsed.jsx": jpxWhereUsedConsumer,
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
	consumer := fileNamed(files, "WhereUsed.jsx")
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url", Name: "(dynamic)"},
	}

	res := jpxRun(t, jpxPropURLConsumerNodes(consumer), ledger, files)

	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	got := jpxURLSet(res.Nodes)
	want := []string{"/api/gadgets/*/usage", "/api/widget_groups/*/usage", "/api/widgets/*/usage"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("urls = %v, want %v", got, want)
	}
	for _, n := range res.Nodes {
		if n.Meta["method"] != "GET" || n.Meta["prop"] != "dataURL" || n.Meta["spa"] != "prop_url" {
			t.Errorf("bad meta: %+v", n.Meta)
		}
		if n.Meta["producer"] == "" {
			t.Errorf("no producer provenance: %+v", n.Meta)
		}
	}
	if !jpxRetracted(res, "svc", consumer, 4) {
		t.Errorf("site not retracted: %v", res.Resolved)
	}
	if len(res.Edges) != 3 {
		t.Errorf("want 3 calls edges, got %d", len(res.Edges))
	}
}

func fileNamed(files []string, base string) string {
	for _, f := range files {
		if filepath.Base(f) == base {
			return f
		}
	}
	return ""
}

// The anti-abstain test: an unresolvable producer must not suppress its
// siblings — the three that resolve still resolve, and the fourth ledgers.
func TestJPX_PropURL_UnresolvableProducerDoesNotAbstain(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"WhereUsed.jsx": jpxWhereUsedConsumer,
		"WidgetPane.jsx": `export function WidgetPane({ id }) {
  return <WhereUsed dataURL={` + "`/api/widgets/${id}/usage`" + `} />;
}
`,
		"SchemaPane.jsx": `export function SchemaPane({ schema }) {
  return <WhereUsed dataURL={schema.usage_url} />;
}
`,
	})
	consumer := fileNamed(files, "WhereUsed.jsx")
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"},
	}

	res := jpxRun(t, jpxPropURLConsumerNodes(consumer), ledger, files)

	if len(res.Nodes) != 1 || res.Nodes[0].Meta["url"] != "/api/widgets/*/usage" {
		t.Fatalf("nodes = %v", jpxURLSet(res.Nodes))
	}
	if !jpxRetracted(res, "svc", consumer, 4) {
		t.Errorf("site should retract once a URL resolved")
	}
	var un *graph.UnresolvedRef
	for i := range res.Unresolved {
		if res.Unresolved[i].Kind == "prop_url_unresolved" {
			un = &res.Unresolved[i]
		}
	}
	if un == nil || un.Targets != "member_expression" {
		t.Fatalf("want one prop_url_unresolved(member_expression), got %+v", res.Unresolved)
	}
}

// The join is on component label, not prop name alone: a producer for one
// component must not leak into another that happens to use the same prop name.
func TestJPX_PropURL_ComponentCollision(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
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
	a := fileNamed(files, "AModal.jsx")
	b := fileNamed(files, "BModal.jsx")
	nodes := []graph.Node{
		{ID: "svc:a:class:AModal", Type: graph.NodeTypeClass, Label: "AModal", Service: "svc", File: a, Line: 1, Language: "javascript"},
		{ID: "svc:b:class:BModal", Type: graph.NodeTypeClass, Label: "BModal", Service: "svc", File: b, Line: 1, Language: "javascript"},
	}
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: a, Line: 4, Kind: "prop_client_dynamic_url"},
		{Service: "svc", File: b, Line: 4, Kind: "prop_client_dynamic_url"},
	}

	res := jpxRun(t, nodes, ledger, files)

	if len(res.Nodes) != 1 || res.Nodes[0].File != a || res.Nodes[0].Meta["url"] != "/api/widgets" {
		t.Fatalf("nodes = %+v", res.Nodes)
	}
	if jpxRetracted(res, "svc", b, 4) {
		t.Errorf("BModal must not resolve from AModal's producer")
	}
}

// jpxTestMaxPropURLFanout mirrors internal/factpipe's unexported
// jpxMaxPropURLFanout (24) — this test package cannot see it.
const jpxTestMaxPropURLFanout = 24

func TestJPX_PropURL_HighFanoutLedgers(t *testing.T) {
	t.Parallel()
	fixture := map[string]string{"WhereUsed.jsx": jpxWhereUsedConsumer}
	var producer string
	for i := 0; i < jpxTestMaxPropURLFanout+1; i++ {
		producer += "  <WhereUsed dataURL={\"/api/r" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\"} />\n"
	}
	fixture["Producers.jsx"] = "export function Panel() {\n  return (<div>\n" + producer + "  </div>);\n}\n"
	_, files := jpxWriteFixture(t, fixture)
	consumer := fileNamed(files, "WhereUsed.jsx")
	ledger := []graph.UnresolvedRef{{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t, jpxPropURLConsumerNodes(consumer), ledger, files)
	if len(res.Nodes) != 0 {
		t.Fatalf("want no nodes above the cap, got %d", len(res.Nodes))
	}
	if len(res.Resolved) != 0 {
		t.Errorf("must not retract a capped site")
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "prop_url_high_fanout" {
		t.Fatalf("want one prop_url_high_fanout, got %+v", res.Unresolved)
	}
}

func TestJPX_PropTransport_WorkedExample(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": jpxGridParent,
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
	parent := fileNamed(files, "ScheduleTopLevel.jsx")
	child := fileNamed(files, "ScheduleGrid.jsx")
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url", Name: "(dynamic)"},
	}

	res := jpxRun(t, jpxPropTransportNodes(parent, child), ledger, files)

	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	got := map[string]bool{}
	for _, n := range res.Nodes {
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
	if !jpxRetracted(res, "svc", parent, 3) {
		t.Errorf("site not retracted: %v", res.Resolved)
	}
	if len(res.Edges) != 2 {
		t.Errorf("want 2 calls edges, got %d", len(res.Edges))
	}
}

// A child call site whose URL argument is itself unreadable must ledger, and
// it must not suppress the sibling that resolved.
func TestJPX_PropTransport_UnresolvableArgLedgers(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": jpxGridParent,
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
	parent := fileNamed(files, "ScheduleTopLevel.jsx")
	child := fileNamed(files, "ScheduleGrid.jsx")
	ledger := []graph.UnresolvedRef{{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t, jpxPropTransportNodes(parent, child), ledger, files)

	if len(res.Nodes) != 1 || res.Nodes[0].Meta["url"] != "/api/schedules/save" {
		t.Fatalf("nodes = %+v", res.Nodes)
	}
	if !jpxRetracted(res, "svc", parent, 3) {
		t.Errorf("site should retract once a URL resolved")
	}
	var un *graph.UnresolvedRef
	for i := range res.Unresolved {
		if res.Unresolved[i].Kind == "prop_transport_unresolved" {
			un = &res.Unresolved[i]
		}
	}
	if un == nil || un.Name != "member_expression" || un.Targets != "postToServer" {
		t.Fatalf("want one prop_transport_unresolved(member_expression), got %+v", res.Unresolved)
	}
}

// The URL argument is a local binding in the enclosing function, not a
// literal at the call site — resolved through Tier UL's walk.
func TestJPX_PropTransport_LocalBindingArg(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"ScheduleTopLevel.jsx": jpxGridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    const endpoint = ` + "`/api/schedules/${this.props.id}/publish`" + `;
    this.props.postToServer(this.props.transport, {}, endpoint);
  };
}
`,
	})
	parent := fileNamed(files, "ScheduleTopLevel.jsx")
	child := fileNamed(files, "ScheduleGrid.jsx")
	ledger := []graph.UnresolvedRef{{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t, jpxPropTransportNodes(parent, child), ledger, files)
	if len(res.Nodes) != 1 || res.Nodes[0].Meta["url"] != "/api/schedules/*/publish" {
		t.Fatalf("nodes = %+v, unresolved = %+v", res.Nodes, res.Unresolved)
	}
}

// The URL argument is not a parameter of the wrapper — that is UB.2 / UL
// territory and this pass must leave the row untouched.
func TestJPX_PropTransport_SkipsNonParamURL(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"Widget.jsx": `export default class Widget extends React.Component {
  save = () => {
    const { transport } = this.props;
    const url = "/api/widgets";
    transport.ajax("Saving...", { url, method: "POST" });
  };
}
`,
	})
	f := fileNamed(files, "Widget.jsx")
	ledger := []graph.UnresolvedRef{{Service: "svc", File: f, Line: 5, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t,
		[]graph.Node{{ID: "svc:w:class:Widget", Type: graph.NodeTypeClass, Label: "Widget", Service: "svc", File: f, Line: 1, Language: "javascript"}},
		ledger, files)
	if len(res.Nodes) != 0 || len(res.Unresolved) != 0 || len(res.Resolved) != 0 {
		t.Fatalf("expected no action, got nodes=%+v unresolved=%+v resolved=%v", res.Nodes, res.Unresolved, res.Resolved)
	}
}

// TestJPX_PropURL_SchemaProducerResolves is MS.3's URL-crosses-a-prop shape:
// the producer expression the value engine crosses to is itself a read of a
// discovered data asset — a member expression, which javascript.yaml
// correctly declares opaque. jpxSchemaProducerFallback recovers it through
// the same generic schemaurl.Resolver the non-crossing schema passes use
// (hub_schema_url_link.go's sulBuildResolver), driven by a REAL discovered
// asset on disk (Tier MS.0), same convention as
// schema_url_link_test.go's TestSUL_SchemaSweep_ResolvesWorkedExample.
func TestJPX_PropURL_SchemaProducerResolves(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "detailUrl": "/api/widgets/:id/detail" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	_, files := jpxWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"WhereUsed.jsx":         jpxWhereUsedConsumer,
		"ConfigPane.jsx": `export function ConfigPane({ id, resources }) {
  return <WhereUsed dataURL={resources.widget.detailUrl} />;
}
`,
	})
	consumer := fileNamed(files, "WhereUsed.jsx")
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"},
	}
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/detail"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	nodes := append(append([]graph.Node{}, handlerNodes...), jpxPropURLConsumerNodes(consumer)...)

	res := jpxRun(t, nodes, ledger, files)

	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want 1", res.Nodes)
	}
	n := res.Nodes[0]
	if n.Meta["url"] != "/api/widgets/*/detail" {
		t.Errorf("url = %q", n.Meta["url"])
	}
	if n.Meta["schema_entity"] != "widget" || n.Meta["schema_key"] != "detailUrl" {
		t.Errorf("schema provenance = %+v", n.Meta)
	}
	if !jpxRetracted(res, "svc", consumer, 4) {
		t.Errorf("site not retracted: %v", res.Resolved)
	}
}

// The mirror pass: the URL argument the child hands back to a
// prop-injected transport is itself a schema-asset read.
func TestJPX_PropTransport_SchemaProducerResolves(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "detailUrl": "/api/widgets/:id/detail" },
    "gadget":  { "endpoint": "/api/gadgets", "saveUrl": "/api/gadgets/:id/save" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	_, files := jpxWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"ScheduleTopLevel.jsx":  jpxGridParent,
		"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.transport, {}, resources.gadget.saveUrl);
  };
}
`,
	})
	parent := fileNamed(files, "ScheduleTopLevel.jsx")
	child := fileNamed(files, "ScheduleGrid.jsx")
	ledger := []graph.UnresolvedRef{
		{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"},
	}
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/detail"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/save"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	nodes := append(append([]graph.Node{}, handlerNodes...), jpxPropTransportNodes(parent, child)...)

	res := jpxRun(t, nodes, ledger, files)

	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Nodes) != 1 || res.Nodes[0].Meta["url"] != "/api/gadgets/*/save" {
		t.Fatalf("nodes = %+v", res.Nodes)
	}
	if res.Nodes[0].Meta["schema_entity"] != "gadget" {
		t.Errorf("schema provenance = %+v", res.Nodes[0].Meta)
	}
	if !jpxRetracted(res, "svc", parent, 3) {
		t.Errorf("site not retracted: %v", res.Resolved)
	}
}

// ── ported from internal/linker/vg_crossing_test.go (Tier VG.4 acceptance
// fixtures) — the disagreement/collision/two-hop shapes, not covered by the
// worked-example tests above. ──────────────────────────────────────────────

func jpxClassNode(svc, label, file string) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":class:" + label, Type: graph.NodeTypeClass,
		Label: label, Service: svc, File: file, Line: 1, Language: "javascript",
	}
}

// One modal, three render sites, three create URLs — the crossing pass must
// not abstain the way LinkReactPropURLs does: the disagreement is the answer.
func TestJPX_PropURL_ThreeSitesMintsOneNodePerURL(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"CCreateModal.jsx": `export default class CCreateModal extends React.Component {
  save = () => {
    const { ajaxStatus, createUrl } = this.props;
    ajaxStatus.post("Saving...", { url: createUrl, method: "POST" });
  };
}
`,
		"Forms.jsx":   `export function Forms() { return <CCreateModal createUrl="/api/forms" />; }`,
		"Studies.jsx": `export function Studies() { return <CCreateModal createUrl="/api/studies" />; }`,
		"Sites.jsx":   `export function Sites() { return <CCreateModal createUrl="/api/sites" />; }`,
	})
	consumer := fileNamed(files, "CCreateModal.jsx")
	nodes := []graph.Node{jpxClassNode("svc", "CCreateModal", consumer)}
	ledger := []graph.UnresolvedRef{{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t, nodes, ledger, files)

	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Nodes) != 3 {
		t.Fatalf("want three clients, one per render site, got %d: %v", len(res.Nodes), jpxURLSet(res.Nodes))
	}
	urls := jpxURLSet(res.Nodes)
	if urls[0] != "/api/forms" || urls[1] != "/api/sites" || urls[2] != "/api/studies" {
		t.Fatalf("urls = %v", urls)
	}
	seen := map[string]bool{}
	for _, n := range res.Nodes {
		if seen[n.ID] {
			t.Errorf("duplicate node id %s", n.ID)
		}
		seen[n.ID] = true
		if n.Meta["producer"] == "" {
			t.Errorf("%s has no producer provenance: %+v", n.ID, n.Meta)
		}
	}
	if !jpxRetracted(res, "svc", consumer, 4) {
		t.Errorf("the site resolved and must retract exactly once: %v", res.Resolved)
	}
}

// Provenance (VG.4 acceptance): the rule names the crossing that resolved
// the node, not just the language.
func TestJPX_ProvenanceNamesTheCrossing(t *testing.T) {
	t.Parallel()
	t.Run("prop_url", func(t *testing.T) {
		t.Parallel()
		_, files := jpxWriteFixture(t, map[string]string{
			"WhereUsed.jsx":  jpxWhereUsedConsumer,
			"WidgetPane.jsx": `export function WidgetPane({ ajaxStatus, id }) { return <WhereUsed ajaxStatus={ajaxStatus} dataURL="/api/widgets" />; }`,
		})
		consumer := fileNamed(files, "WhereUsed.jsx")
		ledger := []graph.UnresolvedRef{{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"}}
		res := jpxRun(t, jpxPropURLConsumerNodes(consumer), ledger, files)
		if len(res.Nodes) == 0 {
			t.Fatal("minted nothing")
		}
		for _, n := range res.Nodes {
			if n.Meta["vg_layer"] != "L2" || n.Meta["vg_rule"] != "valuegraph/javascript#jsx_attribute" {
				t.Errorf("node %s has layer=%q rule=%q", n.ID, n.Meta["vg_layer"], n.Meta["vg_rule"])
			}
		}
	})
	t.Run("prop_transport", func(t *testing.T) {
		t.Parallel()
		_, files := jpxWriteFixture(t, map[string]string{
			"ScheduleTopLevel.jsx": jpxGridParent,
			"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => { this.props.postToServer(this.props.transport, {}, "/api/schedules/reset"); };
}
`,
		})
		parent := fileNamed(files, "ScheduleTopLevel.jsx")
		child := fileNamed(files, "ScheduleGrid.jsx")
		ledger := []graph.UnresolvedRef{{Service: "svc", File: parent, Line: 3, Kind: "prop_client_dynamic_url"}}
		res := jpxRun(t, jpxPropTransportNodes(parent, child), ledger, files)
		if len(res.Nodes) == 0 {
			t.Fatal("minted nothing")
		}
		for _, n := range res.Nodes {
			if n.Meta["vg_layer"] != "L2" || n.Meta["vg_rule"] != "valuegraph/javascript#jsx_attribute_callback" {
				t.Errorf("node %s has layer=%q rule=%q", n.ID, n.Meta["vg_layer"], n.Meta["vg_rule"])
			}
		}
	})
}

// The two-hop crossing: the producer's value is itself a prop of the
// producing component. Neither shipped pass handled this — UB.2 used to
// report the producer as an unresolved member_expression. The engine follows
// it, because a crossed binding is a binding like any other and nothing in
// the traversal says "once only".
func TestJPX_PropURL_TwoHopsCrossing(t *testing.T) {
	t.Parallel()
	_, files := jpxWriteFixture(t, map[string]string{
		"Inner.jsx": `export default class Inner extends React.Component {
  save = () => {
    const { ajaxStatus, createUrl } = this.props;
    ajaxStatus.post("Saving...", { url: createUrl, method: "POST" });
  };
}
`,
		"Middle.jsx": `export default class Middle extends React.Component {
  render() { return <Inner createUrl={this.props.createUrl} />; }
}
`,
		"Outer.jsx": `export function Outer() { return <Middle createUrl="/api/deep" />; }`,
	})
	consumer := fileNamed(files, "Inner.jsx")
	nodes := []graph.Node{
		jpxClassNode("svc", "Inner", consumer),
		jpxClassNode("svc", "Middle", fileNamed(files, "Middle.jsx")),
		jpxClassNode("svc", "Outer", fileNamed(files, "Outer.jsx")),
	}
	ledger := []graph.UnresolvedRef{{Service: "svc", File: consumer, Line: 4, Kind: "prop_client_dynamic_url"}}

	res := jpxRun(t, nodes, ledger, files)
	if len(res.Nodes) != 1 || res.Nodes[0].Meta["url"] != "/api/deep" {
		t.Fatalf("two-hop crossing = %v, want [/api/deep]", jpxURLSet(res.Nodes))
	}
	if !jpxRetracted(res, "svc", consumer, 4) {
		t.Errorf("a resolved site retracts its blind-spot row: %v", res.Resolved)
	}
}
