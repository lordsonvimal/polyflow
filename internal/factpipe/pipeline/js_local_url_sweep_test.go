package pipeline_test

// Tier RC.2 (docs/js-declarative-composition-cluster-plan.md) — replaces
// internal/linker/js_local_url.go's ResolveJSLocalURLs, migrated onto
// patterns/javascript/js_local_url.yaml + rules/javascript/js_local_url.dl,
// driven by the "js_local_url" hub (internal/factpipe/hub_js_local_url.go).
// Fixtures ported verbatim from the retired internal/linker/js_local_url_test.go
// (js_local_url_test.go in THIS package tests a different, older framework —
// schema_url_link_props' mint path through the same jsast resolver — hence
// this file's distinct name rather than a collision).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func julsActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_local_url")
	if fw == nil {
		t.Fatal("js_local_url framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func julsWriteFixture(t *testing.T, files map[string]string) (dir string, paths map[string]string) {
	t.Helper()
	dir = t.TempDir()
	paths = make(map[string]string, len(files))
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[rel] = p
	}
	return dir, paths
}

func julsRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(julsActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestJSLocalURLSweep_RewritesInPlaceAndAddsBranches(t *testing.T) {
	_, p := julsWriteFixture(t, map[string]string{"modules/arms.es6": `function reload(id, isNew) {
  let url;
  if (isNew) {
    url = "/app/arms/new";
  } else {
    url = ` + "`/app/arms/${id}/edit`" + `;
  }
  $.ajax({ url, type: "GET" });
}
`})
	nodes := []graph.Node{{
		ID: "svc:modules/arms.es6:http_client:8", Type: graph.NodeTypeHTTPClient,
		Label: "dynamic", Service: "svc", File: p["modules/arms.es6"], Line: 8,
		Language: "javascript",
		Meta: map[string]string{
			"method": "GET", "key_dynamic": "true", "key_dynamic_raw": `{ url, type: "GET" }`,
		},
	}}
	res := julsRun(t, nodes, []string{p["modules/arms.es6"]})

	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected ledger: %+v", res.Unresolved)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(res.Patches))
	}
	patch := res.Patches[0]
	if patch.ID != nodes[0].ID {
		t.Errorf("patch ID = %q, want origin node ID %q (rewritten in place)", patch.ID, nodes[0].ID)
	}
	if patch.Meta["url"] != "/app/arms/new" {
		t.Errorf("in-place url = %q, want /app/arms/new", patch.Meta["url"])
	}
	if patch.Label != "GET /app/arms/new" {
		t.Errorf("label = %q", patch.Label)
	}
	hasDelete := func(k string) bool {
		for _, d := range patch.DeleteMeta {
			if d == k {
				return true
			}
		}
		return false
	}
	if !hasDelete("key_dynamic") || !hasDelete("key_dynamic_raw") {
		t.Errorf("dynamic markers not retired: %+v", patch.DeleteMeta)
	}

	if len(res.Nodes) != 1 {
		t.Fatalf("minted nodes = %d, want 1", len(res.Nodes))
	}
	branch := res.Nodes[0]
	if branch.Meta["url"] != "/app/arms/*/edit" {
		t.Errorf("branch url = %q, want /app/arms/*/edit", branch.Meta["url"])
	}
	if branch.ID == nodes[0].ID {
		t.Fatal("branch node collides with the origin node's ID")
	}
	if branch.Meta["branch_index"] != "1" {
		t.Errorf("branch_index = %q, want 1", branch.Meta["branch_index"])
	}
}

func TestJSLocalURLSweep_UnreadableSiteLedgers(t *testing.T) {
	_, p := julsWriteFixture(t, map[string]string{"modules/report.es6": `function load() {
  $.ajax({ url: this.comparisonQueryUrl, type: "GET" });
}
`})
	nodes := []graph.Node{{
		ID: "svc:modules/report.es6:http_client:2", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/report.es6"], Line: 2, Language: "javascript",
		Meta: map[string]string{
			"method": "GET", "key_dynamic": "true",
			"key_dynamic_raw": `{ url: this.comparisonQueryUrl, type: "GET" }`,
		},
	}}
	res := julsRun(t, nodes, []string{p["modules/report.es6"]})
	if len(res.Patches) != 0 || len(res.Nodes) != 0 {
		t.Fatalf("a member expression resolved: patches=%+v nodes=%+v", res.Patches, res.Nodes)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "local_url_unresolved" {
		t.Fatalf("ledger = %+v, want one local_url_unresolved row", res.Unresolved)
	}
}

func TestJSLocalURLSweep_Idempotent(t *testing.T) {
	_, p := julsWriteFixture(t, map[string]string{"modules/one.es6": `function load(id) {
  const url = "/app/things";
  $.get(url);
}
`})
	nodes := []graph.Node{{
		ID: "svc:modules/one.es6:http_client:3", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/one.es6"], Line: 3, Language: "javascript",
		Meta: map[string]string{"method": "GET", "key_dynamic": "true", "key_dynamic_raw": "url"},
	}}
	res := julsRun(t, nodes, []string{p["modules/one.es6"]})
	if len(res.Patches) != 1 || len(res.Nodes) != 0 {
		t.Fatalf("first run: patches=%d nodes=%d", len(res.Patches), len(res.Nodes))
	}

	// Apply the patch, then re-run: a readable URL owes nothing more.
	nodes[0].Meta["url"] = res.Patches[0].Meta["url"]
	delete(nodes[0].Meta, "key_dynamic")
	delete(nodes[0].Meta, "key_dynamic_raw")

	res2 := julsRun(t, nodes, []string{p["modules/one.es6"]})
	if len(res2.Patches) != 0 || len(res2.Nodes) != 0 || len(res2.Unresolved) != 0 {
		t.Fatalf("second run was not a no-op: patches=%+v nodes=%+v ledger=%+v", res2.Patches, res2.Nodes, res2.Unresolved)
	}
}

func TestJSLocalURLSweep_SkipsNavLinks(t *testing.T) {
	_, p := julsWriteFixture(t, map[string]string{"modules/nav.es6": `function go() {
  const url = "/app/things";
  window.location = url;
}
`})
	nodes := []graph.Node{{
		ID: "svc:modules/nav.es6:http_client:3", Type: graph.NodeTypeHTTPClient,
		Service: "svc", File: p["modules/nav.es6"], Line: 3, Language: "javascript",
		Meta: map[string]string{"nav_link": "true", "key_dynamic": "true", "key_dynamic_raw": "url"},
	}}
	res := julsRun(t, nodes, []string{p["modules/nav.es6"]})
	if len(res.Patches) != 0 || len(res.Nodes) != 0 || len(res.Unresolved) != 0 {
		t.Fatalf("nav link touched: patches=%+v nodes=%+v ledger=%+v", res.Patches, res.Nodes, res.Unresolved)
	}
}
