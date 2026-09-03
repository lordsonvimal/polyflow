package linker

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/require"
)

// assetTargets returns the target file paths of every imports edge leaving the
// given file, keyed off the ID scheme fileNodeIndex mints.
func assetTargets(edges []graph.Edge, svc, from string) []string {
	fromID := svc + ":" + from + ":file"
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeImports && e.From == fromID {
			id := e.To
			id = id[len(svc)+1 : len(id)-len(":file")]
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// TestLinkSprocketsAssets_DirectiveGraph is the worked example: a manifest that
// requires a sibling by logical path, another load path's asset by its
// subdirectory, a relative path, and a tree.
func TestLinkSprocketsAssets_DirectiveGraph(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/assets/javascripts/application.js": `// manifest
//= require jquery/dist/jquery.min
//= require utility/common
//= require ./components/modal
//= require_tree ./vega
//= require studies
`,
		"app/assets/javascripts/utility/common.js":   "function common() {}",
		"app/assets/javascripts/components/modal.js": "function modal() {}",
		"app/assets/javascripts/vega/vega.js":          "function vega() {}",
		"app/assets/javascripts/vega/deep/nested.js":  "function nested() {}",
		"app/assets/javascripts/studies.es6":         "export const studies = 1;",
	})
	svc := "orion"
	js := filepath.Join(root, "app/assets/javascripts")
	app := filepath.Join(js, "application.js")

	_, edges, unresolved := LinkSprocketsAssets(fileNodesFor(svc, files), map[string][]string{svc: files})

	require.Equal(t, []string{
		filepath.Join(js, "components/modal.js"),
		filepath.Join(js, "studies.es6"), // logical path resolved through .es6
		filepath.Join(js, "utility/common.js"),
		filepath.Join(js, "vega/deep/nested.js"), // require_tree is recursive
		filepath.Join(js, "vega/vega.js"),
	}, assetTargets(edges, svc, app))

	// The node_modules require has no indexed target: ledgered, never invented
	// (phases.md #12).
	require.Len(t, unresolved, 1)
	require.Equal(t, "sprockets_require_unresolved", unresolved[0].Kind)
	require.Equal(t, "jquery/dist/jquery.min", unresolved[0].Name)
	require.Equal(t, 2, unresolved[0].Line)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeImports {
			require.Equal(t, "sprockets", e.Meta["mechanism"])
			require.Equal(t, graph.ConfidenceStatic, e.Confidence)
		}
	}
}

// TestLinkSprocketsAssets_IncludeTagBindsPageToManifest closes the loop the
// phase exists for: a layout names a manifest, and the manifest's own requires
// carry the walk down to the leaf asset.
func TestLinkSprocketsAssets_IncludeTagBindsPageToManifest(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/views/layouts/app.html.erb": `<head>
  <%= javascript_include_tag 'home' %>
  <%= stylesheet_link_tag 'application', media: 'all' %>
  <%# javascript_include_tag 'landing' %>
</head>`,
		"app/assets/javascripts/home.js":          "//= require studies\n",
		"app/assets/javascripts/studies.es6":      "export const s = 1;",
		"app/assets/javascripts/landing.js":       "function landing() {}",
		"app/assets/stylesheets/application.scss": ".a { color: red; }",
	})
	svc := "orion"
	layout := filepath.Join(root, "app/views/layouts/app.html.erb")
	js := filepath.Join(root, "app/assets/javascripts")

	_, edges, unresolved := LinkSprocketsAssets(fileNodesFor(svc, files), map[string][]string{svc: files})
	require.Empty(t, unresolved)

	// `application` is ambiguous by name alone; the helper decides which tree.
	require.Equal(t, []string{
		filepath.Join(js, "home.js"),
		filepath.Join(root, "app/assets/stylesheets/application.scss"),
	}, assetTargets(edges, svc, layout))

	// The commented-out tag did not bind landing.js to the page.
	for _, e := range edges {
		require.NotContains(t, e.To, "landing.js")
	}

	// One more hop: layout → home.js → studies.es6.
	require.Equal(t, []string{filepath.Join(js, "studies.es6")},
		assetTargets(edges, svc, filepath.Join(js, "home.js")))

	for _, e := range edges {
		if e.From == svc+":"+layout+":file" {
			require.Equal(t, "include_tag", e.Meta["mechanism"])
		}
	}
}

// TestLinkSprocketsAssets_MintsEndpointNodes pins the K.5 lesson: an asset
// manifest declares nothing and an ERB layout declares nothing, so containment
// gives neither a file node — and both are endpoints here.
func TestLinkSprocketsAssets_MintsEndpointNodes(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/views/layouts/app.html.erb":        `<%= javascript_include_tag 'application' %>`,
		"app/assets/javascripts/application.js": "//= require studies\n",
		"app/assets/javascripts/studies.es6":    "export const s = 1;",
	})
	svc := "orion"
	leaf := filepath.Join(root, "app/assets/javascripts/studies.es6")

	// Only the leaf declares anything, exactly as containment would leave it.
	nodes := []graph.Node{
		{ID: svc + ":" + leaf + ":file", Type: graph.NodeTypeFile, Label: leaf, Service: svc, File: leaf},
		{ID: "service:" + svc, Type: graph.NodeTypeService, Label: svc},
	}

	newNodes, edges, _ := LinkSprocketsAssets(nodes, map[string][]string{svc: files})

	var minted []string
	for _, n := range newNodes {
		require.Equal(t, graph.NodeTypeFile, n.Type)
		minted = append(minted, n.File)
	}
	sort.Strings(minted)
	require.Equal(t, []string{
		filepath.Join(root, "app/assets/javascripts/application.js"),
		filepath.Join(root, "app/views/layouts/app.html.erb"),
	}, minted)

	// Both hops exist, so the backward walk from the leaf reaches the layout.
	require.Equal(t, []string{leaf},
		assetTargets(edges, svc, filepath.Join(root, "app/assets/javascripts/application.js")))
	require.Len(t, assetTargets(edges, svc, filepath.Join(root, "app/views/layouts/app.html.erb")), 1)

	// Every minted file still hangs off its service.
	var contains int
	for _, e := range edges {
		if e.Type == graph.EdgeTypeContains && e.From == "service:"+svc {
			contains++
		}
	}
	require.Equal(t, 2, contains)
}

// TestLinkSprocketsAssets_LinkDirectiveExtensionFilter: the precompile manifest
// enumerates a directory, optionally filtered by extension.
func TestLinkSprocketsAssets_LinkDirectiveExtensionFilter(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/assets/config/manifest.js": `//= link_directory ../javascripts .js
//= link_tree ../images
`,
		"app/assets/javascripts/a.js":     "function a() {}",
		"app/assets/javascripts/b.es6":    "export const b = 1;",
		"app/assets/javascripts/sub/c.js": "function c() {}",
	})
	svc := "orion"
	js := filepath.Join(root, "app/assets/javascripts")

	_, edges, unresolved := LinkSprocketsAssets(fileNodesFor(svc, files), map[string][]string{svc: files})

	// .js filter excludes the .es6 sibling; link_directory does not recurse.
	require.Equal(t, []string{filepath.Join(js, "a.js")},
		assetTargets(edges, svc, filepath.Join(root, "app/assets/config/manifest.js")))

	// The images tree holds no indexed file: ledgered rather than dropped.
	require.Len(t, unresolved, 1)
	require.Equal(t, "../images", unresolved[0].Name)
}

// TestLinkSprocketsAssets_NoAssetTree: a service with no app/assets tree is not
// a Sprockets app, and a stray `//=` comment in its JS must not mint anything.
func TestLinkSprocketsAssets_NoAssetTree(t *testing.T) {
	t.Parallel()
	_, files := stylesheetFixture(t, map[string]string{
		"app/javascript/entry.js": "//= require ./other\n",
		"app/javascript/other.js": "export const o = 1;",
	})
	svc := "orion"
	newNodes, edges, unresolved := LinkSprocketsAssets(fileNodesFor(svc, files), map[string][]string{svc: files})
	require.Empty(t, newNodes)
	require.Empty(t, edges)
	require.Empty(t, unresolved)
}

func TestLinkSprocketsAssets_Deterministic(t *testing.T) {
	t.Parallel()
	_, aFiles := stylesheetFixture(t, map[string]string{
		"app/assets/javascripts/application.js": "//= require_tree ./mod\n",
		"app/assets/javascripts/mod/a.js":       "function a() {}",
		"app/assets/javascripts/mod/b.js":       "function b() {}",
	})
	_, bFiles := stylesheetFixture(t, map[string]string{
		"app/views/layouts/x.html.erb":   `<%= javascript_include_tag "main" %>`,
		"app/assets/javascripts/main.js": "function main() {}",
	})
	svcFiles := map[string][]string{"orion": aFiles, "willow": bFiles}
	nodes := append(fileNodesFor("orion", aFiles), fileNodesFor("willow", bFiles)...)

	firstNodes, firstEdges, firstUnres := LinkSprocketsAssets(nodes, svcFiles)
	for i := 0; i < 5; i++ {
		n, e, u := LinkSprocketsAssets(nodes, svcFiles)
		require.Equal(t, firstNodes, n)
		require.Equal(t, firstEdges, e)
		require.Equal(t, firstUnres, u)
	}
	require.Len(t, firstEdges, 3)
}
