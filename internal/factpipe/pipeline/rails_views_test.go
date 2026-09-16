package pipeline

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier FX (FX.8.33, 2026-09-16). Ports internal/linker/rails_views_test.go's
// fixtures verbatim through pipeline.Run, replacing direct calls to the
// retired LinkRailsViews. rvFixture/fileNodesFor/rendersFrom/fileID mirror
// the retired test file's own helpers of the same name.

func rvFixture(t *testing.T, files map[string]string) (string, []string) {
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

func rvFileNodesFor(service string, files []string) []graph.Node {
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

func rvFileID(svc, file string) string { return svc + ":" + file + ":file" }

func rvRendersFrom(edges []graph.Edge, fromID string) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeRenders && e.From == fromID {
			out = append(out, e.To)
		}
	}
	sort.Strings(out)
	return out
}

func rvEdgeBetween(t *testing.T, edges []graph.Edge, svc, from, to string) graph.Edge {
	t.Helper()
	fromID, toID := rvFileID(svc, from), rvFileID(svc, to)
	for _, e := range edges {
		if e.From == fromID && e.To == toID {
			return e
		}
	}
	t.Fatalf("no edge %s -> %s", from, to)
	return graph.Edge{}
}

// rvRun loads the "rails_views" framework and runs it once, over nodes
// (always the WHOLE graph in production — a react_component mount's JSX
// implementation is routinely a different service) and one service's own
// files.
func rvRun(t *testing.T, nodes []graph.Node, files []string) Result {
	t.Helper()
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	fw := reg.ByName("rails_views")
	if fw == nil {
		t.Fatalf("rails_views framework not embedded")
	}
	res, err := Run([]*Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// rvRunMulti runs "rails_views" once per (files) entry in svcFiles, always
// passing the same whole-graph nodes, merging edges/nodes/unresolved — the
// internal/indexer/link_passes.go "rails_views" pass's own per-service loop.
func rvRunMulti(t *testing.T, nodes []graph.Node, svcFiles map[string][]string) Result {
	t.Helper()
	var merged Result
	svcs := make([]string, 0, len(svcFiles))
	for svc := range svcFiles {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	for _, svc := range svcs {
		res := rvRun(t, nodes, svcFiles[svc])
		merged.Nodes = append(merged.Nodes, res.Nodes...)
		merged.Edges = append(merged.Edges, res.Edges...)
		merged.Unresolved = append(merged.Unresolved, res.Unresolved...)
	}
	return merged
}

// TestRailsViews_PartialGraph is the worked example: a qualified partial, a
// directory-relative one, a collection, and a layout — plus the three-level
// nesting the phase asks for, which falls out because the edge is file→file.
func TestRailsViews_PartialGraph(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/studies/index.html.erb": `<h1>Studies</h1>
<%= render "shared/nav_bar" %>
<%= render partial: "row", collection: @studies %>
<%# render "shared/dead" %>
<%= render @study %>
`,
		"app/views/studies/_row.html.erb":    `<%= render "cell" %>`,
		"app/views/studies/_cell.html.erb":   `<span>cell</span>`,
		"app/views/shared/_nav_bar.html.erb": `<nav><%= render "shared/logo" %></nav>`,
		"app/views/shared/_logo.html.erb":    `<img>`,
		"app/views/shared/_dead.html.erb":    `<p>never rendered</p>`,
		"app/controllers/studies_controller.rb": `class StudiesController < ApplicationController
  def index
  end
end`,
	})
	svc := "orion"
	v := func(rel string) string { return rvFileID(svc, filepath.Join(root, rel)) }

	res := rvRun(t, rvFileNodesFor(svc, files), files)

	require := func(cond bool, msg string) {
		if !cond {
			t.Fatal(msg)
		}
	}
	got := rvRendersFrom(res.Edges, v("app/views/studies/index.html.erb"))
	want := []string{
		v("app/views/shared/_nav_bar.html.erb"),
		v("app/views/studies/_row.html.erb"),
	}
	if !equalStrings(got, want) {
		t.Fatalf("index renders = %v, want %v", got, want)
	}

	got = rvRendersFrom(res.Edges, v("app/views/studies/_row.html.erb"))
	if !equalStrings(got, []string{v("app/views/studies/_cell.html.erb")}) {
		t.Fatalf("_row renders = %v", got)
	}
	got = rvRendersFrom(res.Edges, v("app/views/shared/_nav_bar.html.erb"))
	if !equalStrings(got, []string{v("app/views/shared/_logo.html.erb")}) {
		t.Fatalf("_nav_bar renders = %v", got)
	}

	for _, e := range res.Edges {
		require(!strings.Contains(e.To, "_dead"), "commented-out render bound something: "+e.To)
	}

	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "erb_render_dynamic" || res.Unresolved[0].Name != "@study" {
		t.Fatalf("unresolved = %+v", res.Unresolved)
	}

	e := rvEdgeBetween(t, res.Edges, svc,
		filepath.Join(root, "app/views/studies/index.html.erb"),
		filepath.Join(root, "app/views/studies/_row.html.erb"))
	if e.Meta["collection"] != "true" {
		t.Fatalf("collection meta = %q", e.Meta["collection"])
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Fatalf("confidence = %q", e.Confidence)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRailsViews_ControllerConvention: the action names no template, so the
// convention does. This is the edge that connects http_handler to view.
func TestRailsViews_ControllerConvention(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/controllers/studies_controller.rb": `class StudiesController < ApplicationController
  def index
    @studies = Study.all
  end

  def show
    render "detail"
  end

  def create
    render json: @study
  end

  private

  def set_study
    @study = Study.find(params[:id])
  end
end`,
		"app/views/studies/index.html.erb":  `<h1>index</h1>`,
		"app/views/studies/detail.html.erb": `<h1>detail</h1>`,
		"app/views/studies/show.html.erb":   `<h1>show</h1>`,
	})
	svc := "orion"
	ctrl := filepath.Join(root, "app/controllers/studies_controller.rb")
	action := func(name string, line, end int) graph.Node {
		return graph.Node{
			ID: svc + ":" + ctrl + ":function:" + name, Type: graph.NodeTypeFunction,
			Label: name, Service: svc, File: ctrl, Line: line,
			Meta: map[string]string{"class": "StudiesController", "end_line": strconv.Itoa(end)},
		}
	}
	nodes := append(rvFileNodesFor(svc, files),
		action("index", 2, 4), action("show", 6, 8), action("create", 10, 12), action("set_study", 16, 18))

	res := rvRun(t, nodes, files)

	if got, want := rvRendersFrom(res.Edges, action("index", 2, 4).ID), []string{rvFileID(svc, filepath.Join(root, "app/views/studies/index.html.erb"))}; !equalStrings(got, want) {
		t.Fatalf("index renders = %v, want %v", got, want)
	}
	if got, want := rvRendersFrom(res.Edges, action("show", 6, 8).ID), []string{rvFileID(svc, filepath.Join(root, "app/views/studies/detail.html.erb"))}; !equalStrings(got, want) {
		t.Fatalf("show renders = %v, want %v", got, want)
	}
	if got := rvRendersFrom(res.Edges, action("create", 10, 12).ID); len(got) != 0 {
		t.Fatalf("create renders = %v, want empty", got)
	}
	if got := rvRendersFrom(res.Edges, action("set_study", 16, 18).ID); len(got) != 0 {
		t.Fatalf("set_study renders = %v, want empty", got)
	}
}

// TestRailsViews_ControllerLayoutAndSymbol: two spellings whose resolution
// differs from a template's. `layout: "x"` in a controller means
// app/views/layouts/x — in a view the same keyword means an ordinary partial —
// and `render :edit` names a template in the controller's own directory.
func TestRailsViews_ControllerLayoutAndSymbol(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/controllers/execution_items_controller.rb": `class ExecutionItemsController < ApplicationController
  def index
    render layout: "sidebar_layout"
  end

  def update
    render :edit, layout: false
  end
end`,
		"app/views/layouts/sidebar_layout.html.erb":   `<html></html>`,
		"app/views/execution_items/index.html.erb":    `<h1>index</h1>`,
		"app/views/execution_items/edit.html.erb":     `<h1>edit</h1>`,
		"app/views/execution_items/_sidebar.html.erb": `<aside></aside>`,
	})
	svc := "orion"
	ctrl := filepath.Join(root, "app/controllers/execution_items_controller.rb")
	action := func(name string, line, end int) graph.Node {
		return graph.Node{
			ID: svc + ":" + ctrl + ":function:" + name, Type: graph.NodeTypeFunction,
			Label: name, Service: svc, File: ctrl, Line: line,
			Meta: map[string]string{"class": "ExecutionItemsController", "end_line": strconv.Itoa(end)},
		}
	}
	nodes := append(rvFileNodesFor(svc, files), action("index", 2, 4), action("update", 6, 8))

	res := rvRun(t, nodes, files)

	want := []string{
		rvFileID(svc, filepath.Join(root, "app/views/execution_items/index.html.erb")),
		rvFileID(svc, filepath.Join(root, "app/views/layouts/sidebar_layout.html.erb")),
	}
	if got := rvRendersFrom(res.Edges, action("index", 2, 4).ID); !equalStrings(got, want) {
		t.Fatalf("index renders = %v, want %v", got, want)
	}
	want = []string{rvFileID(svc, filepath.Join(root, "app/views/execution_items/edit.html.erb"))}
	if got := rvRendersFrom(res.Edges, action("update", 6, 8).ID); !equalStrings(got, want) {
		t.Fatalf("update renders = %v, want %v", got, want)
	}
	if len(res.Unresolved) != 0 {
		t.Fatalf("unresolved = %+v, want none", res.Unresolved)
	}
}

// TestRailsViews_ReactComponentRidesTheGlobalRegistry pins the resolution
// authority: window.X, not the containers/ path convention.
func TestRailsViews_ReactComponentRidesTheGlobalRegistry(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/container_types/index.html.erb": `<div>
  <%= react_component("ContainerTypesContainer", { container_types: @container_types }) %>
  <%= react_component("LinkIcon") %>
  <%= react_component(dynamic_name) %>
</div>`,
		"app/javascript/containers/ContainerTypesContainer.jsx": `export default function ContainerTypesContainer() {}`,
		"app/javascript/components/common/LinkIcon.jsx":         `export default function LinkIcon() {}`,
	})
	svc := "orion"
	view := filepath.Join(root, "app/views/container_types/index.html.erb")
	jsx := filepath.Join(root, "app/javascript/containers/ContainerTypesContainer.jsx")
	icon := filepath.Join(root, "app/javascript/components/common/LinkIcon.jsx")

	nodes := append(rvFileNodesFor(svc, files),
		graph.Node{ID: "fn:ctc", Type: graph.NodeTypeFunction, Label: "ContainerTypesContainer", Service: svc, File: jsx, Line: 1},
		graph.Node{ID: "glob:ctc", Type: graph.NodeTypeVariable, Label: "ContainerTypesContainer", Service: svc, File: jsx, Line: 9,
			Meta: map[string]string{"global_symbol": "ContainerTypesContainer", "scope": "global"}},
		graph.Node{ID: "fn:icon", Type: graph.NodeTypeFunction, Label: "LinkIcon", Service: svc, File: icon, Line: 1},
		graph.Node{ID: "glob:icon", Type: graph.NodeTypeVariable, Label: "LinkIcon", Service: svc, File: icon, Line: 9,
			Meta: map[string]string{"global_symbol": "LinkIcon", "scope": "global"}},
		graph.Node{ID: "fn:ctc_test", Type: graph.NodeTypeFunction, Label: "ContainerTypesContainer", Service: svc,
			File: jsx + ".test.jsx", Line: 3, Meta: map[string]string{"is_test": "true"}},
	)

	res := rvRun(t, nodes, files)

	var elements []string
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeElement {
			elements = append(elements, n.Label)
			if n.File != view {
				t.Fatalf("element file = %q, want %q", n.File, view)
			}
		}
	}
	sort.Strings(elements)
	want := []string{
		"span[data-react-class=ContainerTypesContainer]",
		"span[data-react-class=LinkIcon]",
	}
	if !equalStrings(elements, want) {
		t.Fatalf("elements = %v, want %v", elements, want)
	}

	impl := map[string]string{}
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeComponentImpl {
			impl[e.Meta["component"]] = e.To
		}
	}
	if impl["ContainerTypesContainer"] != "fn:ctc" || impl["LinkIcon"] != "fn:icon" {
		t.Fatalf("impl = %v", impl)
	}

	if got := rvRendersFrom(res.Edges, rvFileID(svc, view)); len(got) != 2 {
		t.Fatalf("view renders %d targets, want 2: %v", len(got), got)
	}

	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "react_component_dynamic" {
		t.Fatalf("unresolved = %+v", res.Unresolved)
	}
}

// TestRailsViews_FormatFanout: `render "index"` with both an HTML and a JS
// template names both.
func TestRailsViews_FormatFanout(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/agent_nodes/show.html.erb": `<%= render "row" %>`,
		"app/views/agent_nodes/_row.html.erb": `<tr></tr>`,
		"app/views/agent_nodes/_row.js.erb":   `$("#row").html("");`,
	})
	svc := "orion"
	res := rvRun(t, rvFileNodesFor(svc, files), files)

	want := []string{
		rvFileID(svc, filepath.Join(root, "app/views/agent_nodes/_row.html.erb")),
		rvFileID(svc, filepath.Join(root, "app/views/agent_nodes/_row.js.erb")),
	}
	if got := rvRendersFrom(res.Edges, rvFileID(svc, filepath.Join(root, "app/views/agent_nodes/show.html.erb"))); !equalStrings(got, want) {
		t.Fatalf("show renders = %v, want %v", got, want)
	}
}

// TestRailsViews_MintsEndpointNodes: a template that declares nothing gets
// no file node from containment, and it is exactly what this pass points at.
func TestRailsViews_MintsEndpointNodes(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/studies/index.html.erb": `<%= render "shared/nav" %>`,
		"app/views/shared/_nav.html.erb":   `<nav></nav>`,
	})
	svc := "orion"
	nodes := []graph.Node{{ID: "service:" + svc, Type: graph.NodeTypeService, Label: svc}}

	res := rvRun(t, nodes, files)

	var minted []string
	var contains int
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeFile {
			minted = append(minted, n.File)
			if n.Language != "erb" {
				t.Fatalf("language = %q, want erb", n.Language)
			}
		}
	}
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeContains {
			contains++
		}
	}
	sort.Strings(minted)
	want := []string{
		filepath.Join(root, "app/views/shared/_nav.html.erb"),
		filepath.Join(root, "app/views/studies/index.html.erb"),
	}
	if !equalStrings(minted, want) {
		t.Fatalf("minted = %v, want %v", minted, want)
	}
	if contains != 2 {
		t.Fatalf("contains edges = %d, want 2", contains)
	}
}

// TestRailsViews_NoViewTree: a service with no app/views is not a Rails app.
func TestRailsViews_NoViewTree(t *testing.T) {
	t.Parallel()
	_, files := rvFixture(t, map[string]string{
		"src/App.jsx": `ReactDOM.render(<App />, root)`,
	})
	svc := "maple-agent"
	res := rvRun(t, rvFileNodesFor(svc, files), files)
	if len(res.Nodes) != 0 || len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Fatalf("res = %+v, want empty", res)
	}
}

func TestRailsViews_Deterministic(t *testing.T) {
	t.Parallel()
	_, aFiles := rvFixture(t, map[string]string{
		"app/views/a/index.html.erb":   "<%= render \"shared/x\" %>\n<%= render \"shared/y\" %>",
		"app/views/shared/_x.html.erb": "<i></i>",
		"app/views/shared/_y.html.erb": "<b></b>",
	})
	_, bFiles := rvFixture(t, map[string]string{
		"app/views/b/show.html.erb": `<%= react_component("Widget") %>`,
	})
	svcFiles := map[string][]string{"orion": aFiles, "willow": bFiles}
	nodes := append(rvFileNodesFor("orion", aFiles), rvFileNodesFor("willow", bFiles)...)

	first := rvRunMulti(t, nodes, svcFiles)
	for i := 0; i < 5; i++ {
		res := rvRunMulti(t, nodes, svcFiles)
		if len(res.Edges) != len(first.Edges) || len(res.Nodes) != len(first.Nodes) || len(res.Unresolved) != len(first.Unresolved) {
			t.Fatalf("run %d not deterministic: %d/%d/%d vs %d/%d/%d",
				i, len(res.Edges), len(res.Nodes), len(res.Unresolved),
				len(first.Edges), len(first.Nodes), len(first.Unresolved))
		}
	}
	if len(first.Edges) != 3 {
		t.Fatalf("edges = %d, want 3 (two partials + the react_component mount point)", len(first.Edges))
	}
}

// TestRailsViews_ReactComponentCrossService: the `react_component` mount
// lives in the Rails service but the JSX it names lives in a sibling `js`
// service. The component_impl edge must still be wired.
func TestRailsViews_ReactComponentCrossService(t *testing.T) {
	t.Parallel()
	root, all := rvFixture(t, map[string]string{
		"app/views/apps/index.html.erb":               `<%= react_component("AppsContainer") %>`,
		"app/javascript/containers/AppsContainer.jsx": `export function AppsContainer() {}`,
	})
	var erb, jsx []string
	for _, f := range all {
		if strings.Contains(f, "/app/javascript/") {
			jsx = append(jsx, f)
		} else {
			erb = append(erb, f)
		}
	}
	jsxFile := filepath.Join(root, "app/javascript/containers/AppsContainer.jsx")
	nodes := append(rvFileNodesFor("orion", erb), rvFileNodesFor("js", jsx)...)
	nodes = append(nodes,
		graph.Node{ID: "js:fn:apps", Type: graph.NodeTypeFunction, Label: "AppsContainer", Service: "js", File: jsxFile, Line: 1},
		graph.Node{ID: "js:glob:apps", Type: graph.NodeTypeVariable, Label: "AppsContainer", Service: "js", File: jsxFile, Line: 1,
			Meta: map[string]string{"global_symbol": "AppsContainer", "scope": "global"}},
	)

	res := rvRunMulti(t, nodes, map[string][]string{"orion": erb, "js": jsx})

	var got string
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeComponentImpl {
			got = e.To
		}
	}
	if got != "js:fn:apps" {
		t.Fatalf("component_impl target = %q, want js:fn:apps", got)
	}
}

// TestRailsViews_ControllerLayoutChain: a class-level `layout` on the
// controller (and inherited from ApplicationController) resolves to
// app/views/layouts/<name>, and the layout is wired back to the action's own
// template via `yield`.
func TestRailsViews_ControllerLayoutChain(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/controllers/application_controller.rb": `class ApplicationController < ActionController::Base
  layout "application"
end`,
		"app/controllers/apps_controller.rb": `class AppsController < ApplicationController
  layout "sidebar_layout", except: %i[raw]

  def index
  end

  def raw
  end
end`,
		"app/controllers/pages_controller.rb": `class PagesController < ApplicationController
  def home
  end
end`,
		"app/views/layouts/application.html.erb":    `<%= yield %>`,
		"app/views/layouts/sidebar_layout.html.erb": `<%= yield %>`,
		"app/views/apps/index.html.erb":             `<h1>apps</h1>`,
		"app/views/apps/raw.html.erb":               `<h1>raw</h1>`,
		"app/views/pages/home.html.erb":             `<h1>home</h1>`,
	})
	svc := "orion"
	act := func(ctrl, cls, name string, line, end int) graph.Node {
		f := filepath.Join(root, "app/controllers/"+ctrl)
		return graph.Node{
			ID: svc + ":" + f + ":function:" + name, Type: graph.NodeTypeFunction, Label: name,
			Service: svc, File: f, Line: line,
			Meta: map[string]string{"class": cls, "end_line": strconv.Itoa(end)},
		}
	}
	nodes := append(rvFileNodesFor(svc, files),
		act("apps_controller.rb", "AppsController", "index", 4, 5),
		act("apps_controller.rb", "AppsController", "raw", 7, 8),
		act("pages_controller.rb", "PagesController", "home", 2, 3),
	)

	res := rvRun(t, nodes, files)

	lid := func(name string) string {
		return rvFileID(svc, filepath.Join(root, "app/views/layouts/"+name+".html.erb"))
	}
	vid := func(p string) string { return rvFileID(svc, filepath.Join(root, "app/views/"+p)) }

	contains := func(xs []string, x string) bool {
		for _, v := range xs {
			if v == x {
				return true
			}
		}
		return false
	}

	if !contains(rvRendersFrom(res.Edges, act("apps_controller.rb", "AppsController", "index", 4, 5).ID), lid("sidebar_layout")) {
		t.Fatal("index missing sidebar_layout")
	}
	if !contains(rvRendersFrom(res.Edges, act("apps_controller.rb", "AppsController", "raw", 7, 8).ID), lid("application")) {
		t.Fatal("raw missing application layout")
	}
	if !contains(rvRendersFrom(res.Edges, act("pages_controller.rb", "PagesController", "home", 2, 3).ID), lid("application")) {
		t.Fatal("home missing application layout")
	}
	if !contains(rvRendersFrom(res.Edges, lid("sidebar_layout")), vid("apps/index.html.erb")) {
		t.Fatal("sidebar_layout missing yield to apps/index")
	}
	if !contains(rvRendersFrom(res.Edges, lid("application")), vid("pages/home.html.erb")) {
		t.Fatal("application missing yield to pages/home")
	}
	if len(res.Unresolved) != 0 {
		t.Fatalf("unresolved = %+v, want none", res.Unresolved)
	}
}

// TestRailsViews_ComponentImplThroughBarrel: a data-react-class name that a
// barrel module only re-exports must resolve one hop further, to the real
// component — not stop at the terminal barrel variable. SPA.6.
func TestRailsViews_ComponentImplThroughBarrel(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/dash/index.html.erb": `<%= react_component("Foo") %>`,
		"react/react_exports.js":        `import Foo from "./components/Foo";` + "\n" + `window.Foo = Foo;` + "\n",
		"react/components/Foo.jsx":      `export default class Foo extends React.Component { render() { return null; } }` + "\n",
	})
	svc := "cedar"
	barrel := filepath.Join(root, "react/react_exports.js")
	impl := filepath.Join(root, "react/components/Foo.jsx")

	nodes := append(rvFileNodesFor(svc, files),
		graph.Node{ID: "barrel:Foo", Type: graph.NodeTypeVariable, Label: "Foo", Service: svc, File: barrel, Line: 2,
			Meta: map[string]string{"global_symbol": "Foo", "scope": "global"}},
		graph.Node{ID: "impl:Foo", Type: graph.NodeTypeVariable, Label: "Foo", Service: svc, File: impl, Line: 1,
			Meta: map[string]string{"component": "true", "hoc": "app_local"}},
	)

	res := rvRun(t, nodes, files)

	var got []graph.Edge
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeComponentImpl {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("component_impl edges = %d, want 1", len(got))
	}
	if got[0].To != "impl:Foo" {
		t.Fatalf("component_impl must land on the real component, not the barrel var: got %q", got[0].To)
	}
	if got[0].Meta["via"] != "barrel" {
		t.Fatalf("via = %q, want barrel", got[0].Meta["via"])
	}
}

// TestRailsViews_ComponentImplDirectStillWorks: a component registered in
// its own file (no barrel indirection) resolves exactly as before, with no
// via=barrel tag.
func TestRailsViews_ComponentImplDirectStillWorks(t *testing.T) {
	t.Parallel()
	root, files := rvFixture(t, map[string]string{
		"app/views/dash/index.html.erb": `<%= react_component("Bar") %>`,
		"react/components/Bar.jsx":      `function Bar() {} window.Bar = Bar;` + "\n",
	})
	svc := "cedar"
	impl := filepath.Join(root, "react/components/Bar.jsx")

	nodes := append(rvFileNodesFor(svc, files),
		graph.Node{ID: "fn:Bar", Type: graph.NodeTypeFunction, Label: "Bar", Service: svc, File: impl, Line: 1},
		graph.Node{ID: "glob:Bar", Type: graph.NodeTypeVariable, Label: "Bar", Service: svc, File: impl, Line: 1,
			Meta: map[string]string{"global_symbol": "Bar", "scope": "global"}},
	)

	res := rvRun(t, nodes, files)

	var got []graph.Edge
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeComponentImpl {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("component_impl edges = %d, want 1", len(got))
	}
	if got[0].To != "fn:Bar" {
		t.Fatalf("component_impl target = %q, want fn:Bar", got[0].To)
	}
	if got[0].Meta["via"] != "" {
		t.Fatalf("via = %q, want empty", got[0].Meta["via"])
	}
}
