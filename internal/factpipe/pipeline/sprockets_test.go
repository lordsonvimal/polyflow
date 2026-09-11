package pipeline_test

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// sprocketsFrameworks pulls the two embedded frameworks under test, bypassing
// the package.json/Gemfile gate the real indexer applies (Active) — this
// exercises Run directly, the same shortcut TestRunGatedOutFrameworkProducesNoEdges's
// siblings use.
func sprocketsFrameworks(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	var out []*pipeline.Framework
	for _, fw := range reg.All() {
		if fw.Name == "sprockets_directives" || fw.Name == "sprockets_includes" {
			out = append(out, fw)
		}
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 sprockets frameworks, got %d", len(out))
	}
	return out
}

func fileNode(id, path, svc string) graph.Node {
	return graph.Node{ID: id, Type: graph.NodeTypeFile, File: path, Service: svc}
}

// TestSprocketsAssetsEndToEnd exercises both halves of the migrated
// sprockets_assets pass (FX.8 resolve_path step 4) together: a JS manifest's
// header directives (require / require_tree / link_directory, one resolved
// one not) and an ERB layout's include tags (js + css, one resolved one
// not) all the way through extract -> resolve -> derive -> emit.
func TestSprocketsAssetsEndToEnd(t *testing.T) {
	const svc = "demo"
	jsSrc := []byte(
		"//= require jquery\n" +
			"//= require missing_lib\n" +
			"//= require_tree ./widgets\n" +
			"//= link_directory ./widgets .css\n" +
			"console.log(1);\n")
	erbSrc := []byte(
		`<%= javascript_include_tag "application" %>` +
			`<%= stylesheet_link_tag "application" %>` +
			`<%= javascript_include_tag "missing" %>`)

	files := []pipeline.ParsedFile{
		{Path: "app/assets/javascripts/application.js", Language: "javascript", Src: jsSrc},
		{Path: "app/views/layouts/application.html.erb", Language: "erb", Src: erbSrc},
	}

	paths := []string{
		"app/assets/javascripts/application.js",
		"app/assets/javascripts/jquery.js",
		"app/assets/javascripts/widgets/a.js",
		"app/assets/javascripts/widgets/b.css",
		"app/assets/stylesheets/application.css",
		"app/views/layouts/application.html.erb",
	}
	var nodes []graph.Node
	for _, p := range paths {
		nodes = append(nodes, fileNode(svc+":"+p+":file", p, svc))
	}
	snap := graph.Snapshot{Nodes: nodes, Files: paths}

	res, err := pipeline.Run(sprocketsFrameworks(t), files, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	byLabel := map[string]graph.Edge{}
	for _, e := range res.Edges {
		if e.Type != graph.EdgeTypeImports {
			t.Errorf("edge %s has type %s, want imports", e.ID, e.Type)
		}
		byLabel[e.From+"->"+e.To] = e
	}

	appJS := svc + ":app/assets/javascripts/application.js:file"
	jquery := svc + ":app/assets/javascripts/jquery.js:file"
	widgetA := svc + ":app/assets/javascripts/widgets/a.js:file"
	widgetB := svc + ":app/assets/javascripts/widgets/b.css:file"
	appCSS := svc + ":app/assets/stylesheets/application.css:file"
	layout := svc + ":app/views/layouts/application.html.erb:file"

	want := map[string]struct{ label, mechanism string }{
		appJS + "->" + jquery:  {"//= require jquery", "sprockets"},
		appJS + "->" + widgetA: {"//= require_tree ./widgets", "sprockets"},
		appJS + "->" + widgetB: {"//= require_tree ./widgets", "sprockets"},
		layout + "->" + appJS:  {"javascript_include_tag application", "include_tag"},
		layout + "->" + appCSS: {"stylesheet_link_tag application", "include_tag"},
	}
	// link_directory ./widgets .css hits only widgetB (ext-filtered), but its
	// From->To pair collides with require_tree's edge to the same target —
	// dedup keys on [from,to,label], and the two directives have different
	// labels, so both survive as distinct edges.
	linkDirKey := appJS + "->" + widgetB
	if e, ok := byLabel[linkDirKey]; !ok || e.Label != "//= require_tree ./widgets" {
		t.Errorf("missing/wrong require_tree edge to widgets/b.css: %+v", e)
	}
	var linkDirEdge *graph.Edge
	for i := range res.Edges {
		if res.Edges[i].From == appJS && res.Edges[i].To == widgetB && res.Edges[i].Label == "//= link_directory ./widgets" {
			linkDirEdge = &res.Edges[i]
		}
	}
	if linkDirEdge == nil {
		t.Errorf("missing link_directory edge to widgets/b.css among: %+v", res.Edges)
	} else if linkDirEdge.Meta["directive"] != "link_directory" {
		t.Errorf("link_directory edge meta = %+v", linkDirEdge.Meta)
	}

	for k, w := range want {
		e, ok := byLabel[k]
		if !ok {
			t.Errorf("missing edge %s among: %+v", k, res.Edges)
			continue
		}
		if e.Label != w.label {
			t.Errorf("edge %s label = %q, want %q", k, e.Label, w.label)
		}
		if e.Meta["mechanism"] != w.mechanism {
			t.Errorf("edge %s meta[mechanism] = %q, want %q", k, e.Meta["mechanism"], w.mechanism)
		}
	}

	var unresolvedKinds []string
	for _, u := range res.Unresolved {
		unresolvedKinds = append(unresolvedKinds, u.Kind+":"+u.Name)
	}
	sort.Strings(unresolvedKinds)
	wantUnresolved := []string{
		"sprockets_include_unresolved:missing",
		"sprockets_require_unresolved:missing_lib",
	}
	if len(unresolvedKinds) != len(wantUnresolved) {
		t.Fatalf("unresolved = %v, want %v", unresolvedKinds, wantUnresolved)
	}
	for i := range wantUnresolved {
		if unresolvedKinds[i] != wantUnresolved[i] {
			t.Errorf("unresolved[%d] = %q, want %q (full: %v)", i, unresolvedKinds[i], wantUnresolved[i], unresolvedKinds)
		}
	}
}
