package linker

import (
	"os"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

// Tier VG.3 acceptance (docs/js-value-graph-pilot-plan.md): the engine path
// must reproduce the legacy walker byte-for-byte on the URL-resolution passes —
// zero LOST and zero CHANGED rows. GAINED rows are permitted (the engine's
// lexical scope walk can resolve a module const the intraprocedural walker
// abstains on) but are surfaced here so a real run can enumerate them.
//
// This runs in-process against fixtures rather than a corpus; the cold-cedar
// vgdiff run is the plan owner's, exactly as VG.0's baseline capture is.

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

func TestVGLocalURLDifferential(t *testing.T) {
	// Not parallel: toggles the process-wide PF_VALUEGRAPH flag.
	prev, had := os.LookupEnv("PF_VALUEGRAPH")
	t.Cleanup(func() {
		if had {
			os.Setenv("PF_VALUEGRAPH", prev)
		} else {
			os.Unsetenv("PF_VALUEGRAPH")
		}
	})

	names := make([]string, 0, len(vgFixtures))
	for name := range vgFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		files := vgFixtures[name]
		t.Run(name, func(t *testing.T) {
			abs := vgWriteFixture(t, files)

			os.Unsetenv("PF_VALUEGRAPH")
			base := vgCaptureClients(t, abs)

			os.Setenv("PF_VALUEGRAPH", "1")
			cand := vgCaptureClients(t, abs)

			d := vgbaseline.Compare(base, cand)
			if d.Regressions() != 0 {
				t.Fatalf("engine path regressed against the legacy walker:\n%s", d.String())
			}
			for _, row := range d.Rows {
				t.Logf("GAINED (permitted, enumerate in VG.5): %s", row.Detail)
			}
		})
	}
}

// TestVGLocalURLProvenanceStamp asserts the engine path stamps SA.1 layer/rule
// onto the node it mutates (deliverable 4). The legacy path leaves them unset.
func TestVGLocalURLProvenanceStamp(t *testing.T) {
	prev, had := os.LookupEnv("PF_VALUEGRAPH")
	t.Cleanup(func() {
		if had {
			os.Setenv("PF_VALUEGRAPH", prev)
		} else {
			os.Unsetenv("PF_VALUEGRAPH")
		}
	})

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
	mk := func() []graph.Node {
		return []graph.Node{{
			ID: "svc:modules/arms.es6:http_client:8", Type: graph.NodeTypeHTTPClient,
			Label: "dynamic", Service: "svc", File: p["modules/arms.es6"], Line: 8,
			Language: "javascript",
			Meta: map[string]string{
				"method": "GET", "key_dynamic": "true", "key_dynamic_raw": `{ url, type: "GET" }`,
			},
		}}
	}

	os.Setenv("PF_VALUEGRAPH", "1")
	in := mk()
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

	os.Unsetenv("PF_VALUEGRAPH")
	legacy := mk()
	lc, _, _ := ResolveJSLocalURLs(legacy, nil)
	if len(lc) != 1 || lc[0].Meta["vg_layer"] != "" {
		t.Errorf("legacy path stamped VG provenance: %+v", lc[0].Meta)
	}
}
