package linker

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

// Tier VG.4 acceptance (docs/js-value-graph-pilot-plan.md) was a differential
// against the UB.2/UB.3 walkers — zero LOST, zero CHANGED. VG.5 retired those
// walkers, so as in vg_differential_test.go the fixtures are now goldens: the
// crossing shapes, and the exact URLs each one resolves to.
//
// The corpus run is the plan owner's; this pins the shapes in process.

// vgPropCase is one fixture: the files, the component/function nodes the graph
// would already hold for them, and the prop_client_dynamic_url site Tier CW
// would have ledgered.
type vgPropCase struct {
	files    map[string]string
	consumer string // fixture key of the file holding the ledger site
	line     int
	nodes    func(p map[string]string) []graph.Node
}

// classNode is the node the parser mints for a component class; the crossing's
// owner index is built from exactly these.
func classNode(svc, label, file string) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":class:" + label, Type: graph.NodeTypeClass,
		Label: label, Service: svc, File: file, Line: 1, Language: "javascript",
	}
}

func methodNode(svc, label, file string, line int) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":method:" + label, Type: graph.NodeTypeMethod,
		Label: label, Service: svc, File: file, Line: line, Language: "javascript",
	}
}

var vgPropFixtures = map[string]vgPropCase{
	// UB.2's worked example: two producers, one of them resolved through a
	// local binding in the producing function.
	"prop-url-worked-example": {
		files: map[string]string{
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
		},
		consumer: "WhereUsed.jsx", line: 4,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "WhereUsed", p["WhereUsed.jsx"]),
				methodNode("svc", "load", p["WhereUsed.jsx"], 2),
				classNode("svc", "WidgetPane", p["WidgetPane.jsx"]),
				classNode("svc", "GadgetPane", p["GadgetPane.jsx"]),
			}
		},
	},

	// (a) The prop arrives through a destructured binding rather than a
	// qualified read — the commonest shape in the corpus, and the one a
	// name-based local walk sees as unbound.
	"prop-url-destructured": {
		files: map[string]string{
			"AModal.jsx": `export default class AModal extends React.Component {
  save = () => {
    const { ajaxStatus, createUrl } = this.props;
    ajaxStatus.post("Saving...", { url: createUrl, method: "POST" });
  };
}
`,
			"Panel.jsx": `export function Panel() {
  return <AModal createUrl="/api/widgets" />;
}
`,
		},
		consumer: "AModal.jsx", line: 4,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "AModal", p["AModal.jsx"]),
				classNode("svc", "Panel", p["Panel.jsx"]),
			}
		},
	},

	// (b) One modal, three render sites, three create URLs. UB.2 must not
	// abstain the way LinkReactPropURLs does — the disagreement is the answer.
	"prop-url-three-sites": {
		files: map[string]string{
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
		},
		consumer: "CCreateModal.jsx", line: 4,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "CCreateModal", p["CCreateModal.jsx"]),
				classNode("svc", "Forms", p["Forms.jsx"]),
				classNode("svc", "Studies", p["Studies.jsx"]),
				classNode("svc", "Sites", p["Sites.jsx"]),
			}
		},
	},

	// An unreadable producer ledgers on its own and does not suppress its
	// resolved sibling.
	"prop-url-unresolvable-sibling": {
		files: map[string]string{
			"WhereUsed.jsx": whereUsedConsumer,
			"WidgetPane.jsx": `export function WidgetPane({ id }) {
  return <WhereUsed dataURL={` + "`/api/widgets/${id}/usage`" + `} />;
}
`,
			"SchemaPane.jsx": `export function SchemaPane({ schema }) {
  return <WhereUsed dataURL={schema.usage_url} />;
}
`,
		},
		consumer: "WhereUsed.jsx", line: 4,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "WhereUsed", p["WhereUsed.jsx"]),
				classNode("svc", "WidgetPane", p["WidgetPane.jsx"]),
				classNode("svc", "SchemaPane", p["SchemaPane.jsx"]),
			}
		},
	},

	// UB.3: the transport crosses down, the URL comes back up into its
	// parameter from two different call sites in the child.
	"prop-transport-worked-example": {
		files: map[string]string{
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
		},
		consumer: "ScheduleTopLevel.jsx", line: 3,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "ScheduleTopLevel", p["ScheduleTopLevel.jsx"]),
				methodNode("svc", "postToServer", p["ScheduleTopLevel.jsx"], 2),
				classNode("svc", "ScheduleGrid", p["ScheduleGrid.jsx"]),
			}
		},
	},

	// A child call site whose URL argument is unreadable ledgers without
	// suppressing the sibling that resolved.
	"prop-transport-unresolvable-arg": {
		files: map[string]string{
			"ScheduleTopLevel.jsx": gridParent,
			"ScheduleGrid.jsx": `export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.transport, this.state.data, this.props.saveUrl);
  };
  reset = () => {
    this.props.postToServer(this.props.transport, {}, "/api/schedules/reset");
  };
}
`,
		},
		consumer: "ScheduleTopLevel.jsx", line: 3,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "ScheduleTopLevel", p["ScheduleTopLevel.jsx"]),
				methodNode("svc", "postToServer", p["ScheduleTopLevel.jsx"], 2),
				classNode("svc", "ScheduleGrid", p["ScheduleGrid.jsx"]),
			}
		},
	},
}

// vgPropSetup writes a fixture once. Both paths then run over the same paths —
// the node IDs embed the file, so a per-run temp directory would make every row
// LOST and GAINED at once and the comparison meaningless.
func vgPropSetup(t *testing.T, c vgPropCase) (nodes []graph.Node, ledger []graph.UnresolvedRef, files map[string][]string) {
	t.Helper()
	full := map[string]string{"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC}
	for k, v := range c.files {
		full[k] = v
	}
	_, p := writeReduxFixture(t, full)

	var abs []string
	for name := range full {
		abs = append(abs, p[name])
	}
	sort.Strings(abs)

	ledger = []graph.UnresolvedRef{{
		Service: "svc", File: p[c.consumer], Line: c.line,
		Kind: "prop_client_dynamic_url", Name: "(dynamic)",
	}}
	return c.nodes(p), ledger, map[string][]string{"svc": abs}
}

// vgCaptureProps runs both crossing passes exactly as the pipeline does — UB.3
// consumes the ledger UB.2 has already thinned — and records what they minted
// plus the blind-spot rows that survived.
func vgCaptureProps(nodes []graph.Node, ledger []graph.UnresolvedRef, files map[string][]string) *vgbaseline.Baseline {
	urlNodes, _, _, retractA := LinkJSPropURLs(nodes, ledger, files)

	var thinned []graph.UnresolvedRef
	for _, r := range ledger {
		if !retractA[PropURLRetractKey(r.File, r.Line)] {
			thinned = append(thinned, r)
		}
	}
	transportNodes, _, _, retractB := LinkJSPropTransport(nodes, thinned, files)

	b := &vgbaseline.Baseline{Corpus: "fixture"}
	for _, n := range append(append([]graph.Node(nil), urlNodes...), transportNodes...) {
		b.Clients = append(b.Clients, vgbaseline.ClientRecord{
			ID: n.ID, Service: n.Service, File: n.File, Line: n.Line,
			Path: n.Meta["path"], Method: n.Meta["method"], URL: n.Meta["url"],
		})
	}
	for _, r := range thinned {
		if retractB[PropURLRetractKey(r.File, r.Line)] {
			continue
		}
		b.Ledger = append(b.Ledger, vgbaseline.LedgerRecord{
			Key: PropURLRetractKey(r.File, r.Line), Service: r.Service,
			File: r.File, Line: r.Line, Name: r.Name, Kind: r.Kind,
		})
	}
	b.Sort()
	return b
}

// vgPropWant is what each crossing fixture resolves to, in vgbaseline.Sort
// order, plus the blind-spot rows that survive both passes.
var vgPropWant = map[string]struct {
	urls   []string
	ledger int
}{
	"prop-url-worked-example":         {urls: []string{"/api/gadgets/*/usage", "/api/widget_groups/*/usage", "/api/widgets/*/usage"}},
	"prop-url-destructured":           {urls: []string{"/api/widgets"}},
	"prop-url-three-sites":            {urls: []string{"/api/forms", "/api/sites", "/api/studies"}},
	"prop-url-unresolvable-sibling":   {urls: []string{"/api/widgets/*/usage"}},
	"prop-transport-worked-example":   {urls: []string{"/api/schedules/*", "/api/schedules/reset"}},
	"prop-transport-unresolvable-arg": {urls: []string{"/api/schedules/reset"}},
}

func TestVGPropCrossingFixtures(t *testing.T) {
	names := make([]string, 0, len(vgPropFixtures))
	for name := range vgPropFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		c := vgPropFixtures[name]
		t.Run(name, func(t *testing.T) {
			want, ok := vgPropWant[name]
			if !ok {
				t.Fatalf("fixture %q has no expectation — add one rather than deleting the fixture", name)
			}
			got := vgCaptureProps(vgPropSetup(t, c))

			var urls []string
			for _, cl := range got.Clients {
				urls = append(urls, cl.URL)
			}
			if !reflect.DeepEqual(urls, want.urls) && !(len(urls) == 0 && len(want.urls) == 0) {
				t.Errorf("urls = %v, want %v", urls, want.urls)
			}
			if len(got.Ledger) != want.ledger {
				t.Errorf("ledger rows = %d, want %d: %+v", len(got.Ledger), want.ledger, got.Ledger)
			}
		})
	}
}

// One modal rendered from three sites is three http_client nodes, each reaching
// exactly one handler — not one node with three edges, and not abstention. The
// differential cannot see this on its own: it compares two runs that could both
// be wrong in the same way.
func TestVGPropCrossingMintsOneNodePerURL(t *testing.T) {
	nodes, ledger, files := vgPropSetup(t, vgPropFixtures["prop-url-three-sites"])
	got, _, out, retract := LinkJSPropURLs(nodes, ledger, files)

	if len(out) != 0 {
		t.Fatalf("unexpected ledger: %+v", out)
	}
	if len(got) != 3 {
		t.Fatalf("want three clients, one per render site, got %d: %v", len(got), urlSet(got))
	}
	urls := urlSet(got)
	if urls[0] != "/api/forms" || urls[1] != "/api/sites" || urls[2] != "/api/studies" {
		t.Fatalf("urls = %v", urls)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n.ID] {
			t.Errorf("duplicate node id %s", n.ID)
		}
		seen[n.ID] = true
		if n.Meta["producer"] == "" {
			t.Errorf("%s has no producer provenance: %+v", n.ID, n.Meta)
		}
	}
	if len(retract) != 1 {
		t.Errorf("the site resolved and must retract exactly once: %v", retract)
	}
}

// Provenance (VG.4 acceptance): the rule names the crossing that resolved the
// node, not just the language. Which of the two mirror rules produced a suspect
// edge is the whole reason they were worth distinguishing.
func TestVGPropCrossingProvenanceNamesTheCrossing(t *testing.T) {
	for name, want := range map[string]string{
		"prop-url-worked-example":       vgPropURLRule,
		"prop-transport-worked-example": vgPropTransportRule,
	} {
		nodes, ledger, files := vgPropSetup(t, vgPropFixtures[name])
		minted, _, _, _ := LinkJSPropURLs(nodes, ledger, files)
		if want == vgPropTransportRule {
			minted, _, _, _ = LinkJSPropTransport(nodes, ledger, files)
		}
		if len(minted) == 0 {
			t.Fatalf("%s minted nothing", name)
		}
		for _, n := range minted {
			if n.Meta["vg_layer"] != vgLayer || n.Meta["vg_rule"] != want {
				t.Errorf("%s: node %s has layer=%q rule=%q, want %q/%q",
					name, n.ID, n.Meta["vg_layer"], n.Meta["vg_rule"], vgLayer, want)
			}
		}
	}
}

// (c) The two-hop crossing: the producer's value is itself a prop of the
// producing component. Neither shipped pass handles this — UB.2 reports the
// producer as an unresolved member_expression. The engine follows it, because a
// crossed binding is a binding like any other and nothing in the traversal says
// "once only".
//
// Asserted deliberately, per the plan: the answer is "resolved", and it is a
// GAINED row on any corpus that contains the shape.
func TestVGPropCrossingTwoHops(t *testing.T) {
	c := vgPropCase{
		files: map[string]string{
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
		},
		consumer: "Inner.jsx", line: 4,
		nodes: func(p map[string]string) []graph.Node {
			return []graph.Node{
				classNode("svc", "Inner", p["Inner.jsx"]),
				classNode("svc", "Middle", p["Middle.jsx"]),
				classNode("svc", "Outer", p["Outer.jsx"]),
			}
		},
	}
	nodes, ledger, files := vgPropSetup(t, c)

	got, _, _, retract := LinkJSPropURLs(nodes, ledger, files)
	if len(got) != 1 || got[0].Meta["url"] != "/api/deep" {
		t.Fatalf("two-hop crossing = %v, want [/api/deep]", urlSet(got))
	}
	if len(retract) != 1 {
		t.Errorf("a resolved site retracts its blind-spot row: %v", retract)
	}
}
