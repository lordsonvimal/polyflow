package pipeline_test

// FX.8 (2026-09-15): stylesheet_imports — internal/linker/
// stylesheet_imports.go's retired LinkStylesheetImports, migrated onto
// patterns/css/stylesheet_imports.yaml + rules/css/stylesheet_imports.dl,
// driven by the stylesheet_imports_sites hub provider
// (internal/factpipe/hub_stylesheet_imports.go). Real file-I/O tests
// (temp-dir fixture files, porting the retired Go test's fixtures
// verbatim), one pipeline.Run call per service (the hub's svc comes from
// nodes[0].Service, correct only when one call covers exactly one
// service — the FX.8.8/8.10 convention).

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func csiActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("stylesheet_imports")
	if fw == nil {
		t.Fatal("stylesheet_imports framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

// csiFixture writes files (relative path -> contents) under a temp service
// root and returns the root plus the absolute file list.
func csiFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var out []string
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, abs)
	}
	sort.Strings(out)
	return root, out
}

func csiFileNodesFor(service string, files []string) []graph.Node {
	nodes := make([]graph.Node, 0, len(files))
	for _, f := range files {
		nodes = append(nodes, graph.Node{
			ID: service + ":" + f + ":" + string(graph.NodeTypeFile),
			Type: graph.NodeTypeFile, Label: f, Service: service, File: f,
		})
	}
	return nodes
}

func csiImportLabels(edges []graph.Edge) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeImports {
			out = append(out, e.Label)
		}
	}
	sort.Strings(out)
	return out
}

func csiEdgeBetween(t *testing.T, edges []graph.Edge, svc, from, to string) graph.Edge {
	t.Helper()
	fromID := svc + ":" + from + ":file"
	toID := svc + ":" + to + ":file"
	for _, e := range edges {
		if e.From == fromID && e.To == toID {
			return e
		}
	}
	t.Fatalf("no edge %s -> %s", from, to)
	return graph.Edge{}
}

func csiRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(csiActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestStylesheetImportsRule_SassResolution is the worked example from a
// real application.scss: an explicit .scss, a .css, a partial referenced
// without its underscore or extension, and a glob.
func TestStylesheetImportsRule_SassResolution(t *testing.T) {
	root, files := csiFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss": `
@import "vendor/bourbon.scss";
@import "style-components.css";
@import "settings/colors";
@import "modules/*";
`,
		"app/assets/stylesheets/vendor/bourbon.scss":    ".b { color: red; }",
		"app/assets/stylesheets/style-components.css":   ".s { color: red; }",
		"app/assets/stylesheets/settings/_colors.scss":  "$c: red;",
		"app/assets/stylesheets/modules/issues.scss":    ".i { color: red; }",
		"app/assets/stylesheets/modules/audit-log.scss": ".a { color: red; }",
		"app/assets/stylesheets/modules/deep/skip.scss": ".d { color: red; }",
	})
	svc := "orion"
	nodes := csiFileNodesFor(svc, files)

	res := csiRun(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Errorf("expected no unresolved, got %+v", res.Unresolved)
	}

	got := csiImportLabels(res.Edges)
	want := []string{
		"@import modules/*",
		"@import modules/*",
		"@import settings/colors",
		"@import style-components.css",
		"@import vendor/bourbon.scss",
	}
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("labels = %v, want %v", got, want)
			break
		}
	}

	base := filepath.Join(root, "app/assets/stylesheets")
	app := filepath.Join(base, "application.scss")
	for _, target := range []string{"modules/issues.scss", "modules/audit-log.scss"} {
		e := csiEdgeBetween(t, res.Edges, svc, app, filepath.Join(base, target))
		if e.Confidence != graph.ConfidenceStatic {
			t.Errorf("confidence = %q, want static", e.Confidence)
		}
	}
	for _, e := range res.Edges {
		if filepath.Base(filepath.Dir(e.To)) == "deep" {
			t.Errorf("glob must not recurse: %+v", e)
		}
	}

	e := csiEdgeBetween(t, res.Edges, svc, app, filepath.Join(base, "settings/_colors.scss"))
	if e.Meta["specifier"] != "settings/colors" || e.Meta["rule"] != "import" {
		t.Errorf("meta = %+v", e.Meta)
	}
}

// TestStylesheetImportsRule_MintsFileNodesForSilentPartials: a Sass partial
// of nothing but $variables declares nothing containment-shaped, yet
// partials are most of what an import graph points at — the pass must
// mint those file nodes itself.
func TestStylesheetImportsRule_MintsFileNodesForSilentPartials(t *testing.T) {
	root, files := csiFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss":      `@import "settings/colors";`,
		"app/assets/stylesheets/settings/_colors.scss": "$brand: #0055aa;",
	})
	svc := "orion"
	base := filepath.Join(root, "app/assets/stylesheets")
	app := filepath.Join(base, "application.scss")
	partial := filepath.Join(base, "settings/_colors.scss")

	nodes := []graph.Node{
		{ID: svc + ":" + app + ":file", Type: graph.NodeTypeFile, Label: app, Service: svc, File: app},
		{ID: "service:" + svc, Type: graph.NodeTypeService, Label: svc},
	}

	res := csiRun(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Errorf("expected no unresolved, got %+v", res.Unresolved)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("expected 1 minted node, got %d: %+v", len(res.Nodes), res.Nodes)
	}
	if res.Nodes[0].File != partial || res.Nodes[0].Type != graph.NodeTypeFile || res.Nodes[0].Language != "scss" {
		t.Errorf("minted node = %+v", res.Nodes[0])
	}

	csiEdgeBetween(t, res.Edges, svc, app, partial)
	wired := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeContains && e.From == "service:"+svc && e.To == res.Nodes[0].ID {
			wired = true
		}
	}
	if !wired {
		t.Error("a minted file node must hang off its service")
	}
}

// TestStylesheetImportsRule_LoadRootAndRelative: a specifier resolves
// relative to the importing file first, then falls back to the
// "stylesheets" load root.
func TestStylesheetImportsRule_LoadRootAndRelative(t *testing.T) {
	root, files := csiFixture(t, map[string]string{
		"app/assets/stylesheets/modules/issues.scss": `
@import "settings/colors";
@import "shared";
`,
		"app/assets/stylesheets/settings/_colors.scss": "$c: red;",
		"app/assets/stylesheets/modules/_shared.scss":  ".sh { color: red; }",
	})
	svc := "orion"
	base := filepath.Join(root, "app/assets/stylesheets")
	res := csiRun(t, csiFileNodesFor(svc, files), files)
	if len(res.Unresolved) != 0 {
		t.Errorf("expected no unresolved, got %+v", res.Unresolved)
	}

	from := filepath.Join(base, "modules/issues.scss")
	csiEdgeBetween(t, res.Edges, svc, from, filepath.Join(base, "settings/_colors.scss"))
	csiEdgeBetween(t, res.Edges, svc, from, filepath.Join(base, "modules/_shared.scss"))
}

// TestStylesheetImportsRule_UnresolvedIsLedgered: a specifier with no
// indexed target is recorded, never invented. Protocol URLs get neither an
// edge nor a ledger entry.
func TestStylesheetImportsRule_UnresolvedIsLedgered(t *testing.T) {
	_, files := csiFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss": `
@import "bourbon";
@import "https://fonts.googleapis.com/css?family=Roboto";
`,
	})
	svc := "orion"
	res := csiRun(t, csiFileNodesFor(svc, files), files)

	if len(csiImportLabels(res.Edges)) != 0 {
		t.Errorf("expected no import edges, got %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("expected 1 unresolved, got %+v", res.Unresolved)
	}
	if res.Unresolved[0].Kind != "stylesheet_import" || res.Unresolved[0].Name != "bourbon" || res.Unresolved[0].Line != 2 {
		t.Errorf("unresolved = %+v", res.Unresolved[0])
	}
}

// TestStylesheetImportsRule_Deterministic: repeated runs are
// byte-identical, one pipeline.Run call per service.
func TestStylesheetImportsRule_Deterministic(t *testing.T) {
	_, aFiles := csiFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss": `@import "modules/*";`,
		"app/assets/stylesheets/modules/a.scss":   ".a {}",
		"app/assets/stylesheets/modules/b.scss":   ".b {}",
	})
	_, bFiles := csiFixture(t, map[string]string{
		"app/assets/stylesheets/main.scss":     `@import "parts/x";`,
		"app/assets/stylesheets/parts/_x.scss": ".x {}",
	})

	run := func() []graph.Edge {
		var edges []graph.Edge
		edges = append(edges, csiRun(t, csiFileNodesFor("orion", aFiles), aFiles).Edges...)
		edges = append(edges, csiRun(t, csiFileNodesFor("willow", bFiles), bFiles).Edges...)
		return edges
	}

	first := run()
	for i := 0; i < 5; i++ {
		next := run()
		if len(next) != len(first) {
			t.Fatalf("run %d: got %d edges, want %d", i, len(next), len(first))
		}
		for j := range first {
			if !reflect.DeepEqual(next[j], first[j]) {
				t.Errorf("run %d: edge %d = %+v, want %+v", i, j, next[j], first[j])
			}
		}
	}
	if len(first) != 3 {
		t.Errorf("expected 3 edges, got %d: %+v", len(first), first)
	}
}
