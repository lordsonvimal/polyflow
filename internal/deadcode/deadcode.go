// Package deadcode finds function/method/component nodes with zero inbound
// invoking edges and variable/const nodes with zero inbound reads: a
// fixed-shape, full-graph scan rather than an ad-hoc query language, matching
// the rest of polyflow's tool set.
//
// Two opt-in extensions widen the scan (see Options): IncludeTypes (DC.29)
// adds struct/interface/type_alias declarations with no inbound type-use edge,
// and Transitive (DC.28) switches the predicate from "zero inbound edge" to
// "unreachable from any live root", catching a dead cluster whose members
// call each other.
package deadcode

import (
	"path"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Item is one flagged zero-caller function/method/component or
// zero-reader variable/const.
type Item struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
}

// Result is the output of a dead-code scan.
type Result struct {
	Functions []Item `json:"functions"`
	Total     int    `json:"total"`
}

// Options scopes a Build scan.
type Options struct {
	Service string // "" = every service
	File    string // "" = every file

	// Transitive (DC.28) switches the callable/type predicate from
	// "zero inbound invoking edge" to "not reachable from any live root".
	// Live roots are the same nodes the per-node carve-outs already spare
	// (entrypoints, framework callbacks, reflect-dispatched methods, test
	// functions); a BFS forward over invokingEdgeTypes (plus typeUseEdgeTypes
	// when IncludeTypes is set) marks everything they can reach. A function
	// whose only callers are themselves unreachable is then flagged too —
	// the single-hop scan misses a whole dead cluster the moment its members
	// call each other.
	//
	// Off by default: it is only as sound as the call graph is complete, and
	// Ruby call resolution in particular is partial enough (see the
	// ruby-call-resolution notes) that a transitive scan there flags large
	// live subtrees whose one real entry edge the indexer never minted. Safe
	// on Go/TS services; treat Ruby output as a lead, not a verdict.
	Transitive bool

	// IncludeTypes (DC.29) adds struct/interface/type_alias declaration nodes
	// to the scan: a type with no inbound type-use edge (see typeUseEdgeTypes)
	// and no live method hanging off it is unreferenced. Off by default so the
	// existing function/method/variable/view output is unchanged. NodeTypeClass
	// is deliberately excluded — a JS/TS/Ruby class doubles as a component and
	// a method bag, with the render-tree and Ruby-resolution caveats that
	// implies; struct/interface/type_alias are plain type declarations.
	IncludeTypes bool

	// UnresolvedRefs is the graph's blind-spot ledger (graph.Store.
	// ListUnresolvedRefs), used only by the DC.27 Rails-view branch to
	// recognize a zero-inbound-renders partial/template as the plausible
	// target of a dynamic `render partial: expr` call already ledgered as
	// erb_render_dynamic/erb_render_unresolved. Every caller should pass the
	// real ledger: leaving this nil does not disable the branch, it just
	// starves one of its two carve-outs, which is the false-positive
	// direction DC.26's investigation warned against (flagging a
	// legitimately dynamic-dispatched partial as dead).
	UnresolvedRefs []graph.UnresolvedRef
}

// Build scans idx for two independent dead shapes:
//
//   - function/method/component nodes with no inbound invoking edge (see
//     invokingEdgeTypes), excluding nodes graph.ClassifyEntrypoint already
//     recognises as an entry point (HTTP handlers, routes, workers,
//     subscribers, gRPC/GraphQL handlers, and functions tagged
//     meta.root_kind=entrypoint) — those are meant to have zero static
//     callers. Structural edges (contains, declares, ...) never count as a
//     caller.
//   - variable/const nodes (graph.NodeTypeVariable) with no inbound
//     EdgeTypeReads — a callable's "invoked" predicate doesn't apply to a
//     value, so this branch uses hasReader instead of hasCaller and skips
//     the entrypoint/reflect-dispatched checks entirely (see hasReader).
//   - (DC.27) Rails view files (graph.NodeTypeFile nodes under app/views/,
//     excluding layouts/mailers) with no inbound EdgeTypeRenders — see
//     isDeadRailsView. A view has no "invoked" predicate either, and unlike
//     the variable branch it needs two carve-outs (implicit view resolution,
//     dynamic render dispatch) before a zero-inbound result means anything.
func Build(idx *graph.AdjacencyIndex, opts Options) *Result {
	var items []Item
	liveRailsRoutes := railsRouteTargets(idx)
	dynDispatches := railsDynamicRenderTargets(opts.UnresolvedRefs)
	var reachable map[string]bool
	if opts.Transitive {
		reachable = liveReachable(idx, opts.IncludeTypes)
	}
	for _, n := range idx.Nodes {
		isCallable := n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod || n.Type == graph.NodeTypeComponent
		isRailsView := n.Type == graph.NodeTypeFile && isRailsViewFile(n.File)
		isType := opts.IncludeTypes && isTypeDecl(n.Type)
		if !isCallable && n.Type != graph.NodeTypeVariable && !isRailsView && !isType {
			continue
		}
		if opts.Service != "" && n.Service != opts.Service {
			continue
		}
		if opts.File != "" && n.File != opts.File {
			continue
		}
		if isType {
			// A test-only helper type (a fixture struct, a mock's interface)
			// is dead by construction the same way a test function is.
			if graph.IsTestFilePath(n.File) {
				continue
			}
			dead := !hasTypeUse(idx, n.ID)
			if opts.Transitive {
				dead = !reachable[n.ID]
			}
			// A struct whose type is never named but whose methods are still
			// called (through a value obtained some other way — an embedding
			// parent, a factory returning an interface) is not dead. Check the
			// contained callables under the same predicate as the scan itself.
			if dead && !typeHasLiveMethod(idx, n.ID, opts, reachable) {
				items = append(items, Item{
					ID:      n.ID,
					Label:   n.Label,
					Type:    string(n.Type),
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
					EndLine: n.EndLine,
				})
			}
			continue
		}
		if isRailsView {
			if isDeadRailsView(idx, n, liveRailsRoutes, dynDispatches) {
				items = append(items, Item{
					ID:      n.ID,
					Label:   n.Label,
					Type:    string(n.Type),
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
					EndLine: n.EndLine,
				})
			}
			continue
		}
		// A variable/const node (see graph.NodeTypeVariable) has no notion of
		// "invoked" — it's read, not called — so it needs its own dead
		// predicate rather than being run through the invokingEdgeTypes/
		// entrypoint/reflect-dispatched machinery below, all of which assume a
		// callable unit. Write-only is still dead: an assignment nobody ever
		// reads has no observable effect.
		//
		// Only meta.scope=package|module qualifies. Verified live on the
		// juniper fleet: scope=captured (isLocalVariable's function-local
		// receivers/params/locals — every one, not just closure-captured ones,
		// see graph.isLocalVariable) had a 100% zero-reads rate (1376/1376) —
		// the reads/writes extractors never mint edges for that bucket at all,
		// so "zero reads" carries no signal there. scope=global (JS
		// window-namespace values matched by MetaGlobalSymbol string in
		// rails_views.go, not by a reads edge) was equally 51/51 zero-reads for
		// the same reason: wrong liveness mechanism, not actually dead.
		// scope=package/module are the only buckets a reads edge is actually
		// minted for on genuine usage (confirmed live: a flagged package-scope
		// const/var with a real call site, e.g. `providerOnce.Do(...)`, was the
		// exception rather than the rule — real orphans like an unused status
		// enum member dominate that bucket).
		if !isCallable {
			scope := n.Meta["scope"]
			if scope != "package" && scope != "module" {
				continue
			}
			if graph.IsTestFilePath(n.File) {
				continue
			}
			if hasReader(idx, n.ID) {
				continue
			}
			items = append(items, Item{
				ID:      n.ID,
				Label:   n.Label,
				Type:    string(n.Type),
				Service: n.Service,
				File:    n.File,
				Line:    n.Line,
				EndLine: n.EndLine,
			})
			continue
		}
		// meta.root_kind=callback (referenced as a value / satisfies an
		// external interface — invoked by a framework, not by a literal call
		// site) is a distinct bucket from entrypoint but the same "not
		// actionable dead code" verdict: Go's SSA-referenced functions and a
		// JS object-literal callback value (`{ onProceed: function(){...} }`)
		// both land here. ClassifyEntrypoint already computes this as
		// skippedRootKind; check it alongside ok rather than duplicating the
		// meta read.
		if _, skippedRootKind, ok := graph.ClassifyEntrypoint(n); ok || skippedRootKind == "callback" {
			continue
		}
		// Test functions are invoked by the test runner, not by a static
		// caller in the graph — every one of them is a zero-caller node by
		// construction. Flagging them as dead code is never actionable and
		// drowns out real hits (measured: 70% of a live scan's flagged
		// functions were _test.go/.spec. files).
		if graph.IsTestFilePath(n.File) {
			continue
		}
		// A method the indexer already determined is invoked by a framework
		// or the standard library through an interface value or reflection
		// (GORM's TableName/Before*/After* hooks, Go's own Stringer/error/
		// Marshaler/Scanner/Handler, ...) is zero-caller by construction, the
		// same way an HTTP handler is. The name list itself is declared
		// per-language/per-package in the pattern registry (see
		// patterns.PatternFile.ReflectDispatchedMethods), package/version-
		// gated per service at index time — not hardcoded here — so a Ruby/
		// JS/Python method sharing one of these names on a repo that never
		// pulled in GORM is untouched and stays a real deadcode candidate.
		if n.Meta[graph.MetaReflectDispatched] == "true" {
			continue
		}
		if opts.Transitive {
			if reachable[n.ID] {
				continue
			}
		} else if hasCaller(idx, n.ID) {
			continue
		}
		items = append(items, Item{
			ID:      n.ID,
			Label:   n.Label,
			Type:    string(n.Type),
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			EndLine: n.EndLine,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Service != items[j].Service {
			return items[i].Service < items[j].Service
		}
		if items[i].File != items[j].File {
			return items[i].File < items[j].File
		}
		if items[i].Line != items[j].Line {
			return items[i].Line < items[j].Line
		}
		return items[i].ID < items[j].ID
	})
	if items == nil {
		items = []Item{}
	}

	return &Result{Functions: items, Total: len(items)}
}

// invokingEdgeTypes are the edge types that represent a real invocation of
// their target for deadcode's purposes. Structural (contains, declares),
// data (reads, writes, flows_to, uses_type), and producer-side (publishes,
// navigates_to, http_call, ...) edges never qualify — only add a type here
// once a live query has confirmed it lands on a NodeTypeFunction/Method, not
// just a still-unresolved proxy node (job/message-broker consumers resolve
// lazily, the same way LinkJS's JSX proxy redirect works for renders — an
// edge type can be correct to add even where today's data shows it still
// landing on the odd unresolved proxy elsewhere).
//
// EdgeTypeSpawns (`go f()`) is a genuine caller the same way a direct call
// is — a goroutine target with no inbound EdgeTypeCalls was a systematic
// false positive (every function only ever launched via `go x.method()`,
// e.g. a scheduler's background loop, qualified as zero-caller by
// construction).
//
// EdgeTypeRenders (JSX/component usage) is a genuine invocation — LinkJS
// redirects the usage-site proxy to the real component declaration before
// this edge lands (confirmed live: chessleap/juniper/orion graphs all
// carry renders edges terminating on real function/component nodes).
//
// EdgeTypeJobEnqueue/EdgeTypeJobPerform (background-job dispatch: ActiveJob,
// Celery, delayed_job, Sidekiq, solid_queue) are minted by the generic
// contract engine (contracts/jobs.yaml) straight onto the class's real
// `perform` method, the same way a direct call would be — confirmed live on
// the orion graph (job_enqueue and job_perform edges landing on
// NodeTypeFunction). EdgeTypeSidekiqEnqueue/EdgeTypeSidekiqPerform are kept
// alongside them: the enum documents them as deprecated aliases for graphs
// indexed before the generic job_enqueue/job_perform rename, so a stored
// graph using the old names must get the same treatment, not a stale one.
//
// EdgeTypeSubscribes (AMQP/message-broker consumer registration) is a
// genuine invocation once resolved to a handler — confirmed live on the
// juniper graph (function/method targets, not just the unresolved
// channel/worker proxy nodes the same edge type can also terminate on).
//
// EdgeTypeComponentImpl (react_rails' `react_component("Name", props)` ERB
// helper, resolved by `linkTemplates` in rails_views.go to the implementing
// JS class/function via componentIndex) is a genuine invocation the same way
// a direct call is: the ERB mount point is the only caller a server-rendered
// React entry point ever gets, the same shape as EdgeTypeRenders for
// JSX-mounted components — confirmed live: orion container/dashboard
// components (AllOperationsContainer and ~25 others) mounted exclusively via
// react_component had zero inbound edges of any other type and were flagged
// dead despite being the page's actual React root.
//
// EdgeTypeDOMListen (a jQuery/vanilla-JS event registration — `$(el).on(...)`,
// `el.addEventListener(...)`) is a genuine invocation the same way a direct
// call is: the browser's event loop calls the handler, not a literal call
// site. The indexer's own root classifier (classifyRoot) already treats any
// non-Contains inbound edge, dom_listen included, as proof a node is reached
// — omitting it here just meant deadcode and root classification disagreed,
// flagging every jQuery/DOM handler as dead code (confirmed live: 92 of a
// orion scan's flagged functions were dom_listen-only handler nodes like
// `click@.js-approve-ai-gen`, none of them actually unreached).
var invokingEdgeTypes = map[graph.EdgeType]bool{
	graph.EdgeTypeCalls:          true,
	graph.EdgeTypeSpawns:         true,
	graph.EdgeTypeRenders:        true,
	graph.EdgeTypeJobEnqueue:     true,
	graph.EdgeTypeJobPerform:     true,
	graph.EdgeTypeSidekiqEnqueue: true,
	graph.EdgeTypeSidekiqPerform: true,
	graph.EdgeTypeSubscribes:     true,
	graph.EdgeTypeDOMListen:      true,
	graph.EdgeTypeComponentImpl:  true,
}

// hasCaller reports whether n has at least one inbound invoking edge.
func hasCaller(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if invokingEdgeTypes[e.Type] {
			return true
		}
	}
	return false
}

// hasReader reports whether the variable/const node id has at least one
// inbound EdgeTypeReads. EdgeTypeWrites deliberately does not count: a
// variable only ever assigned to and never read back is exactly the dead
// case this is meant to catch.
func hasReader(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if e.Type == graph.EdgeTypeReads {
			return true
		}
	}
	return false
}

// typeUseEdgeTypes are the edge types that represent a real reference to a
// type declaration (struct/interface/type_alias), the type-node analogue of
// invokingEdgeTypes. A `contains` edge (file→type, type→method) is structural
// and never qualifies.
//
//   - uses_type: a function/variable names the type in a signature, field, or
//     local declaration.
//   - instantiates: a function constructs a value of the type.
//   - inherits: an embedding struct / subclass names it as a base.
//   - implements: a struct declares (or Go-structurally satisfies) the
//     interface.
//   - returns / consumes: a handler marshals it as a response body / a client
//     decodes a response into it — the type is live across the wire even with
//     no in-language `uses_type`.
//   - response_of: the type is the matched mirror of a DTO in another service.
//   - references: a SQL table's FOREIGN KEY points at it.
var typeUseEdgeTypes = map[graph.EdgeType]bool{
	graph.EdgeTypeUsesType:     true,
	graph.EdgeTypeInstantiates: true,
	graph.EdgeTypeInherits:     true,
	graph.EdgeTypeImplements:   true,
	graph.EdgeTypeReturns:      true,
	graph.EdgeTypeConsumes:     true,
	graph.EdgeTypeResponseOf:   true,
	graph.EdgeTypeReferences:   true,
}

// isTypeDecl reports whether t is a plain type declaration eligible for the
// DC.29 unreferenced-type scan. NodeTypeClass is excluded on purpose — see
// Options.IncludeTypes.
func isTypeDecl(t graph.NodeType) bool {
	return t == graph.NodeTypeStruct || t == graph.NodeTypeInterface || t == graph.NodeTypeTypeAlias
}

// hasTypeUse reports whether the type node id has at least one inbound
// type-use edge (see typeUseEdgeTypes).
func hasTypeUse(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if typeUseEdgeTypes[e.Type] {
			return true
		}
	}
	return false
}

// typeHasLiveMethod reports whether any method/function contained by the type
// node id is itself reached — under the same predicate (transitive or
// single-hop) the caller is scanning with. A struct nobody names by type but
// whose methods are still called via an interface value or an embedding
// parent is not dead.
func typeHasLiveMethod(idx *graph.AdjacencyIndex, id string, opts Options, reachable map[string]bool) bool {
	for _, e := range idx.OutEdges[id] {
		if e.Type != graph.EdgeTypeContains {
			continue
		}
		m := idx.Nodes[e.To]
		if m == nil || (m.Type != graph.NodeTypeMethod && m.Type != graph.NodeTypeFunction) {
			continue
		}
		if opts.Transitive {
			if reachable[e.To] {
				return true
			}
		} else if hasCaller(idx, e.To) {
			return true
		}
	}
	return false
}

// liveReachable returns the set of node IDs reachable from a live root by a
// forward BFS over invokingEdgeTypes (plus typeUseEdgeTypes when
// includeTypes). A live root is any node that is not itself a dead-code
// candidate — every entrypoint, framework callback, reflect-dispatched or
// test callable, and every non-callable/non-type structural anchor
// (http_handler, route, worker, subscriber, DOM element, file, …). Anything
// the walk does not reach is dead: its only inbound invocations, if any, come
// from other unreachable nodes.
func liveReachable(idx *graph.AdjacencyIndex, includeTypes bool) map[string]bool {
	live := make(map[string]bool, len(idx.Nodes))
	queue := make([]string, 0, len(idx.Nodes))
	enqueue := func(id string) {
		if !live[id] {
			live[id] = true
			queue = append(queue, id)
		}
	}
	for id, n := range idx.Nodes {
		if isLiveRoot(n) {
			enqueue(id)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range idx.OutEdges[cur] {
			if invokingEdgeTypes[e.Type] || (includeTypes && typeUseEdgeTypes[e.Type]) {
				enqueue(e.To)
			}
		}
	}
	return live
}

// isLiveRoot classifies a node as an anchor the transitive walk starts from.
// Callables must earn root status the same way the per-node carve-outs spare
// them (entrypoint / callback / reflect-dispatched / test); type and variable
// nodes are never roots (they must be reached to count as live); everything
// else — the non-candidate node types that own real invoking edges — always
// anchors whatever it points at.
func isLiveRoot(n *graph.Node) bool {
	switch n.Type {
	case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeComponent:
		if _, skippedRootKind, ok := graph.ClassifyEntrypoint(n); ok || skippedRootKind == "callback" {
			return true
		}
		return graph.IsTestFilePath(n.File) || n.Meta[graph.MetaReflectDispatched] == "true"
	case graph.NodeTypeStruct, graph.NodeTypeInterface, graph.NodeTypeTypeAlias,
		graph.NodeTypeClass, graph.NodeTypeVariable:
		return false
	default:
		return true
	}
}

// isRailsViewFile reports whether file is a candidate for the DC.27 dead-view
// check: an ERB file under app/views/, excluding layouts (own reachability
// rule — a layout is wired to a controller/action pair, not rendered by
// name) and mailers (delivered by ActionMailer's `mail`/`deliver`, never a
// literal `render` call — same reason DC.26's investigation query excluded
// them from its candidate count).
func isRailsViewFile(file string) bool {
	if !strings.HasSuffix(file, ".erb") {
		return false
	}
	if !strings.Contains(file, "app/views/") {
		return false
	}
	if strings.Contains(file, "app/views/layouts/") {
		return false
	}
	if strings.Contains(strings.ToLower(file), "mailer") {
		return false
	}
	return true
}

// railsRouteTargets indexes every live Ruby http_handler's implicit-view
// target as "service\x00controllerPath/action". controllerPath is
// meta["controller_module"] + "/" + meta["resource"] when a module is
// recorded, else just meta["resource"] — deliberately not the
// namespace-precise, path-segment-verified resolution
// internal/linker/rails_route_actions.go's RailsRouteTarget does for
// pinning a `calls` edge to one exact controller. That precision has no
// counterpart here: a devise_route/devise_default_route handler (Devise's
// own view family) never sets controller_module and its full_path/path
// never contains the resource segment literally (`/users/sign_up` for
// resource "registrations"), so RailsRouteTarget's path-segment fallback
// always failed on it — the family DC.26's investigation confirmed serves
// every one of orion's zero-inbound Devise views. This carve-out only
// needs to know a route exists, not which controller implements it, so the
// generous match (no namespace verification when controller_module is
// absent) is the correct trade — a wrong suppression here just leaves a
// dead file unflagged, the same safe direction DC.26 already argued for.
//
// Independent of whether a controller method node exists for that action:
// DC.26 also found Rails renders an action's template with no explicit
// method body at all (true for every Devise view and any other action a
// controller never overrides), so gating on a method node would silently
// un-clear exactly the views this carve-out exists for.
func railsRouteTargets(idx *graph.AdjacencyIndex) map[string]bool {
	live := map[string]bool{}
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeHTTPHandler || n.Language != "ruby" {
			continue
		}
		action := strings.TrimPrefix(n.Meta["action"], ":")
		resource := n.Meta["resource"]
		if action == "" || resource == "" {
			continue
		}
		ctrlPath := resource
		if ns := n.Meta["controller_module"]; ns != "" {
			ctrlPath = ns + "/" + resource
		}
		live[n.Service+"\x00"+ctrlPath+"/"+action] = true
	}
	return live
}

// dynamicRenderTarget is one erb_render_dynamic/erb_render_unresolved ledger
// entry, reduced to the literal text a matching view's identifier must
// contain.
type dynamicRenderTarget struct {
	service string
	literal string
	// exact is true for erb_render_unresolved (name is a fully resolved
	// literal with no interpolation — leadingSpec already stripped quotes
	// and returned false only because idx.resolve found no file, so the
	// remaining text names one specific target). false for
	// erb_render_dynamic (name is the raw, still-quoted source expression up
	// to its first `#{`; anything after is Ruby's problem to interpolate,
	// not this heuristic's — a matching view only needs to share that
	// literal prefix, the same way DC.26's investigation cleared 73 of 110
	// zero-inbound partials off a single `"change_logs/models/#{obj_name}"`
	// call site).
	exact bool
}

// railsDynamicRenderTargets reduces the graph's unresolved-ref ledger to the
// erb_render_dynamic/erb_render_unresolved rows a dead-view check can use.
// Every other kind (call_ref, import_ref, rails_filter_unresolved, ...) is
// unrelated to view rendering and skipped.
func railsDynamicRenderTargets(refs []graph.UnresolvedRef) []dynamicRenderTarget {
	var out []dynamicRenderTarget
	for _, r := range refs {
		var t dynamicRenderTarget
		switch r.Kind {
		case "erb_render_dynamic":
			t.exact = false
		case "erb_render_unresolved":
			t.exact = true
		default:
			continue
		}
		name := strings.TrimPrefix(r.Name, `"`)
		name = strings.TrimPrefix(name, `'`)
		if i := strings.Index(name, "#{"); i >= 0 {
			name = name[:i]
			t.exact = false // an interpolation always makes this a prefix, regardless of kind
		}
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		t.service, t.literal = r.Service, name
		out = append(out, t)
	}
	return out
}

// railsViewIdentifier reduces a view file's path to the logical name Ruby's
// own `render` call would spell it by: the path relative to app/views/,
// minus the format/handler extensions (".html.erb", ".js.erb", ".text.erb",
// ...) and, for a partial, its leading underscore. "" means file did not
// contain an app/views/ segment (isRailsViewFile already guards every real
// caller against this, so it should not happen in practice).
func railsViewIdentifier(file string) string {
	const marker = "app/views/"
	i := strings.Index(file, marker)
	if i < 0 {
		return ""
	}
	rel := file[i+len(marker):]
	dir, base := path.Split(rel)
	base = strings.TrimSuffix(base, ".erb")
	if j := strings.LastIndex(base, "."); j >= 0 {
		base = base[:j] // drop the format segment: index.html -> index
	}
	base = strings.TrimPrefix(base, "_")
	return strings.TrimSuffix(dir, "/") + "/" + base
}

// isDeadRailsView reports whether a zero-inbound-EdgeTypeRenders view node
// clears both DC.26 carve-outs and can be flagged as a real dead-code
// candidate.
func isDeadRailsView(idx *graph.AdjacencyIndex, n *graph.Node, liveRoutes map[string]bool, dynTargets []dynamicRenderTarget) bool {
	if hasRenderer(idx, n.ID) {
		return false
	}
	ident := railsViewIdentifier(n.File)
	if ident == "" {
		return false
	}
	// (a) implicit view resolution: Devise ships its default views one
	// directory deeper than its own routes name them (app/views/devise/
	// <resource>/<action>.erb serves a devise_for route whose meta.resource
	// is just <resource>, with no "devise" segment) — confirmed live in
	// DC.26's investigation across all 8 Devise view files it found.
	routeIdent := strings.TrimPrefix(ident, "devise/")
	if liveRoutes[n.Service+"\x00"+routeIdent] {
		return false
	}
	// (b) dynamic/unresolved render target: a ledgered call site this view's
	// identifier could plausibly be the destination of.
	for _, t := range dynTargets {
		if t.service != n.Service {
			continue
		}
		if t.exact {
			if ident == t.literal {
				return false
			}
			continue
		}
		if strings.HasPrefix(ident, t.literal) {
			return false
		}
	}
	return true
}

// hasRenderer reports whether the view node id has at least one inbound
// EdgeTypeRenders — the same shape as hasReader, for a view's own "invoked"
// predicate (a plain `renders` edge, not the wider invokingEdgeTypes set
// hasCaller checks: a view is never `calls`'d or `spawns`'d, only rendered).
func hasRenderer(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if e.Type == graph.EdgeTypeRenders {
			return true
		}
	}
	return false
}
