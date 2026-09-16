package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ── the mutating pass over matcher-minted clients ───────────────────────────
//
// The propClientURLs-based tests (Tier UL exercised through the retired
// LinkJSPropClients' mint path) moved to
// internal/factpipe/pipeline/js_local_url_test.go when that pass migrated to
// Tier FX (schema_url_link + js_prop_clients combined migration,
// 2026-09-16). ResolveJSLocalURLs itself was never part of that migration —
// it stays here, tested directly.

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
	changed, added, ledger := ResolveJSLocalURLs(in, nil)
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
	changed, added, ledger := ResolveJSLocalURLs(in, nil)
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
	if changed, added, _ := ResolveJSLocalURLs(in, nil); len(changed) != 1 || len(added) != 0 {
		t.Fatalf("first run: changed=%d added=%d", len(changed), len(added))
	}
	changed, added, ledger := ResolveJSLocalURLs(in, nil)
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
	changed, added, ledger := ResolveJSLocalURLs(in, nil)
	if len(changed) != 0 || len(added) != 0 || len(ledger) != 0 {
		t.Fatalf("nav link touched: changed=%+v added=%+v ledger=%+v", changed, added, ledger)
	}
}
