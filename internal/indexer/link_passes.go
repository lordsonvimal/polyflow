package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/pluginloader"
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

	// targetServices restricts what a scopeCrossService pass's edge-emitting
	// call persists to edges touching one of these services (see
	// filterByTargetServices). nil/empty — Run()'s only setting today — is a
	// no-op: every pass behaves exactly as it did before FR.5b. Set to a
	// non-empty slice only by a future scoped relink (FR.5c).
	targetServices []string

	// jsImportedNames: set by the js_link pass, read by js_globals.
	jsImportedNames map[string]bool
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
func (st *linkPipelineState) writeEdges(edges []graph.Edge) error {
	if st.nodeRef == nil || len(st.allNodes) != st.nodeRefLen {
		st.nodeRef = make(map[string]string, len(st.allNodes))
		for i := range st.allNodes {
			st.nodeRef[st.allNodes[i].ID] = evidence.StaticEdgeRef(&st.allNodes[i])
		}
		st.nodeRefLen = len(st.allNodes)
	}
	bwE := graph.NewBatchWriter(st.store)
	for i := range edges {
		e := edges[i]
		evidence.StampStatic(&e, st.nodeRef[e.From])
		if err := bwE.AddEdge(st.ctx, &e); err != nil {
			return err
		}
		st.allEdges = append(st.allEdges, e)
	}
	return bwE.Flush(st.ctx)
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
		// ActiveRecord has_many/belongs_to/has_one associations — a
		// class-granularity `calls` edge to the associated model, the same
		// shape emitClassMethodCall uses for a call that lands on no method
		// node (an ActiveRecord finder, a `scope` macro).
		{"ruby_associations", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			assocEdges, assocUnresolved := linker.LinkRubyAssociations(st.allNodes, svcFiles)
			if err := st.writeEdges(assocEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, assocUnresolved...)
			return nil
		}},
		// Rails filter chain: before_action/around_action/after_action → the method
		// the callback names, from the declaring class and from each action it
		// guards. Needs the Ruby method nodes' qualified_name, so it runs after the
		// parse phase; independent of the type-relation edges above.
		{"rails_filters", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			filterEdges, filterUnresolved := linker.LinkRailsFilters(st.allNodes, svcFiles)
			if err := st.writeEdges(filterEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, filterUnresolved...)
			return nil
		}},
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
		// Tier PU.2: the Pusher producer half. Mint one `publisher` node per
		// resolvable `PusherClient.new(obj, <chan>).notify_x(...)` call site
		// (channel segment + event name both statically knowable there),
		// instead of leaving them all collapsed onto the wrapper's single
		// key_dynamic `.trigger` node. Runs before the contract engine so
		// contracts/pusher.yaml can join these to the ERB consumer side.
		{"pusher_producer_forward", scopeSameServiceOnly, func() error {
			pubNodes, pubEdges := linker.EnrichPusherProducers(st.allNodes, st.svcFilesOf())
			if len(pubNodes) == 0 {
				return nil
			}
			for i := range pubNodes {
				n := pubNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(pubEdges)
		}},
		// Tier PU.3: the Pusher consumer half. Mint one `subscriber` node per
		// resolvable ERB `pusher_config(channel:, event:)` /
		// `render "shared/pusher", pusher_channel:` call site — the browser's
		// channel/event arrive as a server-built prop and never appear as JS
		// literals, so the keyed consumer node lives on the ERB call site.
		// Runs before the contract engine so contracts/pusher.yaml joins these
		// to the PU.2 producer nodes.
		{"pusher_consumer_erb", scopeSameServiceOnly, func() error {
			subNodes, subEdges := linker.EnrichPusherConsumers(st.allNodes, st.svcFilesOf())
			if len(subNodes) == 0 {
				return nil
			}
			for i := range subNodes {
				n := subNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			return st.writeEdges(subEdges)
		}},
		// Tier-L: rewrite dynamic Ruby http_client URLs (`url`, `path: url`) to the
		// concrete `ENV.fetch("VAR")` their host method resolves to, cross-file, so
		// the downstream config_resolve provider can bind them (or ledger a *named*
		// deploy-secret miss) instead of an unactionable token. Runs before the
		// contract engine + config_resolve so both see the upgraded key_dynamic_raw.
		{"ruby_http_hosts", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			hostNodes := linker.ResolveRubyHTTPHosts(st.allNodes, svcFiles)
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
		// Must run before ApplyHints, like the Ruby pass.
		{"go_http_hosts", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			hostNodes := linker.ResolveGoHTTPHosts(st.allNodes, svcFiles)
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
			svcDirs := make(map[string]string, len(st.allSvcFiles))
			for _, sf := range st.allSvcFiles {
				svcDirs[sf.svc.Name] = sf.svc.Path
			}
			prefixNodes := linker.ResolveConfigBaseURLPaths(st.allNodes, svcDirs)
			if len(prefixNodes) == 0 {
				return nil
			}
			for i := range prefixNodes {
				n := prefixNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
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
		// Rails routes name their action by convention, not by the Meta["handler"]
		// receiver string LinkRouteHandlers keys on, so they need their own pass.
		{"rails_devise_default_routes", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			deviseNodes := linker.LinkDeviseDefaultRoutes(svcFiles)
			if len(deviseNodes) == 0 {
				return nil
			}
			for i := range deviseNodes {
				n := deviseNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			return st.bw.Flush(st.ctx)
		}},
		// Rails routes name their action by convention, not by the Meta["handler"]
		// receiver string LinkRouteHandlers keys on, so they need their own pass.
		{"rails_route_actions", scopeSameServiceOnly, func() error {
			railsActionEdges, railsActionUnresolved := linker.LinkRailsRouteActions(st.allNodes)
			if err := st.writeEdges(railsActionEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, railsActionUnresolved...)
			return nil
		}},
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
		// templ <script src> → JS file imports.
		{"templ_scripts", scopeSameServiceOnly, func() error {
			scriptEdges, scriptUnresolved := linker.LinkTemplScripts(st.allNodes)
			if err := st.writeEdges(scriptEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, scriptUnresolved...)
			return nil
		}},
		// JS DOM target → templ element `defined_in` (creates templ_element nodes).
		{"dom_definitions", scopeSameServiceOnly, func() error {
			domNodes, domEdges, domUnresolved := linker.LinkDOMDefinitions(st.allNodes)
			for i := range domNodes {
				n := domNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(domEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, domUnresolved...)
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
		// A lazy-loaded, string-keyed dynamic import (`fn(() => import(path),
		// 'exportName')`) is a runtime property lookup on the resolved module
		// object -- no static call site names the export directly, so it
		// reads as permanently zero-caller. Runs after ensure_scanned_files
		// so every file already has a NodeTypeFile node to fall back to when
		// the call site has no enclosing function (module-level command
		// registration, gitnexus's confirmed live shape).
		{"js_lazy_import_calls", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			lazyEdges, lazyUnresolved := linker.LinkJSLazyImportCalls(st.allNodes, svcFiles)
			st.allUnresolved = append(st.allUnresolved, lazyUnresolved...)
			return st.writeEdges(lazyEdges)
		}},
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
			wrapperNodes, wrapperEdges, dupIDs := linker.LinkJSAPIWrapperCalls(st.allNodes, svcFiles)
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
		// Tier K.5: stylesheet @import graph + containment for the selector and
		// @font-face nodes the stylesheet parser mints.
		{"stylesheet_imports", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			cssNodes, cssEdges, cssUnresolved := linker.LinkStylesheetImports(st.allNodes, svcFiles)
			for i := range cssNodes {
				n := cssNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(cssEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, cssUnresolved...)
			return nil
		}},
		// Tier K.3: Rails asset pipeline — `//= require` directives plus the
		// `javascript_include_tag` page bindings that sit on top of them.
		{"sprockets_assets", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			assetNodes, assetEdges, assetUnresolved := linker.LinkSprocketsAssets(st.allNodes, svcFiles)
			for i := range assetNodes {
				n := assetNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(assetEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, assetUnresolved...)
			return nil
		}},
		// Tier K.2: Rails view layer — partial nesting, the controller→template
		// convention, and the react_component mount seam.
		{"rails_views", scopeSameServiceOnly, func() error {
			svcFiles := st.svcFilesOf()
			viewNodes, viewEdges, viewUnresolved := linker.LinkRailsViews(st.allNodes, svcFiles)
			for i := range viewNodes {
				n := viewNodes[i]
				if err := st.bw.AddNode(st.ctx, &n); err != nil {
					return err
				}
				st.allNodes = append(st.allNodes, n)
			}
			if err := st.bw.Flush(st.ctx); err != nil {
				return err
			}
			if err := st.writeEdges(viewEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, viewUnresolved...)
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
			changed := linker.LinkReactPropURLs(st.allNodes, svcFiles)
			if len(changed) == 0 {
				return nil
			}
			for i := range changed {
				n := changed[i]
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
		{"datastores", scopeSameServiceOnly, func() error {
			return st.writeEdges(linker.LinkDatastores(st.allNodes))
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
		{"rails_nav_helpers", scopeSameServiceOnly, func() error {
			railsUpdated, railsUnresolved := linker.ResolveRailsNavHelpers(st.allNodes)
			// Build a quick ID→index map for O(1) in-place updates to allNodes.
			nodeByID := make(map[string]int, len(st.allNodes))
			for i, n := range st.allNodes {
				nodeByID[n.ID] = i
			}
			for i := range railsUpdated {
				n := railsUpdated[i]
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
			st.allUnresolved = append(st.allUnresolved, railsUnresolved...)
			return nil
		}},
		// M.0: file-based route synthesis (Next.js, SvelteKit, Nuxt, Remix).
		// Runs after per-file parsing and all linking passes above, before the
		// contract engine, so synthesized http_handler nodes participate in
		// cross-service linking.
		{"file_route_synthesis", scopeSameServiceOnly, func() error {
			fileNodeMap := make(map[string][]graph.Node, len(st.allNodes))
			for _, n := range st.allNodes {
				if n.File != "" {
					fileNodeMap[n.File] = append(fileNodeMap[n.File], n)
				}
			}
			nodesInFile := func(absFile string) []graph.Node { return fileNodeMap[absFile] }

			for _, sf := range st.allSvcFiles {
				absSvcPath, _ := filepath.Abs(sf.svc.Path)
				// Route synthesis needs ALL service files (including unparsed like .svelte, .vue),
				// not just the parser-handled subset — file-based routers are identified by their
				// filesystem paths, which exist regardless of whether a parser is registered.
				allSvcFilesList := walkAllFiles(absSvcPath)
				fr := linker.SynthesizeFileRoutes(absSvcPath, sf.svc.Name, allSvcFilesList, sf.deps, nodesInFile)
				for i := range fr.Nodes {
					n := fr.Nodes[i]
					if err := st.bw.AddNode(st.ctx, &n); err != nil {
						return err
					}
					st.allNodes = append(st.allNodes, n)
				}
				if err := st.bw.Flush(st.ctx); err != nil {
					return err
				}
				if err := st.writeEdges(fr.Edges); err != nil {
					return err
				}
				st.allUnresolved = append(st.allUnresolved, fr.Unresolved...)
			}
			return nil
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
			st.hintedNodes = linker.ApplyHints(st.cfg.Links, st.allNodes, st.allEdges)
			st.enrichedNodes = contract.EnrichRouteGroups(st.hintedNodes)
			// The composition above is computed for matching, on a working copy. Agents
			// query the *stored* graph, so the composed route has to reach it too —
			// otherwise a gin handler declared inside `v1.Group("/api/v1")` is persisted
			// reading `/users/:id`, which routes nowhere and which no search for the
			// real path can find. Only label + meta["full_path"] are written back; see
			// contract.setPath for why meta["path"] must stay raw.
			return persistComposedRoutes(st.ctx, st.bw, st.enrichedNodes, st.allNodes)
		}},
		// Gin middleware chain: handler --calls--> the middleware guarding it
		// (r.Use/group.Use), so `impact`/`context` on a route or a middleware
		// function surfaces the other side without a separate tool.
		{"gin_middleware", scopeSameServiceOnly, func() error {
			mwEdges, mwUnresolved := linker.LinkGinMiddleware(st.enrichedNodes, st.allEdges)
			if err := st.writeEdges(mwEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, mwUnresolved...)
			return nil
		}},
		// Express middleware chain: same handler-calls-guard modeling as Gin's,
		// for `app.use(mw)`/`router.use(mw)` registrations (see
		// internal/linker/express_middleware.go for the v1 same-file/
		// same-receiver scope this covers).
		{"express_middleware", scopeSameServiceOnly, func() error {
			mwEdges, mwUnresolved := linker.LinkExpressMiddleware(st.enrichedNodes, st.allEdges)
			if err := st.writeEdges(mwEdges); err != nil {
				return err
			}
			st.allUnresolved = append(st.allUnresolved, mwUnresolved...)
			return nil
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
		{"amqp_handshake", scopeCrossService, func() error {
			handshakeUnresolved, handshakeResolved := linker.LinkAMQPHandshake(st.enrichedNodes)
			st.handshakeResolved = handshakeResolved
			st.allUnresolved = linker.DropResolvedRefs(st.allUnresolved, handshakeResolved)
			st.allUnresolved = append(st.allUnresolved, handshakeUnresolved...)
			return nil
		}},
		// AH follow-up: the message-type dispatch join, distinct from and
		// unblocked by the queue-name handshake above — it answers "what breaks
		// if I change this message's shape" rather than "where does it go".
		// Emits edges directly (not through the contract engine) since the join
		// is on a bare constant name, not a structural role any contracts/*.yaml
		// rule already models.
		{"amqp_message_type_dispatch", scopeCrossService, func() error {
			mtEdges := st.filterByTargetServices(linker.LinkAMQPMessageTypeDispatch(st.enrichedNodes))
			if len(mtEdges) == 0 {
				return nil
			}
			return st.writeEdges(mtEdges)
		}},
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
