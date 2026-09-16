package pipeline

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier FX — schema_url_link + js_prop_clients combined migration
// (2026-09-16). Ports internal/linker/js_local_url_test.go's
// propClientURLs-based tests, which exercised Tier UL (jsast's
// ResolveLocalURLBinding) through the retired LinkJSPropClients' mint path —
// that mint path is now this package's schema_url_link_props framework.

// sulLookupSwitch is the plan's worked example: one local assigned a distinct
// template literal on each arm of a switch, read at a single call site.
const sulLookupSwitch = `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
export function search(ajaxStatus, kind, name) {
  let searchURL;
  switch (kind) {
    case "Form":
      searchURL = ` + "`/api/forms/list?name=${name}`" + `;
      break;
    case "Item":
      searchURL = ` + "`/api/items/list?name=${name}`" + `;
      break;
  }
  return ajaxStatus.get("Loading...", searchURL);
}
`

// sulPropClientURLs runs the schema_url_link_props framework over files
// (plus the standard ajax-status HOC) and returns the minted http_client
// nodes and unresolved ledger — the pipeline.Run equivalent of the retired
// propClientURLs helper.
func sulPropClientURLs(t *testing.T, files map[string]string) ([]graph.Node, []graph.UnresolvedRef) {
	t.Helper()
	full := map[string]string{"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC}
	for k, v := range files {
		full[k] = v
	}
	_, fileList := sulWriteFixture(t, full)
	anchor := []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}
	res := sulRun(t, anchor, fileList)
	var clients []graph.Node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			clients = append(clients, n)
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
	return clients, res.Unresolved
}

// A switch with two resolvable arms is two flows at one site, so it mints two
// nodes — not one node with two outbound edges, which is what would break the
// fan-out invariant.
func TestSUL_ResolveLocalURLBinding_SwitchMintsOneNodePerURL(t *testing.T) {
	t.Parallel()
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/lookup.jsx": sulLookupSwitch})

	if len(ledger) != 0 {
		t.Errorf("unexpected ledger: %+v", ledger)
	}
	if len(clients) != 2 {
		t.Fatalf("got %d http_client nodes, want 2: %+v", len(clients), clients)
	}
	got := []string{clients[0].Meta["url"], clients[1].Meta["url"]}
	sort.Strings(got)
	want := []string{"/api/forms/list?name=*", "/api/items/list?name=*"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for i := range clients {
		if clients[i].Meta["url_origin"] != "local_binding" {
			t.Errorf("node %d url_origin = %q", i, clients[i].Meta["url_origin"])
		}
		if clients[i].Meta["key_candidates"] != "" {
			t.Errorf("node %d carries key_candidates %q — that is the fan-out shape this tier replaces",
				i, clients[i].Meta["key_candidates"])
		}
	}
	if clients[0].ID == clients[1].ID {
		t.Fatal("branch nodes share an ID; the writer's dedup would drop one flow")
	}
	if clients[0].Line != clients[1].Line {
		t.Errorf("branches sit at different lines (%d, %d); both are the same call site",
			clients[0].Line, clients[1].Line)
	}
}

// The inversion the plan calls the single most important behaviour to test:
// adding an arm whose right-hand side cannot be read removes both working
// flows. Three of four flows presented as complete is worse than a ledger row,
// so partial resolution is not offered — do not soften this.
func TestSUL_ResolveLocalURLBinding_OneUnreadableArmPoisonsTheSite(t *testing.T) {
	t.Parallel()
	src := `export function search(ajaxStatus, kind, name) {
  let searchURL;
  switch (kind) {
    case "Form":
      searchURL = ` + "`/api/forms/list?name=${name}`" + `;
      break;
    case "Item":
      searchURL = ` + "`/api/items/list?name=${name}`" + `;
      break;
    case "Other":
      searchURL = buildLookupURL(kind, name);
      break;
  }
  return ajaxStatus.get("Loading...", searchURL);
}
`
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/lookup.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("got %d http_client nodes, want 0: %+v", len(clients), clients)
	}
	if len(ledger) != 1 || ledger[0].Kind != "prop_client_dynamic_url" {
		t.Fatalf("ledger = %+v, want one prop_client_dynamic_url row", ledger)
	}
}

// The commonest unread shape in the corpus: an options object whose `url` key
// is shorthand for a local bound on each arm of an if/else.
func TestSUL_ResolveLocalURLBinding_OptionsObjectShorthand(t *testing.T) {
	t.Parallel()
	src := `export function submit(ajaxStatus, formData) {
  let url;
  let httpVerb;
  if (formData.id) {
    httpVerb = "PUT";
    url = ` + "`/api/clients/${formData.id}`" + `;
  } else {
    httpVerb = "POST";
    url = "/api/clients";
  }
  return ajaxStatus.ajax("Submitting...", { url, method: httpVerb });
}
`
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/submit.jsx": src})
	if len(ledger) != 0 {
		t.Errorf("unexpected ledger: %+v", ledger)
	}
	got := make([]string, 0, len(clients))
	for _, c := range clients {
		got = append(got, c.Meta["url"])
	}
	sort.Strings(got)
	want := []string{"/api/clients", "/api/clients/*"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("urls = %v, want %v", got, want)
	}
}

// A path hole must stay a wildcard, never be truncated away: `/api/clients/*`
// matches the member route `:id`, whereas `/api/clients/` would normalize to
// the collection route — a wrong edge in place of a missing one.
func TestSUL_ResolveLocalURLBinding_PathHoleStaysWildcard(t *testing.T) {
	t.Parallel()
	src := `export function del(ajaxStatus, ctx) {
  const url = ` + "`/api/data_model_types/${ctx.data.id}`" + `;
  return ajaxStatus.ajax("Deleting", { url, method: "DELETE" });
}
`
	clients, _ := sulPropClientURLs(t, map[string]string{"utils/del.jsx": src})
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1: %+v", len(clients), clients)
	}
	if clients[0].Meta["url"] != "/api/data_model_types/*" {
		t.Errorf("url = %q, want /api/data_model_types/*", clients[0].Meta["url"])
	}
}

// Two arms assigning the same literal are one flow, not two.
func TestSUL_ResolveLocalURLBinding_DuplicateBranchesDedupe(t *testing.T) {
	t.Parallel()
	src := `export function usage(ajaxStatus, ids, parents) {
  let url;
  if (ids.length > 0) {
    url = "/api/code_lists/usage";
  } else if (parents.length > 1) {
    url = "/api/code_lists/usage";
  }
  return ajaxStatus.get("Loading", url);
}
`
	clients, _ := sulPropClientURLs(t, map[string]string{"utils/usage.jsx": src})
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1: %+v", len(clients), clients)
	}
	if clients[0].Meta["url"] != "/api/code_lists/usage" {
		t.Errorf("url = %q", clients[0].Meta["url"])
	}
}

// A sibling function's `url` is not this call's. Without the intraprocedural
// bound the search would find it and mint a confident wrong path.
func TestSUL_ResolveLocalURLBinding_SiblingFunctionBindingNotRead(t *testing.T) {
	t.Parallel()
	src := `function other() {
  const url = "/api/other";
  return url;
}
export function load(ajaxStatus, url) {
  return ajaxStatus.get("Loading", url);
}
`
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/two.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("a sibling function's binding leaked: %+v", clients)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger = %+v, want one row", ledger)
	}
}

// A module-scope write means the enclosing function does not own the name, so
// the assignments found inside it are not the whole story. Written with two
// in-function writes so the site actually reaches this tier: a single write is
// resolved by the KeyWalker's own C.1 backtrack before Tier UL is consulted,
// and that path's willingness to ignore a module-level write is pre-existing
// behaviour this tier does not renegotiate.
func TestSUL_ResolveLocalURLBinding_ModuleScopeReassignAbstains(t *testing.T) {
	t.Parallel()
	src := `let url = "/api/a";
url = "/api/b";
export function load(ajaxStatus, flag) {
  if (flag) {
    url = "/api/c";
  } else {
    url = "/api/d";
  }
  return ajaxStatus.get("Loading", url);
}
`
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/mod.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("resolved despite a module-scope write: %+v", clients)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger = %+v, want one row", ledger)
	}
}

// More than MaxLocalURLBranches distinct URLs is a table or a loop, not a
// branch, and says so in its own ledger kind rather than the generic one.
func TestSUL_ResolveLocalURLBinding_HighFanoutLedgersItsOwnKind(t *testing.T) {
	t.Parallel()
	src := `export function pick(ajaxStatus, k) {
  let url;
  if (k === 0) { url = "/api/a0"; }
  else if (k === 1) { url = "/api/a1"; }
  else if (k === 2) { url = "/api/a2"; }
  else if (k === 3) { url = "/api/a3"; }
  else if (k === 4) { url = "/api/a4"; }
  else if (k === 5) { url = "/api/a5"; }
  else if (k === 6) { url = "/api/a6"; }
  else if (k === 7) { url = "/api/a7"; }
  else { url = "/api/a8"; }
  return ajaxStatus.get("Loading", url);
}
`
	clients, ledger := sulPropClientURLs(t, map[string]string{"utils/pick.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("got %d clients past the cap: %+v", len(clients), clients)
	}
	if len(ledger) != 1 || ledger[0].Kind != "local_url_high_fanout" {
		t.Fatalf("ledger = %+v, want one local_url_high_fanout row", ledger)
	}
}
