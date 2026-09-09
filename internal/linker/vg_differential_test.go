package linker

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

// Tier VG.3 acceptance (docs/js-value-graph-pilot-plan.md) was a differential:
// the engine had to reproduce the legacy walker byte-for-byte — zero LOST, zero
// CHANGED. VG.5 retired the walker, so there is no second arm left to compare
// against and these fixtures became goldens: the shapes and the exact paths the
// engine reads out of them, frozen. A diff here is a behaviour change, and
// changing the table is how you declare one.
//
// This runs in-process against fixtures rather than a corpus; the cold-cedar
// vgdiff run against a captured baseline is the plan owner's, exactly as VG.0's
// baseline capture is.

// vgFixtures are the shapes js_local_url_test.go exercises plus a couple that
// stress the engine's literal / concat / template handling directly.
var vgFixtures = map[string]map[string]string{
	"switch-two-arms": {"utils/lookup.jsx": lookupSwitch},
	"if-else-shorthand": {"utils/submit.jsx": `export function submit(ajaxStatus, formData) {
  let url;
  if (formData.id) {
    url = ` + "`/api/clients/${formData.id}`" + `;
  } else {
    url = "/api/clients";
  }
  return ajaxStatus.ajax("Submitting...", { url, method: "POST" });
}
`},
	"path-hole-wildcard": {"utils/del.jsx": `export function del(ajaxStatus, ctx) {
  const url = ` + "`/api/data_model_types/${ctx.data.id}`" + `;
  return ajaxStatus.ajax("Deleting", { url, method: "DELETE" });
}
`},
	"concat-of-local-const": {"utils/cat.jsx": `export function load(ajaxStatus, kind) {
  const base = "/api/v1/games";
  const url = base + "/" + kind;
  return ajaxStatus.get("Loading", url);
}
`},
	"unreadable-arm-poisons": {"utils/poison.jsx": `export function search(ajaxStatus, kind, name) {
  let searchURL;
  switch (kind) {
    case "Form":
      searchURL = ` + "`/api/forms/list?name=${name}`" + `;
      break;
    case "Other":
      searchURL = buildLookupURL(kind, name);
      break;
  }
  return ajaxStatus.get("Loading...", searchURL);
}
`},
	"module-scope-reassign": {"utils/mod.jsx": `let url = "/api/a";
url = "/api/b";
export function load(ajaxStatus, flag) {
  if (flag) { url = "/api/c"; } else { url = "/api/d"; }
  return ajaxStatus.get("Loading", url);
}
`},
	"sibling-fn-not-read": {"utils/two.jsx": `function other() {
  const url = "/api/other";
  return url;
}
export function load(ajaxStatus, url) {
  return ajaxStatus.get("Loading", url);
}
`},
}

func vgWriteFixture(t *testing.T, files map[string]string) []string {
	t.Helper()
	full := map[string]string{"common/ComponentWithAjaxStatus.jsx": ajaxStatusHOC}
	for k, v := range files {
		full[k] = v
	}
	_, p := writeReduxFixture(t, full)
	var abs []string
	for name := range full {
		abs = append(abs, p[name])
	}
	sort.Strings(abs)
	return abs
}

func vgCaptureClients(t *testing.T, abs []string) *vgbaseline.Baseline {
	t.Helper()
	nodes, edges, ledger := LinkJSPropClients(nil, map[string][]string{"svc": abs}, nil)

	edgesByFrom := map[string][]vgbaseline.EdgeRecord{}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeHTTPCall {
			continue
		}
		edgesByFrom[e.From] = append(edgesByFrom[e.From], vgbaseline.EdgeRecord{
			From: e.From, To: e.To, Type: string(e.Type),
		})
	}

	b := &vgbaseline.Baseline{Corpus: "fixture"}
	for _, n := range nodes {
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		b.Clients = append(b.Clients, vgbaseline.ClientRecord{
			ID: n.ID, Service: n.Service, File: n.File, Line: n.Line,
			Path: n.Meta["path"], Method: n.Meta["method"], URL: n.Meta["url"],
			Edges: edgesByFrom[n.ID],
		})
	}
	for _, r := range ledger {
		if r.Kind != "prop_client_dynamic_url" {
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

// vgWant is what each fixture resolves to: the URLs minted, in the sorted order
// vgbaseline.Sort puts them in, and the number of blind-spot rows left behind.
// An empty urls list with one ledger row is the deliberate abstention case.
var vgWant = map[string]struct {
	urls   []string
	ledger int
}{
	"switch-two-arms":        {urls: []string{"/api/forms/list?name=*", "/api/items/list?name=*"}},
	"if-else-shorthand":      {urls: []string{"/api/clients/*", "/api/clients"}},
	"path-hole-wildcard":     {urls: []string{"/api/data_model_types/*"}},
	"concat-of-local-const":  {urls: []string{"/api/v1/games/*"}},
	"unreadable-arm-poisons": {ledger: 1},
	"module-scope-reassign":  {ledger: 1},
	"sibling-fn-not-read":    {ledger: 1},
}

func TestVGLocalURLFixtures(t *testing.T) {
	names := make([]string, 0, len(vgFixtures))
	for name := range vgFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		files := vgFixtures[name]
		t.Run(name, func(t *testing.T) {
			want, ok := vgWant[name]
			if !ok {
				t.Fatalf("fixture %q has no expectation — add one rather than deleting the fixture", name)
			}
			got := vgCaptureClients(t, vgWriteFixture(t, files))

			var urls []string
			for _, c := range got.Clients {
				urls = append(urls, c.URL)
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

// TestVGLocalURLProvenanceStamp asserts the resolver stamps SA.1 layer/rule
// onto every node it mints or mutates — writeEdges propagates it from there
// onto the http_call edge.
func TestVGLocalURLProvenanceStamp(t *testing.T) {
	_, p := writeReduxFixture(t, map[string]string{"modules/arms.es6": `function reload(id, isNew) {
  let url;
  if (isNew) {
    url = "/app/arms/new";
  } else {
    url = ` + "`/app/arms/${id}/edit`" + `;
  }
  $.ajax({ url, type: "GET" });
}
`})
	in := []graph.Node{{
		ID: "svc:modules/arms.es6:http_client:8", Type: graph.NodeTypeHTTPClient,
		Label: "dynamic", Service: "svc", File: p["modules/arms.es6"], Line: 8,
		Language: "javascript",
		Meta: map[string]string{
			"method": "GET", "key_dynamic": "true", "key_dynamic_raw": `{ url, type: "GET" }`,
		},
	}}

	changed, added, _ := ResolveJSLocalURLs(in, nil)
	if len(changed) != 1 || len(added) != 1 {
		t.Fatalf("changed=%d added=%d, want 1 and 1", len(changed), len(added))
	}
	for _, n := range append(changed, added...) {
		if n.Meta["vg_layer"] != vgLayer || n.Meta["vg_rule"] != vgLocalBindingRule {
			t.Errorf("node %s missing VG provenance: layer=%q rule=%q",
				n.ID, n.Meta["vg_layer"], n.Meta["vg_rule"])
		}
	}
}
