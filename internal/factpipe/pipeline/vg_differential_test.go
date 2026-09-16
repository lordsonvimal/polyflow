package pipeline

import (
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

// Tier VG.3 acceptance goldens — ported from
// internal/linker/vg_differential_test.go's TestVGLocalURLFixtures when
// LinkJSPropClients migrated to Tier FX (schema_url_link + js_prop_clients
// combined migration, 2026-09-16). See that file's remaining
// TestVGLocalURLProvenanceStamp for the ResolveJSLocalURLs half, which
// stayed in internal/linker untouched.

// sulVGFixtures are the shapes js_local_url_test.go/js_local_url_test.go (in
// this package) exercise plus a couple that stress the engine's literal /
// concat / template handling directly.
var sulVGFixtures = map[string]map[string]string{
	"switch-two-arms": {"utils/lookup.jsx": sulLookupSwitch},
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

// sulVGCaptureClients runs the schema_url_link_props framework and packages
// the result into a vgbaseline.Baseline — the pipeline.Run equivalent of the
// retired vgCaptureClients (which called LinkJSPropClients directly).
func sulVGCaptureClients(t *testing.T, files map[string]string) *vgbaseline.Baseline {
	t.Helper()
	full := map[string]string{"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC}
	for k, v := range files {
		full[k] = v
	}
	_, fileList := sulWriteFixture(t, full)
	anchor := []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}
	res := sulRun(t, anchor, fileList)

	edgesByFrom := map[string][]vgbaseline.EdgeRecord{}
	for _, e := range res.Edges {
		if e.Type != graph.EdgeTypeCalls {
			continue
		}
		edgesByFrom[e.From] = append(edgesByFrom[e.From], vgbaseline.EdgeRecord{
			From: e.From, To: e.To, Type: string(e.Type),
		})
	}

	b := &vgbaseline.Baseline{Corpus: "fixture"}
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		b.Clients = append(b.Clients, vgbaseline.ClientRecord{
			ID: n.ID, Service: n.Service, File: n.File, Line: n.Line,
			Path: n.Meta["path"], Method: n.Meta["method"], URL: n.Meta["url"],
			Edges: edgesByFrom[n.ID],
		})
	}
	for _, r := range res.Unresolved {
		if r.Kind != "prop_client_dynamic_url" {
			continue
		}
		b.Ledger = append(b.Ledger, vgbaseline.LedgerRecord{
			Key: r.File + ":" + strconv.Itoa(r.Line), Service: r.Service,
			File: r.File, Line: r.Line, Name: r.Name, Kind: r.Kind,
		})
	}
	b.Sort()
	return b
}

// sulVGWant is what each fixture resolves to: the URLs minted, in the sorted
// order vgbaseline.Sort puts them in, and the number of blind-spot rows left
// behind. An empty urls list with one ledger row is the deliberate
// abstention case.
var sulVGWant = map[string]struct {
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

func TestSUL_VGLocalURLFixtures(t *testing.T) {
	names := make([]string, 0, len(sulVGFixtures))
	for name := range sulVGFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		files := sulVGFixtures[name]
		t.Run(name, func(t *testing.T) {
			want, ok := sulVGWant[name]
			if !ok {
				t.Fatalf("fixture %q has no expectation — add one rather than deleting the fixture", name)
			}
			got := sulVGCaptureClients(t, files)

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
