package linker

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// lookupSwitch is the plan's worked example: one local assigned a distinct
// template literal on each arm of a switch, read at a single call site.
const lookupSwitch = `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
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

func propClientURLs(t *testing.T, files map[string]string) ([]graph.Node, []graph.UnresolvedRef) {
	t.Helper()
	files["common/ComponentWithAjaxStatus.jsx"] = ajaxStatusHOC
	_, p := writeReduxFixture(t, files)
	var abs []string
	for name := range files {
		abs = append(abs, p[name])
	}
	sort.Strings(abs)
	nodes, _, ledger := LinkJSPropClients(nil, map[string][]string{"svc": abs})
	var clients []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			clients = append(clients, n)
		}
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
	return clients, ledger
}

// A switch with two resolvable arms is two flows at one site, so it mints two
// nodes — not one node with two outbound edges, which is what would break the
// fan-out invariant.
func TestResolveLocalURLBinding_SwitchMintsOneNodePerURL(t *testing.T) {
	t.Parallel()
	clients, ledger := propClientURLs(t, map[string]string{"utils/lookup.jsx": lookupSwitch})

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
		if clients[i].Meta["url_origin"] != localURLOriginLocalBinding {
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
func TestResolveLocalURLBinding_OneUnreadableArmPoisonsTheSite(t *testing.T) {
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
	clients, ledger := propClientURLs(t, map[string]string{"utils/lookup.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("got %d http_client nodes, want 0: %+v", len(clients), clients)
	}
	if len(ledger) != 1 || ledger[0].Kind != "prop_client_dynamic_url" {
		t.Fatalf("ledger = %+v, want one prop_client_dynamic_url row", ledger)
	}
}

// The commonest unread shape in the corpus: an options object whose `url` key
// is shorthand for a local bound on each arm of an if/else.
func TestResolveLocalURLBinding_OptionsObjectShorthand(t *testing.T) {
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
	clients, ledger := propClientURLs(t, map[string]string{"utils/submit.jsx": src})
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
func TestResolveLocalURLBinding_PathHoleStaysWildcard(t *testing.T) {
	t.Parallel()
	src := `export function del(ajaxStatus, ctx) {
  const url = ` + "`/api/data_model_types/${ctx.data.id}`" + `;
  return ajaxStatus.ajax("Deleting", { url, method: "DELETE" });
}
`
	clients, _ := propClientURLs(t, map[string]string{"utils/del.jsx": src})
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1: %+v", len(clients), clients)
	}
	if clients[0].Meta["url"] != "/api/data_model_types/*" {
		t.Errorf("url = %q, want /api/data_model_types/*", clients[0].Meta["url"])
	}
}

// Two arms assigning the same literal are one flow, not two.
func TestResolveLocalURLBinding_DuplicateBranchesDedupe(t *testing.T) {
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
	clients, _ := propClientURLs(t, map[string]string{"utils/usage.jsx": src})
	if len(clients) != 1 {
		t.Fatalf("got %d clients, want 1: %+v", len(clients), clients)
	}
	if clients[0].Meta["url"] != "/api/code_lists/usage" {
		t.Errorf("url = %q", clients[0].Meta["url"])
	}
}

// A sibling function's `url` is not this call's. Without the intraprocedural
// bound the search would find it and mint a confident wrong path.
func TestResolveLocalURLBinding_SiblingFunctionBindingNotRead(t *testing.T) {
	t.Parallel()
	src := `function other() {
  const url = "/api/other";
  return url;
}
export function load(ajaxStatus, url) {
  return ajaxStatus.get("Loading", url);
}
`
	clients, ledger := propClientURLs(t, map[string]string{"utils/two.jsx": src})
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
func TestResolveLocalURLBinding_ModuleScopeReassignAbstains(t *testing.T) {
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
	clients, ledger := propClientURLs(t, map[string]string{"utils/mod.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("resolved despite a module-scope write: %+v", clients)
	}
	if len(ledger) != 1 {
		t.Fatalf("ledger = %+v, want one row", ledger)
	}
}

// More than maxLocalURLBranches distinct URLs is a table or a loop, not a
// branch, and says so in its own ledger kind rather than the generic one.
func TestResolveLocalURLBinding_HighFanoutLedgersItsOwnKind(t *testing.T) {
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
	clients, ledger := propClientURLs(t, map[string]string{"utils/pick.jsx": src})
	if len(clients) != 0 {
		t.Fatalf("got %d clients past the cap: %+v", len(clients), clients)
	}
	if len(ledger) != 1 || ledger[0].Kind != ledgerLocalURLHighFanout {
		t.Fatalf("ledger = %+v, want one %s row", ledger, ledgerLocalURLHighFanout)
	}
}

// ── the mutating pass over matcher-minted clients ───────────────────────────

func TestResolveJSLocalURLs_RewritesInPlaceAndAddsBranches(t *testing.T) {
	t.Parallel()
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
	changed, added, ledger := ResolveJSLocalURLs(in)
	if len(ledger) != 0 {
		t.Errorf("unexpected ledger: %+v", ledger)
	}
	if len(changed) != 1 || len(added) != 1 {
		t.Fatalf("changed=%d added=%d, want 1 and 1", len(changed), len(added))
	}
	// The origin node is rewritten in place — a second node at the same ID
	// would give the contract engine two producers for one site.
	if in[0].ID != changed[0].ID {
		t.Errorf("origin node ID changed: %q -> %q", in[0].ID, changed[0].ID)
	}
	if in[0].Meta["url"] != "/app/arms/new" {
		t.Errorf("in-place url = %q, want /app/arms/new", in[0].Meta["url"])
	}
	if in[0].Meta["key_dynamic"] != "" || in[0].Meta["key_dynamic_raw"] != "" {
		t.Errorf("dynamic markers survived: %+v", in[0].Meta)
	}
	if in[0].Label != "GET /app/arms/new" {
		t.Errorf("label = %q", in[0].Label)
	}
	if added[0].Meta["url"] != "/app/arms/*/edit" {
		t.Errorf("branch url = %q, want /app/arms/*/edit", added[0].Meta["url"])
	}
	if added[0].ID == in[0].ID {
		t.Fatal("branch node collides with the origin node's ID")
	}
	if added[0].Meta["branch_index"] != "1" {
		t.Errorf("branch_index = %q, want 1", added[0].Meta["branch_index"])
	}
}

// Every candidate site the pass declines must leave a row. A site with neither
// a node nor a ledger entry is the silence this tier exists to remove.
func TestResolveJSLocalURLs_UnreadableSiteLedgers(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{"modules/report.es6": `function load() {
  $.ajax({ url: this.comparisonQueryUrl, type: "GET" });
}
`})
	in := []graph.Node{{
		ID: "svc:modules/report.es6:http_client:2", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/report.es6"], Line: 2, Language: "javascript",
		Meta: map[string]string{
			"method": "GET", "key_dynamic": "true",
			"key_dynamic_raw": `{ url: this.comparisonQueryUrl, type: "GET" }`,
		},
	}}
	changed, added, ledger := ResolveJSLocalURLs(in)
	if len(changed) != 0 || len(added) != 0 {
		t.Fatalf("a member expression resolved: changed=%+v added=%+v", changed, added)
	}
	if len(ledger) != 1 || ledger[0].Kind != ledgerLocalURLUnresolved {
		t.Fatalf("ledger = %+v, want one %s row", ledger, ledgerLocalURLUnresolved)
	}
}

// Re-running the pass over its own output must be a no-op: the second run sees
// a readable URL and owes nothing.
func TestResolveJSLocalURLs_Idempotent(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{"modules/one.es6": `function load(id) {
  const url = "/app/things";
  $.get(url);
}
`})
	in := []graph.Node{{
		ID: "svc:modules/one.es6:http_client:3", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/one.es6"], Line: 3, Language: "javascript",
		Meta: map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "url"},
	}}
	if changed, added, _ := ResolveJSLocalURLs(in); len(changed) != 1 || len(added) != 0 {
		t.Fatalf("first run: changed=%d added=%d", len(changed), len(added))
	}
	changed, added, ledger := ResolveJSLocalURLs(in)
	if len(changed) != 0 || len(added) != 0 || len(ledger) != 0 {
		t.Fatalf("second run was not a no-op: changed=%+v added=%+v ledger=%+v", changed, added, ledger)
	}
}

// Nav links have their own tier and their own reasons for being unreadable
// (`href="#"`, `javascript:`); this pass must not touch them.
func TestResolveJSLocalURLs_SkipsNavLinks(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{"modules/nav.es6": `function go() {
  const url = "/app/things";
  window.location = url;
}
`})
	in := []graph.Node{{
		ID: "svc:modules/nav.es6:http_client:3", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/nav.es6"], Line: 3, Language: "javascript",
		Meta: map[string]string{"nav_link": "true", "key_dynamic": "true", "key_dynamic_raw": "url"},
	}}
	changed, added, ledger := ResolveJSLocalURLs(in)
	if len(changed) != 0 || len(added) != 0 || len(ledger) != 0 {
		t.Fatalf("nav link touched: changed=%+v added=%+v ledger=%+v", changed, added, ledger)
	}
}
