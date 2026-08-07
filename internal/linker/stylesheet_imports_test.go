package linker

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/require"
)

// stylesheetFixture writes files (relative path → contents) under a temp
// service root and returns the root plus the absolute file list.
func stylesheetFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var out []string
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(body), 0o644))
		out = append(out, abs)
	}
	sort.Strings(out)
	return root, out
}

// fileNodesFor mints the NodeTypeFile backbone LinkContainment would have
// produced, so the import pass has something to wire between.
func fileNodesFor(service string, files []string) []graph.Node {
	nodes := make([]graph.Node, 0, len(files))
	for _, f := range files {
		nodes = append(nodes, graph.Node{
			ID:      service + ":" + f + ":" + string(graph.NodeTypeFile),
			Type:    graph.NodeTypeFile,
			Label:   f,
			Service: service,
			File:    f,
		})
	}
	return nodes
}

func importLabels(edges []graph.Edge) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeImports {
			out = append(out, e.Label)
		}
	}
	sort.Strings(out)
	return out
}

// edgeBetween finds the edge from the file node of `from` to that of `to`.
func edgeBetween(t *testing.T, edges []graph.Edge, svc, from, to string) graph.Edge {
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

// TestLinkStylesheetImports_SassResolution is the worked example from
// orion's application.scss: an explicit `.scss`, a `.css`, a partial
// referenced without its underscore or extension, and a glob.
func TestLinkStylesheetImports_SassResolution(t *testing.T) {
	root, files := stylesheetFixture(t, map[string]string{
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
	nodes := fileNodesFor(svc, files)

	_, edges, unresolved := LinkStylesheetImports(nodes, map[string][]string{svc: files})
	require.Empty(t, unresolved)

	require.Equal(t, []string{
		"@import modules/*",
		"@import modules/*",
		"@import settings/colors",
		"@import style-components.css",
		"@import vendor/bourbon.scss",
	}, importLabels(edges))

	// `modules/*` reaches both files directly in the directory and not the one
	// a level deeper — `*` is not `**`.
	base := filepath.Join(root, "app/assets/stylesheets")
	app := filepath.Join(base, "application.scss")
	for _, target := range []string{"modules/issues.scss", "modules/audit-log.scss"} {
		e := edgeBetween(t, edges, svc, app, filepath.Join(base, target))
		require.Equal(t, graph.ConfidenceStatic, e.Confidence)
	}
	for _, e := range edges {
		require.NotContains(t, e.To, "deep/skip.scss", "glob must not recurse")
	}

	// The partial resolved through the `_name.scss` convention.
	e := edgeBetween(t, edges, svc, app, filepath.Join(base, "settings/_colors.scss"))
	require.Equal(t, "settings/colors", e.Meta["specifier"])
	require.Equal(t, "import", e.Meta["rule"])
}

// TestLinkStylesheetImports_MintsFileNodesForSilentPartials pins the bug that
// cost 33 of orion's 40 import edges: LinkContainment mints a file node only
// for a file that declares something, and a Sass partial of nothing but
// `$variables` declares nothing — yet partials are most of what an import graph
// points *at*. The pass must mint those file nodes itself.
func TestLinkStylesheetImports_MintsFileNodesForSilentPartials(t *testing.T) {
	root, files := stylesheetFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss":      `@import "settings/colors";`,
		"app/assets/stylesheets/settings/_colors.scss": "$brand: #0055aa;",
	})
	svc := "orion"
	base := filepath.Join(root, "app/assets/stylesheets")
	app := filepath.Join(base, "application.scss")
	partial := filepath.Join(base, "settings/_colors.scss")

	// Only the importer has a file node, exactly as containment would leave it.
	nodes := []graph.Node{
		{ID: svc + ":" + app + ":file", Type: graph.NodeTypeFile, Label: app, Service: svc, File: app},
		{ID: "service:" + svc, Type: graph.NodeTypeService, Label: svc},
	}

	newNodes, edges, unresolved := LinkStylesheetImports(nodes, map[string][]string{svc: files})
	require.Empty(t, unresolved)

	require.Len(t, newNodes, 1)
	require.Equal(t, partial, newNodes[0].File)
	require.Equal(t, graph.NodeTypeFile, newNodes[0].Type)
	require.Equal(t, "scss", newNodes[0].Language)

	edgeBetween(t, edges, svc, app, partial)
	// The minted file still joins the service backbone.
	var wired bool
	for _, e := range edges {
		if e.Type == graph.EdgeTypeContains && e.From == "service:"+svc && e.To == newNodes[0].ID {
			wired = true
		}
	}
	require.True(t, wired, "a minted file node must hang off its service")
}

// TestLinkStylesheetImports_LoadRootAndRelative: a specifier resolves relative
// to the importing file first, and falls back to the `stylesheets` load root —
// which is how `@import "settings/colors"` works from a file three directories
// deep.
func TestLinkStylesheetImports_LoadRootAndRelative(t *testing.T) {
	root, files := stylesheetFixture(t, map[string]string{
		"app/assets/stylesheets/modules/issues.scss": `
@import "settings/colors";
@import "shared";
`,
		"app/assets/stylesheets/settings/_colors.scss": "$c: red;",
		"app/assets/stylesheets/modules/_shared.scss":  ".sh { color: red; }",
	})
	svc := "orion"
	base := filepath.Join(root, "app/assets/stylesheets")
	_, edges, unresolved := LinkStylesheetImports(
		fileNodesFor(svc, files), map[string][]string{svc: files})
	require.Empty(t, unresolved)

	from := filepath.Join(base, "modules/issues.scss")
	edgeBetween(t, edges, svc, from, filepath.Join(base, "settings/_colors.scss"))
	// "shared" resolves relative to the importing file, not the load root.
	edgeBetween(t, edges, svc, from, filepath.Join(base, "modules/_shared.scss"))
}

// TestLinkStylesheetImports_UnresolvedIsLedgered: a specifier with no indexed
// target is recorded, never invented (phases.md #12). Protocol URLs get neither
// an edge nor a ledger entry, matching the JS bare-specifier precedent.
func TestLinkStylesheetImports_UnresolvedIsLedgered(t *testing.T) {
	_, files := stylesheetFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss": `
@import "bourbon";
@import "https://fonts.googleapis.com/css?family=Roboto";
`,
	})
	svc := "orion"
	_, edges, unresolved := LinkStylesheetImports(
		fileNodesFor(svc, files), map[string][]string{svc: files})

	require.Empty(t, importLabels(edges))
	require.Len(t, unresolved, 1)
	require.Equal(t, "stylesheet_import", unresolved[0].Kind)
	require.Equal(t, "bourbon", unresolved[0].Name)
	require.Equal(t, 2, unresolved[0].Line)
}

// TestLinkContainment_ContainsStylesheetSelectors: the ~1,900 selector nodes a
// real Rails app mints must hang off their file, not dangle. Element nodes from
// other passes (JSX, ERB) are untouched — they have their own attribution, and
// containing them by type here would move counts across the whole fleet.
func TestLinkContainment_ContainsStylesheetSelectors(t *testing.T) {
	const svc, sheet = "orion", "/app/assets/stylesheets/issues.scss"
	nodes := []graph.Node{
		{
			ID: "sel", Type: graph.NodeTypeElement, Label: ".btn",
			Service: svc, File: sheet,
			Meta: map[string]string{"pattern": "stylesheet_selector"},
		},
		{
			ID: "font", Type: graph.NodeTypeExternalService, Label: "DejaVu",
			Service: svc, File: sheet,
			Meta: map[string]string{"pattern": "font_face_src"},
		},
		{
			ID: "jsx", Type: graph.NodeTypeElement, Label: "button",
			Service: svc, File: "/app/javascript/App.jsx",
			Meta: map[string]string{"tag": "button"},
		},
	}

	newNodes, edges := LinkContainment(nodes)

	var fileNode *graph.Node
	for i := range newNodes {
		if newNodes[i].Type == graph.NodeTypeFile {
			require.Nil(t, fileNode, "only the stylesheet should get a file node")
			fileNode = &newNodes[i]
		}
	}
	require.NotNil(t, fileNode)
	require.Equal(t, sheet, fileNode.File)
	require.Equal(t, "scss", fileNode.Language)

	var contained []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeContains && e.From == fileNode.ID {
			contained = append(contained, e.To)
		}
	}
	sort.Strings(contained)
	require.Equal(t, []string{"font", "sel"}, contained)
}

// TestLinkStylesheetImports_Deterministic: repeated runs are byte-identical,
// including across the service map whose iteration order Go randomises
// (bug-class #2).
func TestLinkStylesheetImports_Deterministic(t *testing.T) {
	_, aFiles := stylesheetFixture(t, map[string]string{
		"app/assets/stylesheets/application.scss": `@import "modules/*";`,
		"app/assets/stylesheets/modules/a.scss":   ".a {}",
		"app/assets/stylesheets/modules/b.scss":   ".b {}",
	})
	_, bFiles := stylesheetFixture(t, map[string]string{
		"app/assets/stylesheets/main.scss":     `@import "parts/x";`,
		"app/assets/stylesheets/parts/_x.scss": ".x {}",
	})
	svcFiles := map[string][]string{"orion": aFiles, "willow": bFiles}
	nodes := append(fileNodesFor("orion", aFiles), fileNodesFor("willow", bFiles)...)

	firstNodes, first, _ := LinkStylesheetImports(nodes, svcFiles)
	for i := 0; i < 5; i++ {
		nextNodes, next, _ := LinkStylesheetImports(nodes, svcFiles)
		require.Equal(t, first, next)
		require.Equal(t, firstNodes, nextNodes)
	}
	require.Len(t, first, 3)
}
