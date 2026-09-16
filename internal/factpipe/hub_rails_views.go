package factpipe

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// hub_rails_views.go is the "rails_views" hub provider (see hub.go) — the
// Tier FX (FX.8.33) migration of internal/linker/rails_views.go's retired
// LinkRailsViews (Tier K.2).
//
// docs/declarative-framework-pipeline-plan.md's FX.8.33 row called this
// blocked on `call_ref` needing keyword-arg support inside ERB ("ERB's
// embedded Ruby is raw text in this grammar"). That premise was stale:
// internal/railsview's scanners (ScanRenders/ScanReactComponents/
// ScanLayouts) never used tree-sitter or call_ref at all — they are a
// hand-rolled byte scanner (callSites/callArgs/codeMask/keywordArg) that
// already handles keyword args in plain Go. The real reason this never fit
// a pattern-match-plus-join shape is the same reason react_prop_urls/
// ruby_http_hosts didn't: a per-service `viewIndex` built once, a
// layout-inheritance walk up the controller ancestry, and a cross-service
// JS barrel-import resolution for react_component mounts — none of it a
// bounded `.dl` join. So the whole algorithm is ported here near-verbatim,
// same "hub carries the whole algorithm" shape.
//
// language: generic is required (see patterns/generic/rails_views.yaml):
// this hub needs the WHOLE graph in every call — a react_component mount's
// JSX implementation is routinely in a different service than the one
// whose views/controllers are being resolved — which the shared per-service
// factpipe_frameworks loop cannot give it (patternLangForFile has no case
// for "generic"). internal/indexer/link_passes.go's "rails_views" pass
// invokes it directly via pipeline.Run instead, ONCE PER Rails-view-owning
// service (the schema_url_link_props convention), passing that service's
// own file list but the WHOLE unfiltered node graph every time, merging
// each call's Nodes/Edges/Unresolved.
//
// Because `files` is scoped to one service per call but `nodes` never is,
// `svc` (the service every minted/patched fact in this call is stamped
// with) can't come from the caller as a plain string — HubProvider's
// signature has no "current service name" slot, only svcPath (a directory).
// rvCallService derives it from whichever real node's File already sits in
// this call's `files` (a controller action, almost always) as the primary
// signal, falling back to the sole graph.NodeTypeService node's Label (an
// otherwise-empty call, e.g. a service with no existing nodes yet) and then
// to the graph's only distinct Service value — the three shapes every
// fixture in this pass's ported test suite actually exercises.
//
// One consequence: `resolveBarrels`'s cross-service barrel-following (a
// `window.Foo = Foo` re-export whose import target lives in ANOTHER
// service) can only see files from the CURRENT call's own service, unlike
// the retired Go's full cross-service `serviceFiles` map — a barrel and its
// import target that live in different services will not be rewritten past
// the barrel variable. No confirmed real site exercises that combination
// (every ported test keeps a barrel and its target in one service); revisit
// if one turns up.
func init() { RegisterHub("rails_views", railsViewsHub) }

const (
	rvFileMintPred      = "rv_file_mint"      // (ID, Label, Service, File, Language, Basename)
	rvFileContainsPred  = "rv_file_contains"  // (From, To)
	rvRendersPred       = "rv_renders"        // (From, To, Label, Mechanism, Kind, Spec, Collection, Layout, Component)
	rvComponentMintPred = "rv_component_mint" // (ID, Label, Service, File, Line, EndLine, DataReactClass)
	rvComponentImplPred = "rv_component_impl" // (From, To, Label, Component, Via)
	rvLedgerPred        = "rv_ledger"         // (Service, File, Line, Name, Kind)
)

func railsViewsHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	fileNodeID := map[string]string{} // svc\x00file -> id
	haveService := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFile:
			fileNodeID[n.Service+"\x00"+n.File] = n.ID
		case graph.NodeTypeService:
			haveService[n.Label] = true
		}
	}

	svc := rvCallService(nodes, files)
	if svc == "" {
		return nil
	}
	idx := rvNewViewIndex(files)
	if len(idx.byLogical) == 0 {
		return nil // no app/views tree: not a Rails app
	}

	var out []Fact
	minted := map[string]bool{}
	seenEdge := map[string]bool{}

	ensureFile := func(svc, file string) string {
		// file is sourced from the indexer's raw absolute file-walk list
		// (rvNewViewIndex's callers, needed for view-lookup logic elsewhere
		// in this file) — relativize before minting so the ID/File matches
		// the cwd-relative convention containment's pre-existing file nodes
		// use, same fix as hub_stylesheet_imports.go's ensure (FK-violation
		// bug found there and fixed identically here).
		file = relativizeToCwd(file)
		key := svc + "\x00" + file
		if id, ok := fileNodeID[key]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:%s", svc, file, graph.NodeTypeFile)
		fileNodeID[key] = id
		if !minted[id] {
			minted[id] = true
			out = append(out, Fact{
				Pred:   rvFileMintPred,
				Args:   []Atom{Str(id), Str(file), Str(svc), Str(file), Str(rvLanguageForFile(file)), Str(path.Base(file))},
				Origin: Origin{Kind: OriginPrimitive, File: file, Pattern: rvFileMintPred},
			})
			if haveService[svc] {
				out = append(out, Fact{
					Pred:   rvFileContainsPred,
					Args:   []Atom{Str("service:" + svc), Str(id)},
					Origin: Origin{Kind: OriginPrimitive, File: file, Pattern: rvFileContainsPred},
				})
			}
		}
		return id
	}

	addRenders := func(from, to, label, mechanism, kind, spec, collection, layout, component string) {
		if from == to || from == "" || to == "" {
			return
		}
		id := "renders:" + from + "->" + to + ":" + label
		if seenEdge[id] {
			return
		}
		seenEdge[id] = true
		out = append(out, Fact{
			Pred: rvRendersPred,
			Args: []Atom{
				Str(from), Str(to), Str(label), Str(mechanism), Str(kind), Str(spec),
				Str(collection), Str(layout), Str(component),
			},
			Origin: Origin{Kind: OriginPrimitive, Pattern: rvRendersPred},
		})
	}

	addComponentImpl := func(from, to, label, component, via string) {
		if from == to || from == "" || to == "" {
			return
		}
		id := "component_impl:" + from + "->" + to + ":" + label
		if seenEdge[id] {
			return
		}
		seenEdge[id] = true
		out = append(out, Fact{
			Pred:   rvComponentImplPred,
			Args:   []Atom{Str(from), Str(to), Str(label), Str(component), Str(via)},
			Origin: Origin{Kind: OriginPrimitive, Pattern: rvComponentImplPred},
		})
	}

	mintElement := func(id, label, svc, file string, line int, dataReactClass string) {
		if minted[id] {
			return
		}
		minted[id] = true
		out = append(out, Fact{
			Pred: rvComponentMintPred,
			Args: []Atom{
				Str(id), Str(label), Str(svc), Str(file), Int(int64(line)), Int(int64(line)), Str(dataReactClass),
			},
			Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: rvComponentMintPred},
		})
	}

	ledger := func(svc, file string, line int, name, kind string) {
		out = append(out, Fact{
			Pred:   rvLedgerPred,
			Args:   []Atom{Str(svc), Str(file), Int(int64(line)), Str(name), Str(kind)},
			Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: rvLedgerPred},
		})
	}

	// Cross-service: a react_component(...) mount in this service names a
	// component that almost always lives in a sibling JS service — nodes is
	// the WHOLE graph (see this file's doc comment), never filtered to svc.
	components := rvNewComponentIndex(nodes)
	components.resolveBarrels(nodes, map[string][]string{svc: files})

	rvLinkTemplates(svc, idx, components, ensureFile, addRenders, addComponentImpl, mintElement, ledger)
	rvLinkControllerActions(svc, idx, nodes, ensureFile, addRenders, ledger)

	return out
}

// rvCallService derives the service every fact from this call is stamped
// with. HubProvider's signature has no "current service name" slot — see
// this file's doc comment for the three-tier fallback and why each tier is
// needed.
func rvCallService(nodes []graph.Node, files []string) string {
	fileSet := make(map[string]bool, len(files))
	for _, f := range files {
		fileSet[f] = true
	}
	votes := map[string]int{}
	var soleService string
	nService := 0
	distinctSvc := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if fileSet[n.File] && n.Service != "" {
			votes[n.Service]++
		}
		if n.Type == graph.NodeTypeService {
			soleService = n.Label
			nService++
		}
		if n.Service != "" {
			distinctSvc[n.Service] = true
		}
	}
	if len(votes) > 0 {
		best, bestN := "", 0
		for svc, n := range votes {
			if n > bestN || (n == bestN && svc < best) {
				best, bestN = svc, n
			}
		}
		return best
	}
	if nService == 1 {
		return soleService
	}
	if len(distinctSvc) == 1 {
		for svc := range distinctSvc {
			return svc
		}
	}
	return ""
}

func rvLanguageForFile(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".erb":
		return "erb"
	case ".rb":
		return "ruby"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// view index (ported from internal/linker/rails_views.go's viewIndex)
// ---------------------------------------------------------------------------

type rvViewIndex struct {
	roots     []string
	byLogical map[string][]string
	erbFiles  []string
}

func rvNewViewIndex(files []string) *rvViewIndex {
	idx := &rvViewIndex{byLogical: map[string][]string{}}
	roots := map[string]bool{}

	for _, f := range files {
		root, rel, ok := rvSplitViewRoot(f)
		if !ok {
			continue
		}
		roots[root] = true
		key := path.Join(filepath.Dir(rel), rvLogicalBase(filepath.Base(rel)))
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

func rvSplitViewRoot(file string) (root, rel string, ok bool) {
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

func rvLogicalBase(base string) string {
	if i := strings.IndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base
}

func (idx *rvViewIndex) resolve(from, spec string, partial bool) []string {
	dir, base := path.Split(spec)
	if partial {
		base = "_" + base
	}

	var keys []string
	if dir != "" {
		keys = append(keys, path.Join(dir, base))
	} else {
		if _, rel, ok := rvSplitViewRoot(from); ok {
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

func rvLinkTemplates(
	svc string,
	idx *rvViewIndex,
	components *rvComponentIndex,
	ensureFile func(svc, file string) string,
	addRenders func(from, to, label, mechanism, kind, spec, collection, layout, component string),
	addComponentImpl func(from, to, label, component, via string),
	mintElement func(id, label, svc, file string, line int, dataReactClass string),
	ledger func(svc, file string, line int, name, kind string),
) {
	for _, f := range idx.erbFiles {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		_, ruby := railsview.SplitERB(src)
		fromID := ""
		ensureFrom := func() string {
			if fromID == "" {
				fromID = ensureFile(svc, f)
			}
			return fromID
		}

		for _, r := range railsview.ScanRenders(ruby) {
			if r.Dynamic {
				ledger(svc, f, r.Line, r.Spec, "erb_render_dynamic")
				continue
			}
			targets := idx.resolve(f, r.Spec, r.Kind != railsview.RenderTemplate)
			if len(targets) == 0 {
				ledger(svc, f, r.Line, r.Spec, "erb_render_unresolved")
				continue
			}
			collection := ""
			if r.Collection {
				collection = "true"
			}
			for _, t := range targets {
				addRenders(ensureFrom(), ensureFile(svc, t), "render "+r.Spec, "render", r.Kind, r.Spec, collection, "", "")
			}
		}

		for _, rc := range railsview.ScanReactComponents(ruby) {
			if rc.Dynamic {
				ledger(svc, f, rc.Line, rc.Name, "react_component_dynamic")
				continue
			}
			elID := fmt.Sprintf("%s:%s:%s:span[data-react-class=%s]:%d", svc, f, graph.NodeTypeElement, rc.Name, rc.Line)
			mintElement(elID, fmt.Sprintf("span[data-react-class=%s]", rc.Name), svc, f, rc.Line, rc.Name)
			addRenders(ensureFrom(), elID, "react_component "+rc.Name, "react_component", "", "", "", "", rc.Name)

			impls := components.lookup(rc.Name)
			if len(impls) == 0 {
				ledger(svc, f, rc.Line, rc.Name, "react_component_unresolved")
				continue
			}
			via := ""
			if components.viaBarrel[rc.Name] {
				via = "barrel"
			}
			for _, impl := range impls {
				addComponentImpl(elID, impl, "window."+rc.Name, rc.Name, via)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// controllers
// ---------------------------------------------------------------------------

func rvLinkControllerActions(
	svc string,
	idx *rvViewIndex,
	nodes []graph.Node,
	ensureFile func(svc, file string) string,
	addRenders func(from, to, label, mechanism, kind, spec, collection, layout, component string),
	ledger func(svc, file string, line int, name, kind string),
) {
	type action struct {
		id            string
		name          string
		line, endLine int
	}
	byFile := map[string][]action{}
	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc || !rvIsControllerFile(n.File) {
			continue
		}
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		end := 0
		fmt.Sscanf(n.Meta["end_line"], "%d", &end)
		if n.Meta["pattern"] != "" && end == 0 {
			continue
		}
		byFile[n.File] = append(byFile[n.File], action{n.ID, n.Label, n.Line, end})
	}

	ctrlFiles := make([]string, 0, len(byFile))
	for f := range byFile {
		ctrlFiles = append(ctrlFiles, f)
	}
	sort.Strings(ctrlFiles)

	appLayout, appLayoutOK := rvApplicationControllerLayout(ctrlFiles)

	for _, f := range ctrlFiles {
		acts := byFile[f]
		sort.Slice(acts, func(i, j int) bool { return acts[i].line < acts[j].line })

		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		renders := railsview.ScanRenders(src)
		layoutDecls := railsview.ScanLayouts(src)
		ctrlPath, ok := rvControllerPath(f)
		if !ok {
			continue
		}

		for _, a := range acts {
			var viewTargets []string
			namedTemplate := false
			for _, r := range renders {
				if r.Line < a.line || (a.endLine > 0 && r.Line > a.endLine) {
					continue
				}
				if r.Kind != railsview.RenderLayout {
					namedTemplate = true
				}
				if r.Dynamic {
					ledger(svc, f, r.Line, r.Spec, "erb_render_dynamic")
					continue
				}
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
					ledger(svc, f, r.Line, r.Spec, "erb_render_unresolved")
					continue
				}
				for _, t := range targets {
					tid := ensureFile(svc, t)
					addRenders(a.id, tid, "render "+r.Spec, "explicit", "", r.Spec, "", "", "")
					if r.Kind != railsview.RenderLayout {
						viewTargets = append(viewTargets, tid)
					}
				}
			}
			if !namedTemplate {
				for _, t := range idx.resolve(f, path.Join(ctrlPath, a.name), false) {
					tid := ensureFile(svc, t)
					addRenders(a.id, tid, a.name, "convention", "", path.Join(ctrlPath, a.name), "", "", "")
					viewTargets = append(viewTargets, tid)
				}
			}
			rvLinkActionLayout(svc, f, a.id, a.name, layoutDecls, appLayout, appLayoutOK, viewTargets, idx, ensureFile, addRenders, ledger)
		}
	}
}

func rvLinkActionLayout(
	svc, file, actionID, action string,
	fileDecls []railsview.LayoutDecl,
	appDecl railsview.LayoutDecl, appOK bool,
	viewTargets []string,
	idx *rvViewIndex,
	ensureFile func(svc, file string) string,
	addRenders func(from, to, label, mechanism, kind, spec, collection, layout, component string),
	ledger func(svc, file string, line int, name, kind string),
) {
	if len(viewTargets) == 0 {
		return
	}

	decl, fromFile, have := railsview.LayoutDecl{}, false, false
	for i := len(fileDecls) - 1; i >= 0; i-- {
		if fileDecls[i].Applies(action) {
			decl, fromFile, have = fileDecls[i], true, true
			break
		}
	}
	if !have && appOK {
		decl, have = appDecl, true
	}

	layoutName := "application"
	switch {
	case have && decl.None:
		return
	case have && decl.Name != "":
		layoutName = decl.Name
	case have && decl.Dynamic:
		if fromFile {
			ledger(svc, file, decl.Line, action, "controller_layout_dynamic")
		}
		return
	}

	targets := idx.resolve(file, path.Join("layouts", layoutName), false)
	if len(targets) == 0 {
		if have && decl.Name != "" {
			ledger(svc, file, 0, layoutName, "controller_layout_unresolved")
		}
		return
	}
	for _, lt := range targets {
		lid := ensureFile(svc, lt)
		addRenders(actionID, lid, "layout "+layoutName, "layout", "", "", "", layoutName, "")
		for _, vt := range viewTargets {
			addRenders(lid, vt, "yield", "yield", "", "", "", layoutName, "")
		}
	}
}

func rvApplicationControllerLayout(ctrlFiles []string) (railsview.LayoutDecl, bool) {
	for _, f := range ctrlFiles {
		if filepath.Base(filepath.ToSlash(f)) != "application_controller.rb" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return railsview.LayoutDecl{}, false
		}
		decls := railsview.ScanLayouts(src)
		if len(decls) == 0 {
			return railsview.LayoutDecl{}, false
		}
		return decls[len(decls)-1], true
	}
	return railsview.LayoutDecl{}, false
}

func rvIsControllerFile(file string) bool {
	s := filepath.ToSlash(file)
	return strings.HasSuffix(s, "_controller.rb") && rvControllersMarkerIndex(s) >= 0
}

func rvControllerPath(file string) (string, bool) {
	s := filepath.ToSlash(file)
	i := rvControllersMarkerIndex(s)
	if i < 0 {
		return "", false
	}
	return strings.TrimSuffix(s[i+len("app/controllers/"):], "_controller.rb"), true
}

func rvControllersMarkerIndex(s string) int {
	const marker = "app/controllers/"
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return -1
	}
	if i > 0 && s[i-1] != '/' {
		return -1
	}
	return i
}

// ---------------------------------------------------------------------------
// component registry (ported from internal/linker/rails_views.go's
// componentIndex)
// ---------------------------------------------------------------------------

type rvComponentIndex struct {
	bySymbol  map[string][]string
	viaBarrel map[string]bool
}

func rvNewComponentIndex(nodes []graph.Node, svcs ...string) *rvComponentIndex {
	svcSet := map[string]bool{}
	for _, s := range svcs {
		svcSet[s] = true
	}
	regFiles := map[string][]string{}
	implID := map[string]string{}
	varID := map[string]string{}

	for i := range nodes {
		n := &nodes[i]
		if len(svcSet) > 0 && !svcSet[n.Service] {
			continue
		}
		if n.Meta["is_test"] == "true" {
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

	idx := &rvComponentIndex{bySymbol: map[string][]string{}, viaBarrel: map[string]bool{}}
	for sym, fs := range regFiles {
		sort.Strings(fs)
		for _, f := range fs {
			if id, ok := implID[f+"\x00"+sym]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			} else if id, ok := varID[sym+"\x00"+f]; ok {
				idx.bySymbol[sym] = append(idx.bySymbol[sym], id)
			}
		}
	}
	return idx
}

func (c *rvComponentIndex) lookup(name string) []string { return c.bySymbol[name] }

func (c *rvComponentIndex) resolveBarrels(nodes []graph.Node, serviceFiles map[string][]string) {
	if len(c.bySymbol) == 0 {
		return
	}

	indexed := map[string]bool{}
	absByRel := map[string]string{}
	for _, files := range serviceFiles {
		for _, f := range files {
			indexed[f] = true
			absByRel[f] = f
		}
	}

	bestID := map[string]string{}
	bestRank := map[string]int{}
	byID := map[string]*graph.Node{}
	for i := range nodes {
		n := &nodes[i]
		byID[n.ID] = n
		if n.Meta["is_test"] == "true" {
			continue
		}
		rank := 0
		switch {
		case n.Meta["component"] == "true":
			rank = 3
		case n.Type == graph.NodeTypeClass:
			rank = 2
		case n.Type == graph.NodeTypeFunction:
			rank = 1
		default:
			continue
		}
		key := n.File + "\x00" + n.Label
		if rank > bestRank[key] {
			bestRank[key] = rank
			bestID[key] = n.ID
		}
	}

	importCache := map[string]map[string]string{}
	defaultImports := func(rel string) map[string]string {
		if m, ok := importCache[rel]; ok {
			return m
		}
		m := map[string]string{}
		importCache[rel] = m
		abs := absByRel[rel]
		if abs == "" {
			return m
		}
		src, root, _, ok := jsast.Parse(abs)
		if !ok {
			return m
		}
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if n.Type() == "import_statement" {
				if s := n.ChildByFieldName("source"); s != nil {
					spec := strings.Trim(s.Content(src), "\"'`")
					if tgt := jsast.ResolveImportPath(rel, spec, indexed); tgt != "" {
						for i := 0; i < int(n.NamedChildCount()); i++ {
							ic := n.NamedChild(i)
							if ic.Type() != "import_clause" {
								continue
							}
							for j := 0; j < int(ic.NamedChildCount()); j++ {
								if cc := ic.NamedChild(j); cc.Type() == "identifier" {
									m[cc.Content(src)] = tgt
								}
							}
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
		}
		walk(root)
		return m
	}

	for sym, ids := range c.bySymbol {
		out := make([]string, 0, len(ids))
		seen := map[string]bool{}
		changed := false
		for _, id := range ids {
			pick := id
			if n := byID[id]; n != nil && n.Type == graph.NodeTypeVariable && n.Meta["scope"] == "global" {
				if tgt := defaultImports(n.File)[sym]; tgt != "" {
					if real := bestID[tgt+"\x00"+sym]; real != "" && real != id {
						pick = real
						changed = true
					}
				}
			}
			if !seen[pick] {
				seen[pick] = true
				out = append(out, pick)
			}
		}
		if changed {
			c.bySymbol[sym] = out
			c.viaBarrel[sym] = true
		}
	}
}
