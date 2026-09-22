package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/pluginloader"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// serviceFiles pairs a workspace.Service with its scanned files and resolved
// dependencies — Run()'s per-service scan result, hoisted from a Run()-local
// type to package level so linkPipelineState can reference it.
type serviceFiles struct {
	svc   workspace.Service
	files []string
	deps  []deps.Dependency
}

// passScope classifies whether a pass's correctness depends on seeing nodes
// from more than one service (FR.5b). scopeSameServiceOnly passes are fully
// decided by FR.2's single-service index run — a future relink can skip
// them entirely. scopeCrossService passes must be rerun against the merged
// multi-service node set to produce correct results for a newly linked
// service; among those, the ones that emit graph.Edge values also apply
// linkPipelineState.filterByTargetServices before writing, so a scoped
// relink only touches edges reachable from the target service(s) — the
// invariant FR.3's MergeServiceDBs depends on (every other service's rows
// stay untouched). This field only classifies passes today; nothing yet
// sets targetServices to actually skip a same-service-only pass — that's
// FR.5c's job (the `polyflow link --relink` command).
type passScope int

const (
	scopeSameServiceOnly passScope = iota
	scopeCrossService
)

// namedPass is one step of the linking pipeline (FR.5a). Extracted verbatim
// from Run()'s previously-inline blocks so the pipeline is enumerable and
// individually addressable — this file changes nothing about what gets
// computed or written, only that it's a list instead of ~40 sequential
// inline statements. buildLinkPasses' order is the execution order; Run()
// no longer contains any linking logic of its own, only the loop that
// drives this list plus the state it threads through it.
type namedPass struct {
	name  string
	scope passScope
	exec  func() error
}

// linkPipelineState is the mutable state every pass reads and/or writes,
// mirroring exactly what used to be Run()'s local variables captured by
// closure. Passes that are read-only w.r.t. a field (e.g. cfg) still go
// through the pointer so a later phase (FR.5b) can swap in a differently
// scoped state without touching this file's pass bodies.
type linkPipelineState struct {
	ctx   context.Context
	store *graph.SQLiteStore
	bw    *graph.BatchWriter
	cfg   *workspace.WorkspaceConfig
	opts  Options
	stats *Stats

	allSvcFiles []serviceFiles

	allNodes      []graph.Node
	allEdges      []graph.Edge
	allUnresolved []graph.UnresolvedRef

	// nodeRef caches node ID → static provenance ref ("<file>:<line>") so
	// writeEdges can stamp each edge's static Sources as it persists it,
	// letting the F.0 reconciler skip re-upserting the whole edge table.
	// Rebuilt whenever allNodes' length changes (a pass added/removed nodes).
	nodeRef    map[string]string
	nodeRefLen int

	// nodeVGRule caches node ID → valuegraph spec rule for nodes the value
	// engine resolved (Meta["vg_rule"], set by ResolveJSLocalURLs and by the
	// crossing passes). Rebuilt every writeEdges call, because an in-place Meta
	// mutation does not change allNodes' length and so would slip past the
	// nodeRef staleness guard above. writeEdges stamps L2/<rule> rather than the
	// uniform L5/<pass> on every edge touching such a node.
	nodeVGRule map[string]string

	// targetServices restricts what a scopeCrossService pass's edge-emitting
	// call persists to edges touching one of these services (see
	// filterByTargetServices). nil/empty — Run()'s only setting today — is a
	// no-op: every pass behaves exactly as it did before FR.5b. Set to a
	// non-empty slice only by a future scoped relink (FR.5c).
	targetServices []string

	// currentPass is the name of the namedPass currently executing, set by the
	// pipeline driver loop before each pass.exec() call. writeEdges reads it to
	// stamp SA.1 static provenance (Layer "L5", Rule = pass name) on every edge
	// the pass persists. Empty only if a pass body calls writeEdges outside the
	// driver loop — ValidateStaticProvenance flags the resulting rows.
	currentPass string

	// jsImportedNames: set by the js_link pass, read by js_globals.
	jsImportedNames map[string]bool
	// schemaURLTables: set by the schema_url_tables pass (Tier MS.0), one
	// per service that has a route-corroborated endpoint-declaring data
	// asset. A lookup table, never a producer — consumed by MS.1+.
	schemaURLTables map[string]*schemaurl.Table
	// schemaURLResolver: set by schema_url_tables, consumed by js_local_urls
	// (Tier MS.1/MS.2) — js_prop_clients and schema_url_links build their own
	// copy inside the Tier FX "schema_url_link" hub (internal/factpipe/
	// hub_schema_url_link.go), since a HubProvider recomputes its own facts
	// fresh per pipeline.Run call rather than sharing this field.
	schemaURLResolver *schemaurl.Resolver
	// contractRules: set by load_contract_rules, read by contract_engine and
	// contract_coverage.
	contractRules []contract.Rule
	// hintedNodes/enrichedNodes: the contract engine's working copy, built by
	// apply_hints_and_enrich and further mutated by enrich_aliases; read by
	// gin_middleware, express_middleware, amqp_handshake,
	// amqp_message_type_dispatch, contract_engine and sse_push.
	hintedNodes   []graph.Node
	enrichedNodes []graph.Node
	// contractResult: set by contract_engine, read by sse_push and
	// contract_coverage.
	contractResult contract.Result
	// handshakeResolved: set by amqp_handshake, read after the pipeline by
	// the evidence-fusion step (its config provider re-derives its ledger
	// from the persisted nodes, which still read key_dynamic since handshake
	// resolution lives only on the pre-engine working copy).
	handshakeResolved map[string]bool

	// pluginManifests: linker plugins discovered + pattern-registered by
	// loadLinkPlugins before the scan loop (plugin_link.go). Read by
	// buildLinkPasses to append one Link pass per (plugin, component) and
	// one Reconcile pass per plugin (plugin_passes.go). Empty for every
	// workspace with no .polyflow/plugins/ — buildLinkPasses' insertion is a
	// no-op in that case, so this field changes nothing for existing users.
	pluginManifests []*pluginloader.Manifest
	// pluginClients: launched plugin subprocesses, keyed by manifest name,
	// lazily populated the first time a (component, service) pair actually
	// qualifies (plugin_passes.go's launchPlugin) — a manifest with no
	// qualifying pair for this run never spawns a process. Closed by Run()'s
	// defer once the whole pipeline (including every plugin's Reconcile)
	// has finished.
	pluginClients map[string]*pluginloader.LaunchedPlugin
	// pluginComponentResults: each plugin's own per-component Link output,
	// pooled across every service that qualified — exactly the shape
	// linkplugin.ReconcileContext.ComponentResults/AllResults need. Keyed
	// pluginName -> componentID.
	pluginComponentResults map[string]map[string]pluginloader.LinkResult
	// pluginCoverageNotes: one entry per (component, service) pair that
	// package-qualified (step 3) but failed version_range gating (step 4,
	// Phase 2) — an out-of-range service, never a silent skip. Persisted by
	// Run() as graph meta "plugin_coverage" and surfaced by `polyflow doctor`,
	// mirroring toolchain.CoverageNote's role for tool/version fallbacks.
	pluginCoverageNotes []pluginloader.CoverageNote
}

// writeEdges appends edges to the store and to allEdges — the same helper
// every pass used inline as a closure before this extraction.
//
// XM.16 (docs/factpipe-cross-framework-matching-plan.md): this used to open
// a brand-new *graph.BatchWriter and Flush it immediately on every call —
// one SQLite transaction per writeEdges call, ~30+ times per link-pass
// phase, profiled at 1.29s cum on a real cedar index. Every pass's
// inter-pass state flows through allNodes/allEdges in memory, not through a
// live store read, and the whole run builds into a tmp DB that only becomes
// visible via the atomic rename at the very end of Run — so there is no
// crash-consistency or read-your-writes reason a pass needs its edges
// durable in SQLite before the *next* pass runs. Routing through the
// link-pass phase's shared st.bw and deferring the commit lets consecutive
// writeEdges/AddNode calls land in the same transaction; deleteNodes (below)
// flushes st.bw before it deletes, so a buffered-but-not-yet-committed
// node/edge can never be missed by a delete or resurrected by a later flush.
func (st *linkPipelineState) writeEdges(edges []graph.Edge) error {
	if st.nodeRef == nil || len(st.allNodes) != st.nodeRefLen {
		st.nodeRef = make(map[string]string, len(st.allNodes))
		for i := range st.allNodes {
			st.nodeRef[st.allNodes[i].ID] = evidence.StaticEdgeRef(&st.allNodes[i])
		}
		st.nodeRefLen = len(st.allNodes)
	}
	st.nodeVGRule = make(map[string]string)
	for i := range st.allNodes {
		if r := st.allNodes[i].Meta["vg_rule"]; r != "" {
			st.nodeVGRule[st.allNodes[i].ID] = r
		}
	}
	for i := range edges {
		e := edges[i]
		layer, rule := "L5", st.currentPass
		if r := st.nodeVGRule[e.From]; r != "" {
			// Tier VG.3: this edge's producer had its URL resolved by the
			// valuegraph engine — carry that layer/rule onto the edge instead
			// of the coarse per-pass fallback.
			layer, rule = "L2", r
		} else if r := st.nodeVGRule[e.To]; r != "" {
			// Tier VG.4: a crossing pass wires its minted client to the
			// enclosing function with a `calls` edge pointing *at* the resolved
			// node. That edge exists only because the crossing resolved, so it
			// carries the crossing's provenance rather than the pass's.
			layer, rule = "L2", r
		}
		evidence.StampStatic(&e, st.nodeRef[e.From], layer, rule)
		if err := st.bw.AddEdge(st.ctx, &e); err != nil {
			return err
		}
		st.allEdges = append(st.allEdges, e)
	}
	return nil
}

// deleteNodes removes ids from the store and from allNodes, and — critically
// — filters allEdges to drop anything touching a removed endpoint in the
// same call. DeleteNodes cascades edge deletion in the store, so the
// in-memory edge set must match or a later pass (the evidence reconciler)
// re-upserts an edge whose endpoint no longer exists and aborts the index on
// an FK violation. That exact split — allNodes filtered, allEdges not — was
// the synergy crash (proxy nodes deleted, dangling renders edge re-upserted);
// bundling both filters into one method means a future node-deleting pass
// can't reintroduce it by only remembering half the cleanup.
func (st *linkPipelineState) deleteNodes(ids map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	// XM.16: writeEdges/AddNode calls now defer their commit (batched across
	// the whole link-pass phase instead of one transaction per call) — a
	// node or edge added by an earlier pass this run may still be sitting in
	// st.bw's in-memory buffer, not yet in SQLite. Flush first so the DELETE
	// below actually reaches it (otherwise it silently no-ops on a row that
	// doesn't exist yet in the DB) and so any buffered edge touching a
	// to-be-deleted node is committed — and thus FK-cascaded — before the
	// delete, rather than flushed afterward as a dangling reference.
	if err := st.bw.Flush(st.ctx); err != nil {
		return fmt.Errorf("flush before delete: %w", err)
	}
	if err := st.store.DeleteNodes(st.ctx, ids); err != nil {
		return fmt.Errorf("delete nodes: %w", err)
	}
	filteredNodes := st.allNodes[:0]
	for _, n := range st.allNodes {
		if !ids[n.ID] {
			filteredNodes = append(filteredNodes, n)
		}
	}
	st.allNodes = filteredNodes
	filteredEdges := st.allEdges[:0]
	for _, e := range st.allEdges {
		if !ids[e.From] && !ids[e.To] {
			filteredEdges = append(filteredEdges, e)
		}
	}
	st.allEdges = filteredEdges
	return nil
}

// filterEdgesByService keeps only edges where at least one endpoint's owning
// service (looked up via nodeService) is in targets. Empty/nil targets is a
// no-op — matching still needs every service's nodes present (narrowing
// input would blind a match on another service's route), only what gets
// *written* is restricted.
func filterEdgesByService(edges []graph.Edge, nodeService map[string]string, targets []string) []graph.Edge {
	if len(targets) == 0 {
		return edges
	}
	want := make(map[string]bool, len(targets))
	for _, s := range targets {
		want[s] = true
	}
	kept := edges[:0]
	for _, e := range edges {
		if want[nodeService[e.From]] || want[nodeService[e.To]] {
			kept = append(kept, e)
		}
	}
	return kept
}

// filterByTargetServices applies filterEdgesByService using this pipeline's
// current node set and targetServices.
func (st *linkPipelineState) filterByTargetServices(edges []graph.Edge) []graph.Edge {
	if len(st.targetServices) == 0 {
		return edges
	}
	nodeService := make(map[string]string, len(st.allNodes))
	for _, n := range st.allNodes {
		nodeService[n.ID] = n.Service
	}
	return filterEdgesByService(edges, nodeService, st.targetServices)
}

// svcFilesOf builds the service-name → file-list map several passes need.
// Recomputed per pass rather than hoisted onto the state, matching the
// pattern the original inline blocks already used.
func (st *linkPipelineState) svcFilesOf() map[string][]string {
	svcFiles := make(map[string][]string, len(st.allSvcFiles))
	for _, sf := range st.allSvcFiles {
		svcFiles[sf.svc.Name] = sf.files
	}
	return svcFiles
}

// buildLinkPasses returns the linking pipeline in the exact order Run() used
// to execute it inline. It only builds closures — nothing runs until the
// caller executes each pass's exec function — so its length and name order
// can be asserted in a test without performing a real index (see
// link_passes_test.go).
func buildLinkPasses(st *linkPipelineState) []namedPass {
	return insertPluginPasses(st, []namedPass{
		// JCM.6: unwrap HOC wrapper identity (observer / memo / forwardRef …) —
		// stamps Meta["hoc"] and Meta["component"] so js_link Pass 1's render-
		// target index and JCM.7 can see the real component. Must precede js_link.
		// Tier FX FX.8.8 (2026-09-15) — declarative js_hoc, replacing
		// linker.LinkJSHOC. Its own dedicated pipeline.Run call (not the shared
		// factpipe_frameworks slot, which applies neither `patch:` nor a
		// stamp-in-place semantic): patches merge into an existing node's Meta
		// (js_hoc_sites hub, patterns/javascript/js_hoc.yaml), mint gap-fills
		// SPA.1's synthetic default-export component.
		{"js_hoc", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_hoc: load registry: %w", err)
			}
			fw := reg.ByName("js_hoc")
			if fw == nil {
				return fmt.Errorf("js_hoc: framework not embedded")
			}
			// Run once per service (matching FX.8.10's fix, see
			// docs/declarative-framework-pipeline-plan.md's FX.8.8 row) —
			// the hub's per-file service inference falls back to "the
			// service every node in this call belongs to" for a file with
			// no declared nodes of its own, which only holds when one hub
			// call covers exactly one service.
			var res pipeline.Result
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				snap := graph.Snapshot{Nodes: svcNodes, Files: sf.files}
				svcRes, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
				if err != nil {
					return fmt.Errorf("js_hoc: service %s: %w", sf.svc.Name, err)
				}
				res.Edges = append(res.Edges, svcRes.Edges...)
				res.Nodes = append(res.Nodes, svcRes.Nodes...)
				res.Patches = append(res.Patches, svcRes.Patches...)
			}

			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.allNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta)+2)
				for k, v := range n.Meta {
					m[k] = v
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				if p.Component {
					m["component"] = "true"
					if p.EndLine > n.EndLine {
						n.EndLine = p.EndLine
						m["end_line"] = strconv.Itoa(p.EndLine)
					}
				}
				n.Meta = m
				st.allNodes[idx] = n
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			// SPA.1: synthetic default-export component nodes for app-local
			// HOC-wrapped exports (`export default withAjax(connect(...)(Inner))`).
			for i := range res.Nodes {
				n := res.Nodes[i]
				if _, exists := byID[n.ID]; exists {
					continue
				}
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
				byID[n.ID] = len(st.allNodes) - 1
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(res.Edges)
		}},
		// JS/TS component + import-aware linking.
		{"js_link", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			jsLinker := linker.NewJSLinker()
			jsEdges, removeIDs, linkerUnresolved, importedNames := jsLinker.LinkJS(st.allNodes, st.allEdges, svcFiles)
			st.jsImportedNames = importedNames
			// Parser-level call_ref candidates that an import statement explains
			// are either resolved by the linker or point at external packages —
			// both are accounted for; the rest are real blind spots.
			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "call_ref" && importedNames[u.File+"\x00"+u.Name] {
					continue
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = append(filtered, linkerUnresolved...)
			if err := st.writeEdges(jsEdges); err != nil {
				return err
			}
			return st.deleteNodes(removeIDs)
		}},
		// Tier FX FX.8.10 (2026-09-15) — declarative js_client_routes, replacing
		// linker.LinkJSClientRoutes (SPA.2 client router + SPA.3 feature-registry
		// resolution + RT.1 render-target retyping). Its own dedicated
		// pipeline.Run call (not the shared factpipe_frameworks slot, which
		// applies no `patch:`/`resolved:`): patches merge into an existing
		// node's Meta (and retype its Type, RT.1), mint gap-fills route +
		// external feature-component nodes, resolved retracts matching
		// jsx_component_unresolved ledger rows. Runs after js_link so the
		// render-target component nodes are already resolved/stamped.
		{"js_client_routes", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_client_routes: load registry: %w", err)
			}
			fw := reg.ByName("js_client_routes")
			if fw == nil {
				return fmt.Errorf("js_client_routes: framework not embedded")
			}
			// Run once per service (the pusher_producer/rails_helpers/
			// rails_route_actions convention) rather than one call over the
			// whole graph: the hub's per-file service inference falls back
			// to "the service every node in this call belongs to" for a
			// file with no declared nodes of its own (a pure route table —
			// `export default {...}`, nothing else — the common case for
			// this pass specifically), which only holds when one hub call
			// covers exactly one service.
			var res pipeline.Result
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				snap := graph.Snapshot{Nodes: svcNodes, Files: sf.files}
				svcRes, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
				if err != nil {
					return fmt.Errorf("js_client_routes: service %s: %w", sf.svc.Name, err)
				}
				res.Edges = append(res.Edges, svcRes.Edges...)
				res.Nodes = append(res.Nodes, svcRes.Nodes...)
				res.Unresolved = append(res.Unresolved, svcRes.Unresolved...)
				res.Patches = append(res.Patches, svcRes.Patches...)
				res.Resolved = append(res.Resolved, svcRes.Resolved...)
			}

			// SPA.3: retract jsx_component_unresolved rows for keys the feature
			// registry resolved; record the non-literal lookups it couldn't.
			resolved := make(map[string]bool, len(res.Resolved))
			for _, r := range res.Resolved {
				resolved[r] = true
			}
			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "jsx_component_unresolved" && resolved[u.Service+"\x00"+u.Name] {
					continue
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = append(filtered, res.Unresolved...)

			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			// RT.1: re-typed render targets replace their existing entry rather
			// than appending. A second node with the same label would be a
			// second render target and would mint fan-out.
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.allNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta)+2)
				for k, v := range n.Meta {
					m[k] = v
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				if p.Type != "" {
					n.Type = p.Type
				}
				st.allNodes[idx] = n
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			for i := range res.Nodes {
				n := res.Nodes[i]
				if _, exists := byID[n.ID]; exists {
					continue
				}
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
				byID[n.ID] = len(st.allNodes) - 1
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(res.Edges)
		}},
		// L.W1: global/window symbol resolution + inline handler linking.
		// Runs after LinkJS so imports-first ordering is enforced via jsImportedNames.
		{"js_globals", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			globalEdges, globallyResolved, globalCollisions := linker.LinkJSGlobals(st.allNodes, st.allUnresolved, st.jsImportedNames, svcFiles)
			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "call_ref" && globallyResolved[u.File+"\x00"+u.Name] {
					continue
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = append(filtered, globalCollisions...)
			return st.writeEdges(globalEdges)
		}},
		// JS/TS cross-file inherits/implements/instantiates edges.
		{"js_type_relations", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			jsTypeEdges, jsTypeUnresolved := linker.LinkJSTypeRelations(st.allNodes, st.allEdges, svcFiles)
			if err := st.writeEdges(jsTypeEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, jsTypeUnresolved...)
			return nil
		}},
		// JS/TS typed-receiver method calls (this., typed locals/params/
		// fields, interface fan-out) — see js_receiver_type_calls.go.
		{"js_receiver_type_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			receiverTypeEdges, receiverTypeUnresolved := linker.LinkJSReceiverTypeCalls(st.allNodes, st.allEdges, svcFiles)
			if err := st.writeEdges(receiverTypeEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, receiverTypeUnresolved...)
			return nil
		}},
		// JCM.3: Redux dispatch chain — component handler → action creator →
		// action-type constant → reducer. Mints synthetic nodes for action-type
		// constants no parser captured (keyMirror keys, string consts).
		{"js_redux", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			reduxNodes, reduxEdges := linker.LinkJSRedux(st.allNodes, svcFiles)
			for i := range reduxNodes {
				n := reduxNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(reduxEdges)
		}},
		// JCM.4: MobX reactivity — tags observable/action/computed class members
		// (makeObservable / makeAutoObservable) and links autorun/reaction/when
		// callbacks to the observable members they read.
		// Tier FX FX.8.9 (2026-09-15) — declarative js_mobx, replacing
		// linker.LinkJSMobx. Its own dedicated pipeline.Run call (not the shared
		// factpipe_frameworks slot, which applies no `patch:`): patches merge
		// into an existing node's Meta (js_mobx_sites hub,
		// patterns/javascript/js_mobx.yaml), mint gap-fills a member no parser
		// captured.
		{"js_mobx", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_mobx: load registry: %w", err)
			}
			fw := reg.ByName("js_mobx")
			if fw == nil {
				return fmt.Errorf("js_mobx: framework not embedded")
			}
			var allFiles []string
			for _, sf := range st.allSvcFiles {
				allFiles = append(allFiles, sf.files...)
			}
			snap := graph.Snapshot{Nodes: st.allNodes, Files: allFiles}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
			if err != nil {
				return fmt.Errorf("js_mobx: %w", err)
			}

			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.allNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta)+2)
				for k, v := range n.Meta {
					m[k] = v
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				st.allNodes[idx] = n
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			for i := range res.Nodes {
				n := res.Nodes[i]
				if _, exists := byID[n.ID]; exists {
					continue
				}
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
				byID[n.ID] = len(st.allNodes) - 1
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(res.Edges)
		}},
		// Ruby cross-file inherits/implements/instantiates edges.
		{"ruby_type_relations", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			rubyTypeEdges, rubyTypeUnresolved := linker.LinkRubyTypeRelations(st.allNodes, svcFiles)
			if err := st.writeEdges(rubyTypeEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, rubyTypeUnresolved...)
			return nil
		}},
		// CJ / Tier FX FX.8.15: ActiveJob `perform` classes whose base class is a
		// project class, not ApplicationJob — invisible to the pattern file's
		// direct-superclass predicate, recoverable by walking the `inherits`
		// edges the pass above just emitted. Runs here, early, for two reasons:
		// promotion rewrites a candidate node's ID (the ID embeds the node
		// type), so it must land before `containment` hangs edges off the old
		// ID; and the promoted subscribers must exist before the contract
		// engine matches enqueue sites against them. A dedicated
		// pipeline.Run call — not the shared `factpipe_frameworks` slot every
		// other Tier FX framework runs through (which runs after
		// `containment`, too late for this one) — backed by
		// patterns/ruby/ruby_job_inherit.yaml + rules/ruby/ruby_job_inherit.dl
		// and the `replace:`/`delete:` emit primitives those needed
		// (internal/factpipe/emit.go). Replaces
		// internal/linker/ruby_job_inherit.go's PromoteInheritedJobPerform
		// (409 lines).
		{"ruby_job_inherit", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("ruby_job_inherit: load registry: %w", err)
			}
			fw := reg.ByName("ruby_job_inherit")
			if fw == nil {
				return fmt.Errorf("ruby_job_inherit: framework not embedded")
			}

			for _, sf := range st.allSvcFiles {
				var snap graph.Snapshot
				realNode := make(map[string]bool)
				for i := range st.allNodes {
					n := &st.allNodes[i]
					if n.Service != sf.svc.Name || n.Language != "ruby" {
						continue
					}
					snap.Nodes = append(snap.Nodes, *n)
					realNode[n.ID] = true
				}
				if len(snap.Nodes) == 0 {
					continue
				}
				for i := range st.allEdges {
					if realNode[st.allEdges[i].From] {
						snap.Edges = append(snap.Edges, st.allEdges[i])
					}
				}

				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
				if err != nil {
					return fmt.Errorf("ruby_job_inherit: service %s: %w", sf.svc.Name, err)
				}

				for i := range res.Nodes {
					if err := st.bw.AddNode(st.ctx, &res.Nodes[i]); err != nil {
						return err
					}
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				// Replace each candidate in place rather than appending, so the
				// class keeps exactly one node and the enqueue matcher sees
				// exactly one target (a second one would be fan-out, the one
				// thing this tier may not introduce).
				newByID := make(map[string]*graph.Node, len(res.Nodes))
				for i := range res.Nodes {
					newByID[res.Nodes[i].ID] = &res.Nodes[i]
				}
				for i := range st.allNodes {
					newID, ok := res.Replaced[st.allNodes[i].ID]
					if !ok {
						continue
					}
					if p := newByID[newID]; p != nil {
						st.allNodes[i] = *p
					}
				}
				// A candidate this pass did not promote is bookkeeping for a
				// class that is not a job; deleting it keeps the graph exactly
				// as it was before CJ for every PORO that happens to define
				// `perform`.
				dropped := make(map[string]bool, len(res.Deleted))
				for _, id := range res.Deleted {
					dropped[id] = true
				}
				if err := st.deleteNodes(dropped); err != nil {
					return err
				}
				// The store rows for replaced candidates are keyed by the old
				// ID, which no in-memory node carries any more — drop them
				// directly (deleteNodes would also strip the replacement from
				// allNodes, since promotion reuses nothing of the old ID).
				replacedIDs := make(map[string]bool, len(res.Replaced))
				for old := range res.Replaced {
					replacedIDs[old] = true
				}
				if len(replacedIDs) > 0 {
					if err := st.store.DeleteNodes(st.ctx, replacedIDs); err != nil {
						return fmt.Errorf("delete promoted job candidates: %w", err)
					}
					kept := st.allEdges[:0]
					for _, e := range st.allEdges {
						if replacedIDs[e.From] || replacedIDs[e.To] {
							continue
						}
						kept = append(kept, e)
					}
					st.allEdges = kept
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
				st.allUnresolved = append(st.allUnresolved, res.Ledger...)
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
			}
			return nil
		}},
		// Cross-file `ClassName.method_name` calls (Product.find_by,
		// UserCategoryRuleSet.latest_for, LicenseReportJob.create!) — the
		// same-file case is extractRubyVariables' job; this is the cross-file
		// half, same split as the type-relations pass above.
		{"ruby_class_method_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			classCallEdges, classCallUnresolved := linker.LinkRubyClassMethodCalls(st.allNodes, svcFiles)
			if err := st.writeEdges(classCallEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, classCallUnresolved...)
			return nil
		}},
		// Receiver-typed calls (`x = Product.new; x.save`, a memoized ivar, or a
		// memo-reader method like `def aws; @aws ||= AwsFacade.new_instance; end`
		// then `aws.complete_multipart_upload`) — the syntactically recoverable
		// slice of the "any other receiver needs static type inference" gap the
		// two passes above explicitly leave alone.
		{"ruby_receiver_type_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			receiverTypeEdges, receiverTypeUnresolved := linker.LinkRubyReceiverTypeCalls(st.allNodes, st.allEdges, svcFiles)
			if err := st.writeEdges(receiverTypeEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, receiverTypeUnresolved...)
			return nil
		}},
		// ActiveRecord has_many/belongs_to/has_one associations (a
		// class-granularity `calls` edge to the associated model) is Tier FX
		// FX.8.25: patterns/ruby/ruby_associations.yaml + rules/ruby/
		// ruby_associations.dl, run by the factpipe_frameworks pass below.
		//
		// Tier AT.2 (ActiveRecord model class → db/schema.rb table) is Tier FX
		// FX.8: patterns/ruby/rails_model_tables.yaml + rules/ruby/
		// rails_model_tables.dl, run by the factpipe_frameworks pass below.
		//
		// Rails filter chain (before_action/skip_before_action/rescue_from/AR
		// lifecycle → the callback method, per action, minus retractions) is
		// Tier FX FX.8: patterns/ruby/rails_filters.yaml + rules/ruby/
		// rails_filters.dl, run by the factpipe_frameworks pass below.
		//
		// C.4: a bare Ruby call the parser could not bind in its own file, resolved
		// against the methods the calling class inherits or mixes in. Must run after
		// LinkRubyTypeRelations, whose `inherits` edges are the ancestor chain this
		// walks — and which is also what keeps it from binding a call to the copy of
		// lib/dx.rb another service vendors.
		{"ruby_mixin_methods", scopeSameServiceOnly, func() error {
			// DC.6: both LinkRubyMixinMethods (upward: caller's ancestor
			// defines the name) and LinkRubyOverrideDispatch (downward: caller's
			// descendant overrides the name) scan the same call_ref snapshot
			// independently, so a base class calling its own method AND having
			// subclasses override it gets both edges -- "in addition to, not
			// instead of". Neither pass may see the other's filtered-down
			// st.allUnresolved, or whichever runs second would miss call sites
			// the first one already resolved and removed from the ledger.
			rawCallRefs := st.allUnresolved
			mixinEdges, mixinResolved, mixinCollisions, mixinNodes := linker.LinkRubyMixinMethods(st.allNodes, st.allEdges, rawCallRefs)
			overrideEdges, overrideResolved, overrideLedger := linker.LinkRubyOverrideDispatch(st.allNodes, st.allEdges, rawCallRefs)

			// DC.12: a view_helper edge's From is a `.erb` view's NodeTypeFile
			// node, minted here on demand — "ensure_scanned_files" mints the
			// same node for every file, but runs long after this pass, too
			// late for this pass's own edge write to satisfy the FK
			// constraint on first insert.
			for i := range mixinNodes {
				n := mixinNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			if len(mixinNodes) > 0 {
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, mixinNodes...)
			}

			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "call_ref" {
					key := linker.RubyCallRefKey(u.File, u.Line, u.Name)
					if mixinResolved[key] || overrideResolved[key] {
						continue
					}
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = append(filtered, mixinCollisions...)
			st.allUnresolved = append(st.allUnresolved, overrideLedger...)
			return st.writeEdges(append(mixinEdges, overrideEdges...))
		}},
		// DC.31: a bare constant reference the parser could not bind in its
		// own file (case "constant" in ruby_variables.go), resolved the same
		// way ruby_mixin_methods resolves a bare call -- against the
		// constants the referencing class inherits or mixes in. Runs on its
		// own const_ref ledger snapshot, independent of the call_ref
		// filtering above.
		{"ruby_mixin_constants", scopeSameServiceOnly, func() error {
			constEdges, constResolved, constCollisions := linker.LinkRubyMixinConstants(st.allNodes, st.allEdges, st.allUnresolved)
			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "const_ref" {
					key := linker.RubyCallRefKey(u.File, u.Line, u.Name)
					if constResolved[key] {
						continue
					}
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = append(filtered, constCollisions...)
			return st.writeEdges(constEdges)
		}},
		// DC.32: a typed-receiver Ruby call (`file.clear_lock!`) the parser
		// could not attribute (case "call"'s default receiver branch in
		// ruby_variables.go) resolves when exactly one method in the whole
		// service shares its name -- see LinkRubySoleDefinerCalls' doc
		// comment. Every typed_call_ref entry is dropped from the ledger
		// here regardless of outcome: an unresolved one is a framework/gem
		// call by construction, and no later pass will ever explain it, so
		// keeping it would only inflate deadcode's "verify N manually"
		// footer with entries nothing will ever resolve.
		{"ruby_sole_definer_calls", scopeSameServiceOnly, func() error {
			soleEdges, _ := linker.LinkRubySoleDefinerCalls(st.allNodes, st.allEdges, st.allUnresolved)
			filtered := st.allUnresolved[:0]
			for _, u := range st.allUnresolved {
				if u.Kind == "typed_call_ref" {
					continue
				}
				filtered = append(filtered, u)
			}
			st.allUnresolved = filtered
			return st.writeEdges(soleEdges)
		}},
		// RW.2: mint one http_client node per call site of a Level-1-detected
		// Ruby wrapper (patterns/ruby/wrapper_url_target.yaml), instead of
		// leaving every caller collapsed onto the wrapper's single shared,
		// unresolvable key_dynamic node. Runs before ResolveRubyHTTPHosts so an
		// abstained call site's Meta["key_dynamic_raw"] gets the same shot at
		// its host-method registry as any other dynamic Ruby http_client node.
		{"ruby_wrapper_url_call_sites", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			wrapperNodes, wrapperEdges := linker.ResolveRubyWrapperURLCallSites(st.allNodes, svcFiles)
			if len(wrapperNodes) == 0 {
				return nil
			}
			for i := range wrapperNodes {
				n := wrapperNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(wrapperEdges)
		}},
		// Tier PU.2 (the producer half) is Tier FX FX.8.11 (2026-09-15):
		// patterns/ruby/pusher_producer.yaml + rules/ruby/pusher_producer.dl,
		// run by the factpipe_frameworks pass below — same "runs before the
		// contract engine so contracts/pusher.yaml can join these to the ERB
		// consumer side" slot the hand-written pass held.
		// PU.2e (`pusher(msg, status)` helper call sites → the canonical
		// PusherClient trigger publisher, across `include`d concerns) is
		// Tier FX FX.8: patterns/ruby/pusher_helper_calls.yaml + rules/ruby/
		// pusher_helper_calls.dl, run by the factpipe_frameworks pass below.
		// Tier PU.3 (the ERB half) is Tier FX FX.8 (2026-09-14):
		// patterns/ruby/pusher_consumer.yaml + rules/ruby/pusher_consumer.dl,
		// run by the factpipe_frameworks pass below — same "runs before the
		// contract engine" slot the hand-written pass held.
		// SPA.7 (the JS half, formerly "pusher_consumer_js") is Tier FX FX.8
		// (2026-09-15): patterns/javascript/pusher_js_consumer.yaml + rules/
		// javascript/pusher_js_consumer.dl, run by the factpipe_frameworks pass
		// below. The JS-dataflow site detection (instance/subscribe/bind
		// correlation) stays hand-written Go inside the
		// pusher_js_subscribe_sites hub provider
		// (internal/factpipe/hub_pusher_js_consumer.go) — everything downstream
		// of "here is a resolved site" (mint, edges, confidence, fan-out, the
		// dynamic-channel ledger, the producer-event bridge) is declarative.
		// Tier-L: rewrite dynamic Ruby http_client URLs (`url`, `path: url`) to the
		// concrete `ENV.fetch("VAR")` their host method resolves to, cross-file, so
		// the downstream config_resolve provider can bind them (or ledger a *named*
		// deploy-secret miss) instead of an unactionable token. Runs before the
		// contract engine + config_resolve so both see the upgraded key_dynamic_raw.
		// Tier FX migration: replaces internal/linker/ruby_http_hosts.go's
		// ResolveRubyHTTPHosts. Originally scoped onto Tier VG's VG.7; re-scoped to
		// Tier FX/datalog after reading the retired Go, same "whole-service fixpoint,
		// not a bounded traversal" diagnosis as go_http_hosts — see
		// hub_ruby_http_hosts.go's doc comment for why, unlike go_http_hosts, the
		// ENTIRE algorithm (registry fold AND per-call-site resolution) stayed in the
		// hub rather than splitting the fixpoint into `.dl` joins: the call-site trace
		// needs live map lookups against the folded registry at several branch
		// points, and the path-template text synthesis it depends on has no
		// join-shaped equivalent. rules/ruby/ruby_http_hosts.dl is pure pass-through
		// (the gorm_tables precedent). Same dedicated per-service pipeline.Run loop as
		// go_http_hosts (the hub's registry fold is scoped to one service's own
		// files).
		{"ruby_http_hosts", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("ruby_http_hosts: load registry: %w", err)
			}
			fw := reg.ByName("ruby_http_hosts")
			if fw == nil {
				return fmt.Errorf("ruby_http_hosts: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files})
				if err != nil {
					return fmt.Errorf("ruby_http_hosts: service %s: %w", sf.svc.Name, err)
				}
				for _, p := range res.Patches {
					idx, ok := byID[p.ID]
					if !ok {
						continue
					}
					n := st.allNodes[idx]
					m := make(map[string]string, len(n.Meta)+len(p.Meta))
					for k, v := range n.Meta {
						m[k] = v
					}
					for k, v := range p.Meta {
						m[k] = v
					}
					for _, k := range p.DeleteMeta {
						delete(m, k)
					}
					n.Meta = m
					st.allNodes[idx] = n
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier L.2: for a Ruby http_client whose host ruby_http_hosts resolved but
		// whose path stayed key_dynamic because the sink is polymorphic (one
		// `execute`/`request` helper reached from several entry methods via
		// keyword args + `delegate`), mint one node per (entry method, endpoint
		// constant) pair. Runs after ruby_http_hosts (needs host_env_var), before
		// ApplyHints + the contract engine.
		{"ruby_poly_path_sites", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			polyNodes, polyEdges := linker.ResolveRubyPolymorphicPathSites(st.allNodes, svcFiles)
			if len(polyNodes) == 0 {
				return nil
			}
			for i := range polyNodes {
				n := polyNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(polyEdges)
		}},
		// J.2b: the Go analogue — stamp Meta["env_var"] on Go http_client nodes
		// whose base URL traces back to an os.Getenv read, so ApplyHints (J.2c)
		// can turn a workspace `hint: SOME_URL` into a target_service allowlist.
		// Must run before ApplyHints, like the Ruby pass. Tier FX migration:
		// replaces internal/linker/go_http_hosts.go's ResolveGoHTTPHosts.
		// Originally scoped onto Tier VG's VG.7 ("generalize the value engine
		// to Go/Ruby"); re-scoped to Tier FX/datalog after reading the retired
		// Go — it is a whole-service least-fixpoint dataflow analysis (facts to
		// a fixpoint, then a lookup), not a bounded per-expression traversal,
		// so patterns/go/go_http_hosts.yaml + rules/go/go_http_hosts.dl (the
		// "go_http_hosts" hub provider + a hop-unrolled fixpoint join) is the
		// natural fit, not valuegraph.Engine. A dedicated pipeline.Run call,
		// looped per service (the hub's fixpoint is scoped to one service's own
		// files — the FX.8.30 convention), not the shared factpipe_frameworks
		// slot.
		{"go_http_hosts", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("go_http_hosts: load registry: %w", err)
			}
			fw := reg.ByName("go_http_hosts")
			if fw == nil {
				return fmt.Errorf("go_http_hosts: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files})
				if err != nil {
					return fmt.Errorf("go_http_hosts: service %s: %w", sf.svc.Name, err)
				}
				for _, p := range res.Patches {
					idx, ok := byID[p.ID]
					if !ok {
						continue
					}
					n := st.allNodes[idx]
					m := make(map[string]string, len(n.Meta)+len(p.Meta))
					for k, v := range n.Meta {
						m[k] = v
					}
					for k, v := range p.Meta {
						m[k] = v
					}
					for _, k := range p.DeleteMeta {
						delete(m, k)
					}
					n.Meta = m
					st.allNodes[idx] = n
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier MS.0: discover endpoint-declaring data assets (checked-in JSON/
		// YAML that names this service's real routes) by route corroboration,
		// build the per-service URL table, ledger dead entries, and learn the
		// accessor functions that read the asset (MS.2a). Mints NOTHING — the
		// table is a resolver consumed by the two JS mint sites below, never a
		// producer of nodes. Runs before js_prop_clients so its resolver is
		// available there; it corroborates against handler nodes from the parse
		// phase (SPA-synthesized routes come later and do not name asset URLs).
		{"schema_url_tables", scopeSameServiceOnly, func() error {
			handlerPaths := make(map[string]map[string]bool)
			for i := range st.allNodes {
				n := &st.allNodes[i]
				if n.Type != graph.NodeTypeHTTPHandler {
					continue
				}
				norm, ok := schemaurl.NormalizeSchemaPath(n.Meta["path"])
				if !ok {
					continue
				}
				if handlerPaths[n.Service] == nil {
					handlerPaths[n.Service] = make(map[string]bool)
				}
				handlerPaths[n.Service][norm] = true
			}
			svcFiles := make(map[string][]string, len(st.allSvcFiles))
			for _, sf := range st.allSvcFiles {
				abs, _ := filepath.Abs(sf.svc.Path)
				svcFiles[sf.svc.Name] = walkAllFiles(abs)
			}
			schemaCfg := schemaurl.Config{
				Assets:                  st.cfg.Schema.Assets,
				MinCorroboratedPaths:    st.cfg.Schema.MinCorroboratedPaths,
				MinCorroboratedRatio:    st.cfg.Schema.MinCorroboratedRatio,
				MinEntityDiscrimination: st.cfg.Schema.MinEntityDiscrimination,
				Disable:                 st.cfg.Schema.Disable,
			}
			tables, ledger := schemaurl.LoadTables(svcFiles, handlerPaths, schemaCfg)
			st.schemaURLTables = tables
			st.schemaURLResolver = schemaurl.NewResolver(tables, svcFiles)
			st.allUnresolved = append(st.allUnresolved, ledger...)
			return nil
		}},
		// SPA.4/SPA.5: prop-injected HTTP-client wrapper (Tier FX, combined
		// schema_url_link + js_prop_clients migration, 2026-09-16). Mints
		// http_client nodes for `this.props.ajaxStatus.get(msg, url)` call
		// sites (URL KeyWalked, dynamic-URL-builder shape synthesis, or the
		// schema-asset resolver built above) so the contract engine joins
		// them to Rails routes. Runs before js_http_hosts (a template-host
		// URL it emits still gets its host recovered), before js_local_urls
		// (Tier UL) so a prop-client-minted node is a candidate there too,
		// and well before the contract engine.
		{"schema_url_link_props", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("schema_url_link_props: load registry: %w", err)
			}
			fw := reg.ByName("schema_url_link_props")
			if fw == nil {
				return fmt.Errorf("schema_url_link_props: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files, ServicePath: absSvcPath})
				if err != nil {
					return fmt.Errorf("schema_url_link_props: service %s: %w", sf.svc.Name, err)
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
				for i := range res.Nodes {
					n := res.Nodes[i]
					if _, exists := byID[n.ID]; exists {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = len(st.allNodes) - 1
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
			}
			return nil
		}},
		// Tier UL (Tier RC.2, docs/js-declarative-composition-cluster-plan.md):
		// read the URL of every JS/TS http_client the matcher left dynamic, by
		// backtracking its URL expression to the assignments in the enclosing
		// function. Runs after js_prop_clients (so a prop-client node minted
		// there is a candidate too) and before js_http_hosts and Tier CB, so a
		// path recovered here still gets its host and base-URL treatment. A
		// dedicated per-service pipeline.Run call, not factpipe_frameworks,
		// matching schema_url_links' own convention (and for the same
		// ordering reason).
		{"js_local_urls", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_local_url: load registry: %w", err)
			}
			fw := reg.ByName("js_local_url")
			if fw == nil {
				return fmt.Errorf("js_local_url: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files, ServicePath: absSvcPath})
				if err != nil {
					return fmt.Errorf("js_local_url: service %s: %w", sf.svc.Name, err)
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
				// Patches rewrite the origin node in place — a second node at
				// the same site would give the contract engine two producers.
				for _, p := range res.Patches {
					idx, ok := byID[p.ID]
					if !ok {
						continue
					}
					n := st.allNodes[idx]
					m := make(map[string]string, len(n.Meta)+len(p.Meta))
					for k, v := range n.Meta {
						m[k] = v
					}
					for _, k := range p.DeleteMeta {
						delete(m, k)
					}
					for k, v := range p.Meta {
						m[k] = v
					}
					n.Meta = m
					if p.Label != "" {
						n.Label = p.Label
					}
					st.allNodes[idx] = n
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
				}
				// Nodes are the mint: block's output — one additional
				// http_client per branch past the first, never a second edge
				// on the origin node (the fan-out invariant Tier UL exists
				// to preserve).
				for _, n := range res.Nodes {
					if _, exists := byID[n.ID]; exists {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = len(st.allNodes) - 1
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier UB.2/UB.3 (Tier RC.3, docs/js-declarative-composition-cluster-plan.md):
		// the URL crosses a JSX prop (parent computes the endpoint, child
		// requests it) and its mirror, the transport function crosses a JSX
		// prop (parent owns the wrapper, child supplies the argument). Both
		// consume js_prop_clients' prop_client_dynamic_url ledger and resolve
		// it through the "js_prop_crossings" hub's two sequential passes
		// (UB.2-then-UB.3 in one Go call, not two indexer steps — a row UB.2
		// resolves is retracted before UB.3 ever sees it). Runs after
		// js_local_urls so its local-binding walk is available for producer
		// values. A dedicated per-service pipeline.Run call, matching
		// schema_url_link_props/js_local_urls' own convention.
		{"js_prop_crossings", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_prop_crossings: load registry: %w", err)
			}
			fw := reg.ByName("js_prop_crossings")
			if fw == nil {
				return fmt.Errorf("js_prop_crossings: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			var allResolved []string
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				var svcUnresolved []graph.UnresolvedRef
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				for _, u := range st.allUnresolved {
					if u.Service == sf.svc.Name {
						svcUnresolved = append(svcUnresolved, u)
					}
				}
				if len(svcNodes) == 0 || len(svcUnresolved) == 0 {
					continue
				}
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{
					Nodes: svcNodes, Files: sf.files, ServicePath: absSvcPath, Unresolved: svcUnresolved,
				})
				if err != nil {
					return fmt.Errorf("js_prop_crossings: service %s: %w", sf.svc.Name, err)
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
				allResolved = append(allResolved, res.Resolved...)
				for i := range res.Nodes {
					n := res.Nodes[i]
					if _, exists := byID[n.ID]; exists {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = len(st.allNodes) - 1
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
			}
			if len(allResolved) > 0 {
				resolved := make(map[string]bool, len(allResolved))
				for _, r := range allResolved {
					resolved[r] = true
				}
				filtered := st.allUnresolved[:0]
				for _, u := range st.allUnresolved {
					if u.Kind == "prop_client_dynamic_url" &&
						resolved[u.Service+"\x00"+u.File+"\x00"+strconv.Itoa(u.Line)] {
						continue
					}
					filtered = append(filtered, u)
				}
				st.allUnresolved = filtered
			}
			return nil
		}},
		// Tier JH: the JS/TS analogue of the two passes above. Neither traces a
		// JS/TS client at all, so this is the only source of Meta["env_var"] /
		// Meta["host_default_literal"] for JS/TS nodes — must also run before
		// Tier CB, same as the Go/Ruby passes.
		{"js_http_hosts", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			hostNodes := linker.ResolveJSHTTPHosts(st.allNodes, svcFiles)
			if len(hostNodes) == 0 {
				return nil
			}
			for i := range hostNodes {
				n := hostNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier CB: the three passes above recover *which* env var a client's base
		// URL comes from; this one reads the path component out of that
		// variable's checked-in value and composes it onto the node's own path,
		// so a client deployed behind `API_URL=https://host/api/v2` can join the
		// `/api/v2/...` route it really calls. Runs here so it sees fresh stamps
		// from all three, and well before ApplyHints.
		{"config_baseurl", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("config_baseurl: load registry: %w", err)
			}
			fw := reg.ByName("config_baseurl")
			if fw == nil {
				return fmt.Errorf("config_baseurl: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				absPath, err := filepath.Abs(sf.svc.Path)
				if err != nil {
					absPath = sf.svc.Path
				}
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, ServicePath: absPath})
				if err != nil {
					return fmt.Errorf("config_baseurl: service %s: %w", sf.svc.Name, err)
				}
				for _, p := range res.Patches {
					idx, ok := byID[p.ID]
					if !ok {
						continue
					}
					n := st.allNodes[idx]
					m := make(map[string]string, len(n.Meta)+len(p.Meta))
					for k, v := range n.Meta {
						m[k] = v
					}
					for k, v := range p.Meta {
						m[k] = v
					}
					for _, k := range p.DeleteMeta {
						delete(m, k)
					}
					n.Meta = m
					st.allNodes[idx] = n
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		{"route_handlers", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkRouteHandlers(st.allNodes))
		}},
		// PW.1: stamp the registering route's path/method onto Go's bare
		// ws_upgrade node (see LinkWSUpgradeRoute doc comment). Must run
		// before ApplyHints/the contract engine so the stamped path is
		// visible to contracts/websocket.yaml's connect-time rule, same as
		// rails_nav_helpers below.
		{"ws_upgrade_route", scopeSameServiceOnly, func() error {
			wsUpdated := linker.LinkWSUpgradeRoute(st.allNodes)
			if len(wsUpdated) == 0 {
				return nil
			}
			nodeByID := make(map[string]int, len(st.allNodes))
			for i, n := range st.allNodes {
				nodeByID[n.ID] = i
			}
			for i := range wsUpdated {
				n := wsUpdated[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				if idx, ok := nodeByID[n.ID]; ok {
					st.allNodes[idx] = n
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		{"grpc_handlers", scopeSameServiceOnly, func() error {
			grpcEdges, grpcUnresolved := linker.LinkGRPCHandlers(st.allNodes)
			if err := st.writeEdges(grpcEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, grpcUnresolved...)
			return nil
		}},
		// Tier FX FX.8.28: synthesize http_handler nodes for Devise's default
		// (non-overridden, non-skipped) route scopes. Replaces
		// internal/linker/rails_devise.go's LinkDeviseDefaultRoutes (365
		// lines). A dedicated pipeline.Run call, looped per service (the
		// hub derives its `svc` string from nodes[0].Service, so each call
		// must be scoped to exactly one service's own nodes — the FX.8.8/
		// 8.10 convention), not the shared factpipe_frameworks slot.
		{"rails_devise_default_routes", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("rails_devise: load registry: %w", err)
			}
			fw := reg.ByName("rails_devise")
			if fw == nil {
				return fmt.Errorf("rails_devise: framework not embedded")
			}
			byID := make(map[string]bool, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = true
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files})
				if err != nil {
					return fmt.Errorf("rails_devise: service %s: %w", sf.svc.Name, err)
				}
				for i := range res.Nodes {
					n := res.Nodes[i]
					if byID[n.ID] {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = true
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Rails routes name their action by convention, not by the Meta["handler"]
		// receiver string LinkRouteHandlers keys on. That resolution is Tier FX
		// FX.8.24: patterns/ruby/rails_route_actions.yaml + rules/ruby/
		// rails_route_actions.dl, run by the factpipe_frameworks pass below.
		{"route_components", scopeSameServiceOnly, func() error {
			routeCompEdges, routeCompUnresolved := linker.LinkRouteComponents(st.allNodes)
			if err := st.writeEdges(routeCompEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, routeCompUnresolved...)
			return nil
		}},
		{"templ_components", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkTemplComponents(st.allNodes))
		}},
		// Tier FX FX.8.27: templ <script src> → JS file imports, and JS DOM
		// target → templ/HTML/JSX/stylesheet element `defined_in` (mints
		// element nodes). Replaces internal/linker/templ_layer.go's
		// LinkTemplScripts + LinkDOMDefinitions (531 lines) — merged into
		// one pass (was two: templ_scripts, dom_definitions) since both are
		// one hub provider now. A dedicated pipeline.Run call, not the
		// shared factpipe_frameworks slot — dom_declutter (downstream)
		// must run right after this pass and dom_contracts, well before
		// factpipe_frameworks's late position (after containment).
		{"templ_layer", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("templ_layer: load registry: %w", err)
			}
			fw := reg.ByName("templ_layer")
			if fw == nil {
				return fmt.Errorf("templ_layer: framework not embedded")
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes})
			if err != nil {
				return fmt.Errorf("templ_layer: %w", err)
			}
			byID := make(map[string]bool, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = true
			}
			for i := range res.Nodes {
				n := res.Nodes[i]
				if byID[n.ID] {
					continue
				}
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
				byID[n.ID] = true
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(res.Edges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			return nil
		}},
		// templ producer (data-testid/id) attribute -> JS attribute-selector
		// consumer `dom_contract` (IA.5): component -> JS site directly, no
		// intermediate node, so investigate/walkFlows reach it in one hop.
		{"dom_contracts", scopeSameServiceOnly, func() error {
			_, contractEdges, contractUnresolved := linker.LinkDOMContracts(st.allNodes)
			if err := st.writeEdges(contractEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, contractUnresolved...)
			return nil
		}},
		// Declutter the DOM layer: fold bare jQuery/querySelector "$" nodes into
		// direct caller→element edges, then drop element nodes nothing selects.
		// Must run after every DOM-linking pass (dom_definitions, dom_contracts)
		// and before containment, so an orphan element is simply edge-less.
		{"dom_declutter", scopeSameServiceOnly, func() error {
			foldEdges, foldRemove := linker.FoldDOMSelectors(st.allNodes, st.allEdges)
			if err := st.writeEdges(foldEdges); err != nil {
				return err
			}
			if err := st.deleteNodes(foldRemove); err != nil {
				return err
			}
			return st.deleteNodes(linker.PruneOrphanElements(st.allNodes, st.allEdges))
		}},
		// Structural backbone: service→file→declaration + struct→method contains
		// edges (mints synthetic service/file nodes, so persist them before wiring).
		{"containment", scopeSameServiceOnly, func() error {
			containNodes, containEdges := linker.LinkContainment(st.allNodes)
			for i := range containNodes {
				n := containNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(containEdges)
		}},
		// Backbone completeness: mint a bare file node for every scanned file that
		// LinkContainment skipped (barrel/re-export-only and enum-only files declare
		// nothing containment-shaped). Runs before the JS import-edge pass so those
		// files are already valid, persisted import targets rather than mint-on-miss
		// fallbacks there.
		{"ensure_scanned_files", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			barrelNodes, barrelEdges := linker.EnsureAllScannedFiles(st.allNodes, svcFiles)
			for i := range barrelNodes {
				n := barrelNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(barrelEdges)
		}},
		// js_lazy_import_calls (a lazy-loaded, string-keyed dynamic import —
		// `fn(() => import(path), 'exportName')`) migrated to Tier FX
		// (patterns/javascript/js_lazy_import_calls.yaml +
		// rules/javascript/js_lazy_import_calls.dl), run by the
		// "factpipe_frameworks" pass below, itself after ensure_scanned_files
		// so every file already has a NodeTypeFile node to fall back to.
		// JS/TS + Ruby file-level import edges (file→file between NodeTypeFile nodes).
		// Runs after LinkContainment so the file nodes are present in allNodes.
		{"js_import_edges", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			jsImportEdges, updatedFileNodes, jsImportUnresolved := linker.LinkJSImportEdges(st.allNodes, svcFiles)
			for i := range updatedFileNodes {
				n := updatedFileNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(jsImportEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, jsImportUnresolved...)
			return nil
		}},
		// JS/TS wrapped API-client calls (services/ApiServices.js-style shared
		// axios/fetch wrappers): mints an http_client node for a call to a
		// WB.1-detected wrapper even across files and even when the URL argument
		// is a local variable, not a literal — producer_alias_url_call/obj_call
		// require a literal at the call site and never fire otherwise.
		{"js_api_wrapper_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			wrapperNodes, wrapperEdges, wrapperUnresolved, dupIDs := linker.LinkJSAPIWrapperCalls(st.allNodes, svcFiles)
			st.allUnresolved = append(st.allUnresolved, wrapperUnresolved...)
			for i := range wrapperNodes {
				n := wrapperNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(wrapperEdges); err != nil {
				return err
			}
			// RT.5: remove producer_alias_url_call duplicates now covered by a
			// wrapper call-site node.
			return st.deleteNodes(dupIDs)
		}},
		// Closes a same-file/one-import-hop gap in the JS KeyWalker's
		// same-file call inlining (contract/keywalk_javascript_local.go's
		// walkJSCallExpr): a call to a helper whose whole body is `return
		// TABLE[param]` (TABLE a local or one-import-hop-away object literal)
		// resolves for any call site passing a literal at that parameter
		// position. Patches existing key_dynamic nodes in place; mints
		// nothing new.
		{"js_call_table_lookups", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			patched := linker.LinkJSCallTableLookups(st.allNodes, svcFiles)
			if len(patched) == 0 {
				return nil
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, n := range patched {
				if idx, ok := byID[n.ID]; ok {
					st.allNodes[idx] = n
				}
				nn := n
				if err := st.bw.AddNode(st.ctx, &nn); err != nil {
					return err
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Same shape, a switch instead of a table: a call to a function or
		// object-literal method whose whole body is a switch on one of its
		// own parameters, each case returning a further-walkable expression.
		// Patches existing key_dynamic nodes in place; mints nothing new.
		{"js_switch_dispatch_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			patched := linker.LinkJSSwitchDispatchCalls(st.allNodes, svcFiles)
			if len(patched) == 0 {
				return nil
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, n := range patched {
				if idx, ok := byID[n.ID]; ok {
					st.allNodes[idx] = n
				}
				nn := n
				if err := st.bw.AddNode(st.ctx, &nn); err != nil {
					return err
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier FX (2026-09-15) — declarative stylesheet_imports, replacing
		// linker.LinkStylesheetImports. Its own dedicated pipeline.Run call,
		// once per service (the FX.8.8/8.10 convention — the hub's svc
		// comes from nodes[0].Service, correct only when one call covers
		// exactly one service): stylesheet @import graph + containment for
		// the selector and @font-face nodes the stylesheet parser mints.
		{"stylesheet_imports", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("stylesheet_imports: load registry: %w", err)
			}
			fw := reg.ByName("stylesheet_imports")
			if fw == nil {
				return fmt.Errorf("stylesheet_imports: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				snap := graph.Snapshot{Nodes: svcNodes, Files: sf.files}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
				if err != nil {
					return fmt.Errorf("stylesheet_imports: service %s: %w", sf.svc.Name, err)
				}
				for i := range res.Nodes {
					n := res.Nodes[i]
					if _, exists := byID[n.ID]; exists {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = len(st.allNodes) - 1
				}
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			}
			return st.bw.Flush(st.ctx)
		}},
		// Tier K.3: Rails asset pipeline — `//= require` directives plus the
		// `javascript_include_tag` page bindings that sit on top of them — is
		// Tier FX FX.8 (resolve_path step 4): patterns/javascript/
		// sprockets_directives.yaml + rules/javascript/sprockets_directives.dl
		// (the header-directive half) and patterns/erb/sprockets_includes.yaml
		// + rules/erb/sprockets_includes.dl (the include-tag half), both run by
		// the factpipe_frameworks pass below. Replaces
		// internal/linker/sprockets_assets.go + internal/sprockets.
		//
		// Tier K.2: Rails view layer — partial nesting, the controller→template
		// convention, and the react_component mount seam. Tier FX (FX.8.33,
		// 2026-09-16): declarative "rails_views" hub, replacing
		// linker.LinkRailsViews. Cross-service (a react_component mount and
		// its JSX implementation are routinely different services), so this
		// runs the same way react_prop_urls does — one pipeline.Run call over
		// the whole graph + every service's files flattened, not the
		// per-service factpipe_frameworks loop.
		{"rails_views", scopeCrossService, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("rails_views: load registry: %w", err)
			}
			fw := reg.ByName("rails_views")
			if fw == nil {
				return fmt.Errorf("rails_views: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes, Files: sf.files})
				if err != nil {
					return fmt.Errorf("rails_views: service %s: %w", sf.svc.Name, err)
				}
				for i := range res.Nodes {
					n := res.Nodes[i]
					if _, exists := byID[n.ID]; exists {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = len(st.allNodes) - 1
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			}
			return nil
		}},
		// React prop URL forwarding: resolve a `js_api_wrapper_call_site`
		// http_client whose URL is a component prop set on the Rails side
		// (`react_component("X", { some_url: foo_path })`) to the route that prop
		// names, so the contract engine can join the frontend call to its API
		// handler. Cross-service (ERB and JSX are often separate services); runs
		// after rails_views + js_api_wrapper_calls, before the contract engine.
		{"react_prop_urls", scopeCrossService, func() error {
			svcFiles := st.svcFilesOf()
			var allFiles []string
			for _, fs := range svcFiles {
				allFiles = append(allFiles, fs...)
			}
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("react_prop_urls: load registry: %w", err)
			}
			fw := reg.ByName("react_prop_urls")
			if fw == nil {
				return fmt.Errorf("react_prop_urls: framework not embedded")
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes, Files: allFiles})
			if err != nil {
				return fmt.Errorf("react_prop_urls: %w", err)
			}
			if len(res.Patches) == 0 {
				return nil
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.allNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta))
				for k, v := range n.Meta {
					m[k] = v
				}
				for _, k := range p.DeleteMeta {
					delete(m, k)
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				if p.Label != "" {
					n.Label = p.Label
				}
				st.allNodes[idx] = n
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// JG.1: grade path-evidence on JS/TS http_client producers whose host
		// segment is still an opaque wildcard after every host resolver above
		// (ResolveJSHTTPHosts, ResolveConfigBaseURLPaths, LinkReactPropURLs).
		// The Go SSA-wrapper pass stamps this inline; JS/TS producers were
		// ungraded, so the contract engine's weak-path fan-out suppressor never
		// fired for them and a `*/*/unlock` client matched a devise route in an
		// unrelated fleet service. Mutates the working copy in place so
		// ApplyHints/EnrichRouteGroups carry the grade to the contract engine;
		// re-persists so the stored graph carries it too.
		//
		// Tier FX FX.8.16 (2026-09-16): patterns/generic/js_http_grade.yaml +
		// rules/generic/js_http_grade.dl, backed by the js_http_grade hub
		// (internal/factpipe/hub_js_http_grade.go), replacing
		// internal/linker/js_http_grade.go's GradeJSHTTPProducers. A dedicated
		// pipeline.Run call, not the shared factpipe_frameworks slot (which
		// runs too late — this must land before ApplyHints/the contract
		// engine, same as every other early dedicated-call FX.8 pass). Called
		// globally over st.allNodes, not per-service — the hub grades each
		// node purely off its own Meta, no cross-node lookup at all, matching
		// the retired Go's own st.allNodes call shape exactly (gorm_tables
		// precedent).
		{"js_http_grade", scopeCrossService, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("js_http_grade: load registry: %w", err)
			}
			fw := reg.ByName("js_http_grade")
			if fw == nil {
				return fmt.Errorf("js_http_grade: framework not embedded")
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes})
			if err != nil {
				return fmt.Errorf("js_http_grade: %w", err)
			}
			if len(res.Patches) == 0 {
				return nil
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.allNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta))
				for k, v := range n.Meta {
					m[k] = v
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				st.allNodes[idx] = n
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		{"ruby_import_edges", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			rubyImportEdges, rubyImportUnresolved := linker.LinkRubyImportEdges(st.allNodes, svcFiles)
			if err := st.writeEdges(rubyImportEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, rubyImportUnresolved...)
			return nil
		}},
		// SH1: shell script cross-file invocation (`bash x.sh`, `source x.sh`,
		// bare `./x.sh`) as `calls` edges (meta via=exec). Runs after every
		// shell file's own (script) scope node exists in st.allNodes (minted
		// unconditionally by internal/parser/shell.go during the main parse
		// phase, not by a link pass), same ordering requirement as the JS/Ruby
		// import-edge passes above.
		{"shell_invocation_edges", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			shellEdges, shellUnresolved := linker.LinkShellInvocationEdges(st.allNodes, svcFiles)
			if err := st.writeEdges(shellEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, shellUnresolved...)
			return nil
		}},
		// SQ1: .sql REFERENCES/FOREIGN KEY clauses as `references` edges
		// between schema-declared table nodes. Runs after every service's
		// .sql files have been parsed (internal/parser/sql.go mints the
		// table nodes during the main parse phase, not by a link pass).
		{"sql_reference_edges", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			sqlEdges, sqlUnresolved := linker.LinkSQLReferences(st.allNodes, svcFiles)
			if err := st.writeEdges(sqlEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, sqlUnresolved...)
			return nil
		}},
		// Y.3c: parse table names out of datastore call SQL and terminate each
		// query/persist at a real table entity (mints table nodes).
		{"tables", scopeSameServiceOnly, func() error {
			tableNodes, tableEdges, tableUnresolved := linker.LinkTables(st.allNodes)
			for i := range tableNodes {
				n := tableNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, tableUnresolved...)
			return st.writeEdges(tableEdges)
		}},
		// Tier GT / Tier FX FX.8.23: terminate GORM datastore call nodes (no
		// literal SQL) at the schema-declared table their Go model maps to.
		// Runs after "tables" so every CREATE TABLE node already exists in
		// st.allNodes, and before "datastores" so a resolved GORM call is
		// never also fanned onto a generic engine node as unresolved — a
		// dedicated pipeline.Run call (not the shared factpipe_frameworks
		// slot every other Tier FX framework runs through, which runs after
		// "datastores", too late for this one), backed by
		// patterns/go/gorm_tables.yaml + rules/go/gorm_tables.dl and the
		// "gorm_tables" hub provider (internal/factpipe/hub_gorm_tables.go).
		// Replaces internal/linker/gorm_tables.go's LinkGormModelTables
		// (529 lines). Called globally over st.allNodes, not per-service —
		// the hub keys every lookup off each node's own n.Service (no
		// nodes[0].Service single-service fallback to get wrong), matching
		// the retired Go's own call shape exactly.
		{"gorm_model_tables", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("gorm_tables: load registry: %w", err)
			}
			fw := reg.ByName("gorm_tables")
			if fw == nil {
				return fmt.Errorf("gorm_tables: framework not embedded")
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes})
			if err != nil {
				return fmt.Errorf("gorm_tables: %w", err)
			}
			if err := st.writeEdges(res.Edges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			return nil
		}},
		// Terminate any datastore call node that "tables" / "gorm_model_tables"
		// left unresolved at its service's logical DB engine. Runs last of the
		// three so a call with a concrete table edge is not also fanned onto an
		// (ambiguous) engine node.
		{"datastores", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkDatastores(st.allNodes, st.allEdges))
		}},
		// Y.4: join server response DTOs to the client interfaces that mirror their
		// JSON shape (cross-language response_of). Runs after all returns/consumes
		// edges are collected so it can gate on server-declared response structs.
		{"response_shapes", scopeCrossService, func() error {
			return st.writeEdges(st.filterByTargetServices(linker.LinkResponseShapes(st.allNodes, st.allEdges)))
		}},
		// Y.6: join a createResource loader's http_client to the reactive signal it
		// feeds (http_client → signal flows_to). Needs the calls edges from Pass 2,
		// so it runs after the bulk of edges are collected.
		{"resource_signals", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkResourceSignals(st.allNodes, st.allEdges))
		}},
		{"sse_clients", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkSSEClients(st.allNodes))
		}},
		// Broker hint linking (via: rabbitmq + exchange).
		{"broker_hints", scopeCrossService, func() error {
			hintNodes, hintEdges, hintUnresolved := linker.LinkBrokerHints(st.cfg.Links, st.allNodes)
			st.allUnresolved = append(st.allUnresolved, hintUnresolved...)
			for i := range hintNodes {
				n := hintNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(st.filterByTargetServices(hintEdges))
		}},
		// L.W0: resolve Rails route-helper names on nav_link_rails_helper nodes to
		// real method+path so the http contract rule (G.1 nav variant) can match them.
		// Must run before ApplyHints so the resolved path is visible to the engine.
		//
		// Tier FX FX.8.7 (2026-09-15): patterns/ruby/rails_helpers.yaml +
		// rules/ruby/rails_helpers.dl, backed by the rails_helper_routes hub
		// (internal/factpipe/hub_rails_helpers.go), replacing
		// internal/linker/rails_helpers.go's ResolveRailsNavHelpers. Run through
		// a dedicated pipeline.Run call — not the shared `factpipe_frameworks`
		// slot every other Tier FX framework runs through — because this pass
		// resolves an EXISTING http_client node's meta in place (same id), and
		// factpipe_frameworks treats `mint:` as gap-fill-only, skipping any id
		// that already names a real node. Same precedent as ruby_job_inherit.
		{"rails_nav_helpers", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("rails_nav_helpers: load registry: %w", err)
			}
			fw := reg.ByName("rails_helpers")
			if fw == nil {
				return fmt.Errorf("rails_nav_helpers: framework not embedded")
			}

			snap := graph.Snapshot{Nodes: st.allNodes}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, snap)
			if err != nil {
				return fmt.Errorf("rails_nav_helpers: %w", err)
			}

			// Build a quick ID→index map for O(1) in-place updates to allNodes.
			nodeByID := make(map[string]int, len(st.allNodes))
			for i, n := range st.allNodes {
				nodeByID[n.ID] = i
			}
			for i := range res.Nodes {
				n := res.Nodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				if idx, ok := nodeByID[n.ID]; ok {
					st.allNodes[idx] = n
				} else {
					// Fan-out candidate: new node not in allNodes yet.
					st.allNodes = append(st.allNodes, n)
				}
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			return nil
		}},
		// M.0: file-based route synthesis (Next.js, SvelteKit, Nuxt, Remix).
		// Runs after per-file parsing and all linking passes above, before the
		// contract engine, so synthesized http_handler nodes participate in
		// cross-service linking.
		{"file_route_synthesis", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("file_routes: load registry: %w", err)
			}
			fw := reg.ByName("file_routes")
			if fw == nil {
				return fmt.Errorf("file_routes: framework not embedded")
			}
			byID := make(map[string]bool, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = true
			}
			for _, sf := range st.allSvcFiles {
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				// Route synthesis needs ALL service files (including unparsed like .svelte, .vue),
				// not just the parser-handled subset — file-based routers are identified by their
				// filesystem paths, which exist regardless of whether a parser is registered.
				allSvcFilesList := walkAllFiles(absSvcPath)
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: allSvcFilesList, ServicePath: absSvcPath})
				if err != nil {
					return fmt.Errorf("file_routes: service %s: %w", sf.svc.Name, err)
				}
				for i := range res.Nodes {
					n := res.Nodes[i]
					if byID[n.ID] {
						continue
					}
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
					byID[n.ID] = true
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				if err := st.writeEdges(res.Edges); err != nil {
					return err
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			}
			return nil
		}},
		// Tier MS.1/MS.2 (Tier FX, combined schema_url_link + js_prop_clients
		// migration, 2026-09-16): pin an entity from a discovered asset's
		// vocabulary and resolve a direct `<pinned>.<key>` read or a
		// learnt-accessor call on a JS/TS http_client the matcher left
		// dynamic. Rewrites the existing node in place with the resolved
		// path + provenance Meta; mints nothing here (the no-node
		// prop-client sites are handled inside schema_url_link_props).
		// Runs after js_local_urls so a node it could already read is left
		// alone — a dedicated per-service pipeline.Run call, not
		// factpipe_frameworks, and a SEPARATE framework from
		// schema_url_link_props even though both share one hub-side
		// resolver-builder, precisely because of this ordering requirement
		// (see hub_schema_url_link.go's doc comment).
		{"schema_url_links", scopeSameServiceOnly, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("schema_url_link_sweep: load registry: %w", err)
			}
			fw := reg.ByName("schema_url_link_sweep")
			if fw == nil {
				return fmt.Errorf("schema_url_link_sweep: framework not embedded")
			}
			byID := make(map[string]int, len(st.allNodes))
			for i := range st.allNodes {
				byID[st.allNodes[i].ID] = i
			}
			for _, sf := range st.allSvcFiles {
				var svcNodes []graph.Node
				for i := range st.allNodes {
					if st.allNodes[i].Service == sf.svc.Name {
						svcNodes = append(svcNodes, st.allNodes[i])
					}
				}
				if len(svcNodes) == 0 {
					continue
				}
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: svcNodes, Files: sf.files, ServicePath: absSvcPath})
				if err != nil {
					return fmt.Errorf("schema_url_link_sweep: service %s: %w", sf.svc.Name, err)
				}
				st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
				for _, p := range res.Patches {
					idx, ok := byID[p.ID]
					if !ok {
						continue
					}
					n := st.allNodes[idx]
					m := make(map[string]string, len(n.Meta)+len(p.Meta))
					for k, v := range n.Meta {
						m[k] = v
					}
					for _, k := range p.DeleteMeta {
						delete(m, k)
					}
					for k, v := range p.Meta {
						m[k] = v
					}
					n.Meta = m
					if p.Label != "" {
						n.Label = p.Label
					}
					st.allNodes[idx] = n
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
				}
			}
			return st.bw.Flush(st.ctx)
		}},
		// Cross-service contract linking (HTTP, AMQP, Hub, Jobs, Pusher, WebSocket via contracts/*.yaml).
		// opts.ContractsDir may add workspace-custom rules on top of the embedded defaults (G.5).
		{"load_contract_rules", scopeCrossService, func() error {
			rules, err := contract.Load(contractdata.FS, st.opts.ContractsDir)
			if err != nil {
				return fmt.Errorf("contract rules: %w", err)
			}
			st.contractRules = rules
			return nil
		}},
		// G.3 pre-engine enrichment: reconstruct full route paths for nodes inside
		// router groups (gin r.Group / chi r.Route). This is a contextual node-join
		// that normalizers cannot perform; it mutates only the working copy
		// (hintedNodes/enrichedNodes), not the persisted allNodes.
		{"apply_hints_and_enrich", scopeCrossService, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("hints: load registry: %w", err)
			}
			fw := reg.ByName("hints")
			if fw == nil {
				return fmt.Errorf("hints: framework not embedded")
			}
			links := make([]graph.LinkHint, len(st.cfg.Links))
			for i, l := range st.cfg.Links {
				links[i] = graph.LinkHint{From: l.From, To: l.To, BaseURL: l.BaseURL, Hint: l.Hint}
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.allNodes, Links: links})
			if err != nil {
				return fmt.Errorf("hints: %w", err)
			}
			st.hintedNodes = make([]graph.Node, len(st.allNodes))
			copy(st.hintedNodes, st.allNodes)
			byID := make(map[string]int, len(st.hintedNodes))
			for i := range st.hintedNodes {
				byID[st.hintedNodes[i].ID] = i
			}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.hintedNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta))
				for k, v := range n.Meta {
					m[k] = v
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				st.hintedNodes[idx] = n
			}
			st.enrichedNodes = contract.EnrichRouteGroups(st.hintedNodes)
			// The composition above is computed for matching, on a working copy. Agents
			// query the *stored* graph, so the composed route has to reach it too —
			// otherwise a gin handler declared inside `v1.Group("/api/v1")` is persisted
			// reading `/users/:id`, which routes nowhere and which no search for the
			// real path can find. Only label + meta["full_path"] are written back; see
			// contract.setPath for why meta["path"] must stay raw.
			return persistComposedRoutes(st.ctx, st.bw, st.enrichedNodes, st.allNodes)
		}},
		// Tier FX declarative framework pipeline: per-service, gate the embedded
		// framework registry to the service's dependencies, re-parse its
		// source, and run extract → bridge → derive → emit. FX.7 migrated the
		// gin_middleware and express_middleware chains here (handler --calls-->
		// the middleware guarding it), replacing the hand-written link passes.
		{"factpipe_frameworks", scopeSameServiceOnly, func() error {
			return runFactpipeFrameworks(st)
		}},
		// G.7 pre-engine enrichment: resolve alias/instance bindings and one-hop
		// wrapper functions. Alias binding nodes (NodeTypeVariable with alias_name
		// or instance_name meta) are removed from the working copy; their info feeds
		// the alias table used to rewrite call nodes before Engine.Link.
		{"enrich_aliases", scopeCrossService, func() error {
			enriched, aliasUnresolved := contract.EnrichAliases(st.enrichedNodes)
			st.enrichedNodes = enriched
			st.allUnresolved = append(st.allUnresolved, aliasUnresolved...)
			return nil
		}},
		// K.6 step 3 pre-engine enrichment: carry a runtime-negotiated queue name
		// across the repo boundary on the registration handshake's field symbol, so
		// the existing queue_name contract can join publisher to consumer. Resolves
		// keys only — it emits no edges of its own.
		// Tier FX (2026-09-16): patterns/generic/amqp_handshake.yaml +
		// rules/generic/amqp_handshake.dl, backed by the amqp_handshake hub
		// (internal/factpipe/hub_amqp_handshake.go), replacing
		// internal/linker/amqp_handshake.go's LinkAMQPHandshake. Extraction
		// (amqp_field_pair/amqp_field_symbol/amqp_field_string_dig) still
		// lives in patterns/ruby/amqp_handshake.yaml, consumed by the
		// general matcher registry, untouched — only the cross-service join
		// migrated. A dedicated pipeline.Run call over the WHOLE graph
		// (st.enrichedNodes), same reason as hints: the join needs both
		// sides of the repo boundary in one pass, which the shared
		// factpipe_frameworks per-service loop can never see (and never
		// tries to — this framework's `language: generic` shields it from
		// that loop, same as hints/config_baseurl).
		{"amqp_handshake", scopeCrossService, func() error {
			reg, err := pipeline.LoadEmbedded()
			if err != nil {
				return fmt.Errorf("amqp_handshake: load registry: %w", err)
			}
			fw := reg.ByName("amqp_handshake")
			if fw == nil {
				return fmt.Errorf("amqp_handshake: framework not embedded")
			}
			res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: st.enrichedNodes})
			if err != nil {
				return fmt.Errorf("amqp_handshake: %w", err)
			}

			byID := make(map[string]int, len(st.enrichedNodes))
			for i := range st.enrichedNodes {
				byID[st.enrichedNodes[i].ID] = i
			}
			resolved := map[string]bool{}
			for _, p := range res.Patches {
				idx, ok := byID[p.ID]
				if !ok {
					continue
				}
				n := st.enrichedNodes[idx]
				m := make(map[string]string, len(n.Meta)+len(p.Meta))
				for k, v := range n.Meta {
					m[k] = v
				}
				for _, k := range p.DeleteMeta {
					delete(m, k)
				}
				for k, v := range p.Meta {
					m[k] = v
				}
				n.Meta = m
				if p.Label != "" {
					n.Label = p.Label
				}
				st.enrichedNodes[idx] = n
				// Only the resolved (not the ambiguous) relation sets
				// queue_name — exactly the case DropResolvedRefs' original
				// `resolved` map tracked.
				if p.Meta["queue_name"] != "" {
					resolved[linker.HandshakeSiteKey(n.Service, n.File, n.Line)] = true
				}
			}
			st.handshakeResolved = resolved
			st.allUnresolved = linker.DropResolvedRefs(st.allUnresolved, resolved)
			st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
			return nil
		}},
		// FX.8.14: the message-type dispatch join, distinct from and unblocked
		// by the queue-name handshake above — it answers "what breaks if I
		// change this message's shape" rather than "where does it go". Now a
		// contracts/amqp.yaml rule (message_type key, confidence_ceiling
		// stamped at pattern-match time), so it runs inside contract_engine
		// below rather than as its own pass.
		{"contract_engine", scopeCrossService, func() error {
			eng := &contract.Engine{}
			result := eng.Link(st.enrichedNodes, st.contractRules, st.cfg.Links)
			result.Edges = st.filterByTargetServices(result.Edges)
			st.contractResult = result

			for i := range result.Nodes {
				n := result.Nodes[i]
				_ = st.bw.AddNode(st.ctx, &n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(result.Edges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, result.Unresolved...)
			st.stats.ContractEdges, st.stats.CrossLinks = countContractEdges(result.Edges, st.enrichedNodes)
			return nil
		}},
		// Server→client SSE push edge, mirroring the http_call connection edge
		// the engine just produced for eventsource_connect nodes. Must run after
		// contractResult.Edges exists (see linker.LinkSSEPush).
		{"sse_push", scopeCrossService, func() error {
			return st.writeEdges(st.filterByTargetServices(linker.LinkSSEPush(st.enrichedNodes, st.contractResult.Edges)))
		}},
		// G.5: persist per-kind coverage so `polyflow doctor` can report matched/unresolved.
		{"contract_coverage", scopeCrossService, func() error {
			coverage := contract.ComputeCoverage(st.contractRules, st.contractResult)
			if coverageJSON, marshalErr := json.Marshal(coverage); marshalErr == nil {
				_ = st.store.SetMeta(st.ctx, "contract_coverage", string(coverageJSON))
			}
			return nil
		}},
	})
}
