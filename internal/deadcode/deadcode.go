// Package deadcode finds function/method/component nodes with zero inbound
// invoking edges and variable/const nodes with zero inbound reads: a
// fixed-shape, full-graph scan rather than an ad-hoc query language, matching
// the rest of polyflow's tool set.
package deadcode

import (
	"sort"

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
func Build(idx *graph.AdjacencyIndex, opts Options) *Result {
	var items []Item
	for _, n := range idx.Nodes {
		isCallable := n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod || n.Type == graph.NodeTypeComponent
		if !isCallable && n.Type != graph.NodeTypeVariable {
			continue
		}
		if opts.Service != "" && n.Service != opts.Service {
			continue
		}
		if opts.File != "" && n.File != opts.File {
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
		if hasCaller(idx, n.ID) {
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
