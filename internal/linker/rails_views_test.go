package linker

import (
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/require"
)

// rendersFrom returns the target files of every renders edge leaving a node.
func rendersFrom(edges []graph.Edge, fromID string) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeRenders && e.From == fromID {
			out = append(out, e.To)
		}
	}
	sort.Strings(out)
	return out
}

func fileID(svc, file string) string { return svc + ":" + file + ":file" }

// TestLinkRailsViews_PartialGraph is the worked example: a qualified partial, a
// directory-relative one, a collection, and a layout — plus the three-level
// nesting the phase asks for, which falls out because the edge is file→file.
func TestLinkRailsViews_PartialGraph(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
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
	svc := "nextGen"
	v := func(rel string) string { return fileID(svc, filepath.Join(root, rel)) }

	_, edges, unresolved := LinkRailsViews(fileNodesFor(svc, files), map[string][]string{svc: files})

	require.Equal(t, []string{
		v("app/views/shared/_nav_bar.html.erb"),
		v("app/views/studies/_row.html.erb"), // unqualified: own directory first
	}, rendersFrom(edges, v("app/views/studies/index.html.erb")))

	// Nesting is transitive with no extra machinery: three levels deep.
	require.Equal(t, []string{v("app/views/studies/_cell.html.erb")},
		rendersFrom(edges, v("app/views/studies/_row.html.erb")))
	require.Equal(t, []string{v("app/views/shared/_logo.html.erb")},
		rendersFrom(edges, v("app/views/shared/_nav_bar.html.erb")))

	// The commented-out render bound nothing.
	for _, e := range edges {
		require.NotContains(t, e.To, "_dead")
	}

	// `render @study` is dynamic: ledgered, never guessed (phases.md #12).
	require.Len(t, unresolved, 1)
	require.Equal(t, "erb_render_dynamic", unresolved[0].Kind)
	require.Equal(t, "@study", unresolved[0].Name)

	e := edgeBetween(t, edges, svc,
		filepath.Join(root, "app/views/studies/index.html.erb"),
		filepath.Join(root, "app/views/studies/_row.html.erb"))
	require.Equal(t, "true", e.Meta["collection"])
	require.Equal(t, graph.ConfidenceStatic, e.Confidence)
}

// TestLinkRailsViews_ControllerConvention: the action names no template, so the
// convention does. This is the edge that connects http_handler to view.
func TestLinkRailsViews_ControllerConvention(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
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
	svc := "nextGen"
	ctrl := filepath.Join(root, "app/controllers/studies_controller.rb")
	action := func(name string, line, end int) graph.Node {
		return graph.Node{
			ID: svc + ":" + ctrl + ":function:" + name, Type: graph.NodeTypeFunction,
			Label: name, Service: svc, File: ctrl, Line: line,
			Meta: map[string]string{"class": "StudiesController", "end_line": strconv.Itoa(end)},
		}
	}
	nodes := append(fileNodesFor(svc, files),
		action("index", 2, 4), action("show", 6, 8), action("create", 10, 12), action("set_study", 16, 18))

	_, edges, _ := LinkRailsViews(nodes, map[string][]string{svc: files})

	// Convention.
	require.Equal(t, []string{fileID(svc, filepath.Join(root, "app/views/studies/index.html.erb"))},
		rendersFrom(edges, action("index", 2, 4).ID))

	// Explicit render wins, and a controller's bare spec is a *template*, so it
	// resolves to detail.html.erb — not to show.html.erb, and not to a partial.
	require.Equal(t, []string{fileID(svc, filepath.Join(root, "app/views/studies/detail.html.erb"))},
		rendersFrom(edges, action("show", 6, 8).ID))

	// `render json:` states there is no view; a private helper has none either.
	require.Empty(t, rendersFrom(edges, action("create", 10, 12).ID))
	require.Empty(t, rendersFrom(edges, action("set_study", 16, 18).ID))
}

// TestLinkRailsViews_ControllerLayoutAndSymbol: two spellings whose resolution
// differs from a template's. `layout: "x"` in a controller means
// app/views/layouts/x — in a view the same keyword means an ordinary partial —
// and `render :edit` names a template in the controller's own directory.
func TestLinkRailsViews_ControllerLayoutAndSymbol(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
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
	svc := "nextGen"
	ctrl := filepath.Join(root, "app/controllers/execution_items_controller.rb")
	action := func(name string, line, end int) graph.Node {
		return graph.Node{
			ID: svc + ":" + ctrl + ":function:" + name, Type: graph.NodeTypeFunction,
			Label: name, Service: svc, File: ctrl, Line: line,
			Meta: map[string]string{"class": "ExecutionItemsController", "end_line": strconv.Itoa(end)},
		}
	}
	nodes := append(fileNodesFor(svc, files), action("index", 2, 4), action("update", 6, 8))

	_, edges, unresolved := LinkRailsViews(nodes, map[string][]string{svc: files})

	// The layout does not replace the convention template — it wraps it.
	require.Equal(t, []string{
		fileID(svc, filepath.Join(root, "app/views/execution_items/index.html.erb")),
		fileID(svc, filepath.Join(root, "app/views/layouts/sidebar_layout.html.erb")),
	}, rendersFrom(edges, action("index", 2, 4).ID))
	// `layout: false` names nothing at all — no edge and no ledger entry.
	require.Equal(t, []string{fileID(svc, filepath.Join(root, "app/views/execution_items/edit.html.erb"))},
		rendersFrom(edges, action("update", 6, 8).ID))
	require.Empty(t, unresolved)
}

// TestLinkRailsViews_ReactComponentRidesTheGlobalRegistry pins the resolution
// authority: window.X, not the containers/ path convention. nextGen mounts two
// components from app/javascript/components/, which the path rule cannot reach.
func TestLinkRailsViews_ReactComponentRidesTheGlobalRegistry(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/views/container_types/index.html.erb": `<div>
  <%= react_component("ContainerTypesContainer", { container_types: @container_types }) %>
  <%= react_component("LinkIcon") %>
  <%= react_component(dynamic_name) %>
</div>`,
		"app/javascript/containers/ContainerTypesContainer.jsx": `export default function ContainerTypesContainer() {}`,
		"app/javascript/components/common/LinkIcon.jsx":         `export default function LinkIcon() {}`,
	})
	svc := "nextGen"
	view := filepath.Join(root, "app/views/container_types/index.html.erb")
	jsx := filepath.Join(root, "app/javascript/containers/ContainerTypesContainer.jsx")
	icon := filepath.Join(root, "app/javascript/components/common/LinkIcon.jsx")

	nodes := append(fileNodesFor(svc, files),
		graph.Node{ID: "fn:ctc", Type: graph.NodeTypeFunction, Label: "ContainerTypesContainer", Service: svc, File: jsx, Line: 1},
		graph.Node{ID: "glob:ctc", Type: graph.NodeTypeVariable, Label: "ContainerTypesContainer", Service: svc, File: jsx, Line: 9,
			Meta: map[string]string{"global_symbol": "ContainerTypesContainer", "scope": "global"}},
		graph.Node{ID: "fn:icon", Type: graph.NodeTypeFunction, Label: "LinkIcon", Service: svc, File: icon, Line: 1},
		graph.Node{ID: "glob:icon", Type: graph.NodeTypeVariable, Label: "LinkIcon", Service: svc, File: icon, Line: 9,
			Meta: map[string]string{"global_symbol": "LinkIcon", "scope": "global"}},
		// A test file registers the same name; it is not the mounted component.
		graph.Node{ID: "fn:ctc_test", Type: graph.NodeTypeFunction, Label: "ContainerTypesContainer", Service: svc,
			File: jsx + ".test.jsx", Line: 3, Meta: map[string]string{"is_test": "true"}},
	)

	newNodes, edges, unresolved := LinkRailsViews(nodes, map[string][]string{svc: files})

	// The mount point is a node of its own: Tier K.4 binds listeners to it.
	var elements []string
	for _, n := range newNodes {
		if n.Type == graph.NodeTypeElement {
			elements = append(elements, n.Label)
			require.Equal(t, view, n.File)
		}
	}
	sort.Strings(elements)
	require.Equal(t, []string{
		"span[data-react-class=ContainerTypesContainer]",
		"span[data-react-class=LinkIcon]",
	}, elements)

	impl := map[string]string{}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeComponentImpl {
			impl[e.Meta["component"]] = e.To
		}
	}
	require.Equal(t, map[string]string{
		"ContainerTypesContainer": "fn:ctc",
		"LinkIcon":                "fn:icon", // outside containers/: only the registry finds it
	}, impl)

	// The view reaches the JSX in two hops, through the addressable span.
	el := rendersFrom(edges, fileID(svc, view))
	require.Len(t, el, 2)

	require.Len(t, unresolved, 1)
	require.Equal(t, "react_component_dynamic", unresolved[0].Kind)
}

// TestLinkRailsViews_FormatFanout: `render "index"` with both an HTML and a JS
// template names both. Rails picks by request format; the graph cannot know
// which request, and first-match would be the fan-out bug (phases.md #1).
func TestLinkRailsViews_FormatFanout(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/views/agent_nodes/show.html.erb": `<%= render "row" %>`,
		"app/views/agent_nodes/_row.html.erb": `<tr></tr>`,
		"app/views/agent_nodes/_row.js.erb":   `$("#row").html("");`,
	})
	svc := "nextGen"
	_, edges, _ := LinkRailsViews(fileNodesFor(svc, files), map[string][]string{svc: files})

	require.Equal(t, []string{
		fileID(svc, filepath.Join(root, "app/views/agent_nodes/_row.html.erb")),
		fileID(svc, filepath.Join(root, "app/views/agent_nodes/_row.js.erb")),
	}, rendersFrom(edges, fileID(svc, filepath.Join(root, "app/views/agent_nodes/show.html.erb"))))
}

// TestLinkRailsViews_MintsEndpointNodes: a template that declares nothing gets
// no file node from containment, and it is exactly what this pass points at
// (the K.5 / K.3 lesson, third recurrence).
func TestLinkRailsViews_MintsEndpointNodes(t *testing.T) {
	t.Parallel()
	root, files := stylesheetFixture(t, map[string]string{
		"app/views/studies/index.html.erb": `<%= render "shared/nav" %>`,
		"app/views/shared/_nav.html.erb":   `<nav></nav>`,
	})
	svc := "nextGen"
	nodes := []graph.Node{{ID: "service:" + svc, Type: graph.NodeTypeService, Label: svc}}

	newNodes, edges, _ := LinkRailsViews(nodes, map[string][]string{svc: files})

	var minted []string
	var contains int
	for _, n := range newNodes {
		if n.Type == graph.NodeTypeFile {
			minted = append(minted, n.File)
			require.Equal(t, "erb", n.Language)
		}
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeContains {
			contains++
		}
	}
	sort.Strings(minted)
	require.Equal(t, []string{
		filepath.Join(root, "app/views/shared/_nav.html.erb"),
		filepath.Join(root, "app/views/studies/index.html.erb"),
	}, minted)
	require.Equal(t, 2, contains)
}

// TestLinkRailsViews_NoViewTree: a service with no app/views is not a Rails
// app, and a `render` call in its JS must mint nothing.
func TestLinkRailsViews_NoViewTree(t *testing.T) {
	t.Parallel()
	_, files := stylesheetFixture(t, map[string]string{
		"src/App.jsx": `ReactDOM.render(<App />, root)`,
	})
	svc := "dsw-agent"
	n, e, u := LinkRailsViews(fileNodesFor(svc, files), map[string][]string{svc: files})
	require.Empty(t, n)
	require.Empty(t, e)
	require.Empty(t, u)
}

func TestLinkRailsViews_Deterministic(t *testing.T) {
	t.Parallel()
	_, aFiles := stylesheetFixture(t, map[string]string{
		"app/views/a/index.html.erb":   "<%= render \"shared/x\" %>\n<%= render \"shared/y\" %>",
		"app/views/shared/_x.html.erb": "<i></i>",
		"app/views/shared/_y.html.erb": "<b></b>",
	})
	_, bFiles := stylesheetFixture(t, map[string]string{
		"app/views/b/show.html.erb": `<%= react_component("Widget") %>`,
	})
	svcFiles := map[string][]string{"nextGen": aFiles, "mysycamore": bFiles}
	nodes := append(fileNodesFor("nextGen", aFiles), fileNodesFor("mysycamore", bFiles)...)

	firstN, firstE, firstU := LinkRailsViews(nodes, svcFiles)
	for i := 0; i < 5; i++ {
		n, e, u := LinkRailsViews(nodes, svcFiles)
		require.Equal(t, firstN, n)
		require.Equal(t, firstE, e)
		require.Equal(t, firstU, u)
	}
	require.Len(t, firstE, 3) // two partials + the react_component mount point
}
