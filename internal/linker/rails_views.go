package linker

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// LinkRailsViews connects the Rails view layer (Tier K.2):
//
//	UsersController#index --renders--> index.html.erb --renders--> _row.html.erb
//	                                        └--renders--> span[data-react-class=X]
//	                                                          └--component_impl--> X.jsx
//
// Three bindings, all of them conventions Rails resolves at runtime and nothing
// in the source states outright:
//
//  1. `render "shared/foo"` in a template names app/views/shared/_foo.html.erb.
//  2. A controller action with no explicit render names
//     app/views/<controller_path>/<action>.html.erb.
//  3. `react_component("Foo")` writes data-react-class="Foo", which
//     application.js mounts as window["Foo"] — the Tier Z global registry.
//
// Without them ERB is an island: 3,099 parsed nodes with 10 outgoing edges.
//
// Must run after LinkContainment. Most templates declare nothing a parser
// records, so their file nodes have to be minted here (see fileNodeIndex).
func LinkRailsViews(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	files := newFileNodeIndex(nodes)

	seen := map[string]bool{}
	addEdge := func(from, to, label string, et graph.EdgeType, meta map[string]string) {
		if from == to || from == "" || to == "" {
			return
		}
		id := fmt.Sprintf("%s:%s->%s", string(et), from, to)
		if seen[id] {
			return
		}
		seen[id] = true
		newEdges = append(newEdges, graph.Edge{
			ID:         id,
			From:       from,
			To:         to,
			Type:       et,
			Label:      label,
			Meta:       meta,
			Confidence: graph.ConfidenceStatic,
		})
	}

	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames) // bug-class #2: map order must never reach output

	for _, svc := range svcNames {
		idx := newViewIndex(serviceFiles[svc])
		if len(idx.byLogical) == 0 {
			continue // no app/views tree: not a Rails app
		}
		components := newComponentIndex(nodes, svc)

		u := linkTemplates(svc, idx, components, files, addEdge, &newNodes)
		unresolved = append(unresolved, u...)
		unresolved = append(unresolved, linkControllerActions(svc, idx, nodes, files, addEdge)...)
	}

	return append(newNodes, files.minted...), append(newEdges, files.mintedEdges...), unresolved
}

// ---------------------------------------------------------------------------
// view index
// ---------------------------------------------------------------------------

// viewIndex maps a Rails logical template path ("shared/_nav_bar") to the files
// that implement it.
//
// The value is a slice, not a single path, because `index.html.erb` and
// `index.js.erb` are both real answers to `render "index"` — Rails picks by
// request format, and the graph cannot know which request. Naming only the
// first would be the fan-out bug (phases.md #1).
type viewIndex struct {
	roots     []string            // every app/views directory in the service
	byLogical map[string][]string // "shared/_nav_bar" → absolute paths
	erbFiles  []string            // every template, sorted
}

func newViewIndex(files []string) *viewIndex {
	idx := &viewIndex{byLogical: map[string][]string{}}
	roots := map[string]bool{}

	for _, f := range files {
		root, rel, ok := splitViewRoot(f)
		if !ok {
			continue
		}
		roots[root] = true
		key := path.Join(filepath.Dir(rel), logicalBase(filepath.Base(rel)))
		idx.byLogical[key] = append(idx.byLogical[key], f)
		if strings.HasSuffix(f, ".erb") {
			idx.erbFiles = append(idx.erbFiles, f)
		}
	}
	for k := range idx.byLogical {
		sort.Strings(idx.byLogical[k])
	}
	for r := range roots {
		idx.roots = append(idx.roots, r)
	}
	sort.Strings(idx.roots)
	sort.Strings(idx.erbFiles)
	return idx
}

// splitViewRoot returns the `.../app/views` prefix of a template and its path
// below it. An engine or a mounted gem contributes its own views root, which is
// why this is a search rather than a single configured directory.
func splitViewRoot(file string) (root, rel string, ok bool) {
	dir := filepath.Dir(file)
	for {
		if filepath.Base(dir) == "views" && filepath.Base(filepath.Dir(dir)) == "app" {
			r, err := filepath.Rel(dir, file)
			if err != nil {
				return "", "", false
			}
			return dir, r, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// logicalBase strips the format/handler extension chain: `index.html.erb` and
// `index.js.erb` share the logical name `index`.
func logicalBase(base string) string {
	if i := strings.IndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base
}

// resolve turns a render spec into the templates it names.
//
// `from` is the file doing the rendering: an unqualified spec is looked up in
// its own directory first, which is the rule that makes `render "form"` mean
// different files in app/views/users and app/views/studies.
func (idx *viewIndex) resolve(from, spec string, partial bool) []string {
	dir, base := path.Split(spec)
	if partial {
		base = "_" + base
	}

	var keys []string
	if dir != "" {
		keys = append(keys, path.Join(dir, base))
	} else {
		if _, rel, ok := splitViewRoot(from); ok {
			keys = append(keys, path.Join(filepath.Dir(rel), base))
		}
		keys = append(keys, base)
	}
	for _, k := range keys {
		if hits := idx.byLogical[k]; len(hits) > 0 {
			return hits
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// templates: render + react_component
// ---------------------------------------------------------------------------

func linkTemplates(
	svc string,
	idx *viewIndex,
	components *componentIndex,
	files *fileNodeIndex,
	addEdge func(from, to, label string, et graph.EdgeType, meta map[string]string),
	newNodes *[]graph.Node,
) (unresolved []graph.UnresolvedRef) {
	for _, f := range idx.erbFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// Templates are read through the split: a `<%# render "x" %>` tag is a
		// commented-out helper, and its target is not loaded.
		_, ruby := railsview.SplitERB(src)
		fromID := ""
		ensureFrom := func() string {
			if fromID == "" {
				fromID = files.ensure(svc, f)
			}
			return fromID
		}

		for _, r := range railsview.ScanRenders(ruby) {
			if r.Dynamic {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: svc, File: f, Line: r.Line, Name: r.Spec, Kind: "erb_render_dynamic",
				})
				continue
			}
			// In a template the bare form names a partial; in a controller it
			// names a template. Same syntax, different default.
			targets := idx.resolve(f, r.Spec, r.Kind != railsview.RenderTemplate)
			if len(targets) == 0 {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: svc, File: f, Line: r.Line, Name: r.Spec, Kind: "erb_render_unresolved",
				})
				continue
			}
			meta := map[string]string{"mechanism": "render", "kind": r.Kind, "spec": r.Spec}
			if r.Collection {
				meta["collection"] = "true"
			}
			for _, t := range targets {
				addEdge(ensureFrom(), files.ensure(svc, t), "render "+r.Spec, graph.EdgeTypeRenders, meta)
			}
		}

		for _, rc := range railsview.ScanReactComponents(ruby) {
			if rc.Dynamic {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: svc, File: f, Line: rc.Line, Name: rc.Name, Kind: "react_component_dynamic",
				})
				continue
			}
			// The mount point is addressable in its own right: Tier K.4 binds
			// DOM listeners to elements, and this span is the seam between the
			// server-rendered page and the React tree.
			el := graph.Node{
				ID:       fmt.Sprintf("%s:%s:%s:span[data-react-class=%s]:%d", svc, f, graph.NodeTypeElement, rc.Name, rc.Line),
				Type:     graph.NodeTypeElement,
				Label:    fmt.Sprintf("span[data-react-class=%s]", rc.Name),
				Service:  svc,
				File:     f,
				Line:     rc.Line,
				Language: "erb",
				Meta:     map[string]string{"data-react-class": rc.Name, "mechanism": "react_component"},
			}
			*newNodes = append(*newNodes, el)
			addEdge(ensureFrom(), el.ID, "react_component "+rc.Name, graph.EdgeTypeRenders,
				map[string]string{"mechanism": "react_component", "component": rc.Name})

			impls := components.lookup(rc.Name)
			if len(impls) == 0 {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: svc, File: f, Line: rc.Line, Name: rc.Name, Kind: "react_component_unresolved",
				})
				continue
			}
			for _, impl := range impls {
				addEdge(el.ID, impl, "window."+rc.Name, graph.EdgeTypeComponentImpl,
					map[string]string{"mechanism": "data-react-class", "component": rc.Name})
			}
		}
	}
	return unresolved
}

// ---------------------------------------------------------------------------
// controllers
// ---------------------------------------------------------------------------

// linkControllerActions wires each controller action to the template it
// renders: the one it names explicitly, or the convention-named one.
//
// This is the edge that ends ERB's isolation — it is what puts a view on the
// path from an http_handler, and there is no syntax anywhere in the repo that
// states it.
func linkControllerActions(
	svc string,
	idx *viewIndex,
	nodes []graph.Node,
	files *fileNodeIndex,
	addEdge func(from, to, label string, et graph.EdgeType, meta map[string]string),
) (unresolved []graph.UnresolvedRef) {
	type action struct {
		id            string
		name          string
		line, endLine int
	}
	byFile := map[string][]action{}
	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc || !isControllerFile(n.File) {
			continue
		}
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		end := 0
		fmt.Sscanf(n.Meta["end_line"], "%d", &end)
		// A pattern-derived function node with no end_line is a call site, not a
		// declaration — a *call* pattern captures no body and so cannot record
		// where one ends (the @_def contract, see patterns/go/functions.yaml).
		// Its span is then unbounded, and every render below it in the file gets
		// attributed to it: `before_action :authenticate_user!` at line 7 was
		// claiming to render index.html.erb, 97 times across the fleet. Same
		// discriminator the matcher's Pass 2 uses for enclosing scopes.
		if n.Meta["pattern"] != "" && end == 0 {
			continue
		}
		byFile[n.File] = append(byFile[n.File], action{n.ID, n.Label, n.Line, end})
	}

	ctrlFiles := make([]string, 0, len(byFile))
	for f := range byFile {
		ctrlFiles = append(ctrlFiles, f)
	}
	sort.Strings(ctrlFiles) // bug-class #2

	for _, f := range ctrlFiles {
		acts := byFile[f]
		sort.Slice(acts, func(i, j int) bool { return acts[i].line < acts[j].line })

		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		renders := railsview.ScanRenders(src)
		ctrlPath, ok := controllerPath(f)
		if !ok {
			continue
		}

		for _, a := range acts {
			// A `layout:`-only render does *not* replace the convention: Rails
			// still renders <action>.html.erb, just inside that layout. Only a
			// named template or partial suppresses it.
			namedTemplate := false
			for _, r := range renders {
				if r.Line < a.line || (a.endLine > 0 && r.Line > a.endLine) {
					continue
				}
				if r.Kind != railsview.RenderLayout {
					namedTemplate = true
				}
				if r.Dynamic {
					unresolved = append(unresolved, graph.UnresolvedRef{
						Service: svc, File: f, Line: r.Line, Name: r.Spec, Kind: "erb_render_dynamic",
					})
					continue
				}
				// A controller's bare `render "x"` names a template, not a
				// partial — the opposite default from a view — and it resolves
				// against the controller's own view directory, which the file
				// path cannot supply because the controller does not live there.
				//
				// `layout:` is the other way round again: in a controller it
				// names app/views/layouts/x, while in a template the same
				// keyword names an ordinary partial.
				spec := r.Spec
				if !strings.Contains(spec, "/") {
					if r.Kind == railsview.RenderLayout {
						spec = path.Join("layouts", spec)
					} else {
						spec = path.Join(ctrlPath, spec)
					}
				}
				targets := idx.resolve(f, spec, r.Kind == railsview.RenderPartial)
				if len(targets) == 0 {
					unresolved = append(unresolved, graph.UnresolvedRef{
						Service: svc, File: f, Line: r.Line, Name: r.Spec, Kind: "erb_render_unresolved",
					})
					continue
				}
				for _, t := range targets {
					addEdge(a.id, files.ensure(svc, t), "render "+r.Spec, graph.EdgeTypeRenders,
						map[string]string{"mechanism": "explicit", "spec": r.Spec})
				}
			}
			if namedTemplate {
				continue
			}
			// Convention. Only emitted when the file exists, which is also what
			// keeps private helper methods out: `def set_user` has no view.
			for _, t := range idx.resolve(f, path.Join(ctrlPath, a.name), false) {
				addEdge(a.id, files.ensure(svc, t), a.name, graph.EdgeTypeRenders,
					map[string]string{"mechanism": "convention", "spec": path.Join(ctrlPath, a.name)})
			}
		}
	}
	return unresolved
}

func isControllerFile(file string) bool {
	return strings.HasSuffix(file, "_controller.rb") && strings.Contains(filepath.ToSlash(file), "/app/controllers/")
}

// controllerPath is the view directory an action's template lives in:
// app/controllers/admin/users_controller.rb → "admin/users".
func controllerPath(file string) (string, bool) {
	s := filepath.ToSlash(file)
	i := strings.LastIndex(s, "/app/controllers/")
	if i < 0 {
		return "", false
	}
	return strings.TrimSuffix(s[i+len("/app/controllers/"):], "_controller.rb"), true
}

// ---------------------------------------------------------------------------
// component registry
// ---------------------------------------------------------------------------

// componentIndex resolves a data-react-class name to the JSX that implements it.
//
// The authority is the Tier Z global registry (`window.Foo = Foo`), not the
// helper's `containers/<Name>.jsx` path convention, because the convention is
// only the *dev-mode* script tag. nextGen mounts LinkIcon and OnboardingTip
// from app/javascript/components/, which the path rule cannot reach and the
// registry can.
type componentIndex struct{ bySymbol map[string][]string }

func newComponentIndex(nodes []graph.Node, svc string) *componentIndex {
	// symbol → files that register it, and (file,label) → implementation node.
	regFiles := map[string][]string{}
	implID := map[string]string{}
	varID := map[string]string{}

	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc || n.Meta["is_test"] == "true" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeClass:
			key := n.File + "\x00" + n.Label
			if _, dup := implID[key]; !dup {
				implID[key] = n.ID
			}
		case graph.NodeTypeVariable:
			sym := n.Meta["global_symbol"]
			if sym == "" || n.Meta["scope"] != "global" {
				continue
			}
			regFiles[sym] = append(regFiles[sym], n.File)
			varID[sym+"\x00"+n.File] = n.ID
		}
	}

	idx := &componentIndex{bySymbol: map[string][]string{}}
	for sym, fs := range regFiles {
		sort.Strings(fs)
		for _, f := range fs {
			// Prefer the declaration over the assignment: `window.Foo = Foo`
			// registers a function defined in the same file, and that function
			// is what a caller wants to trace into.
			if id, ok := implID[f+"\x00"+sym]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			} else if id, ok := varID[sym+"\x00"+f]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			}
		}
	}
	return idx
}

func (c *componentIndex) lookup(name string) []string { return c.bySymbol[name] }
