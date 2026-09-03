// Package mcpserver exposes polyflow's query layer (search, context, impact,
// trace) as MCP tools over any MCP transport (the CLI serves stdio). It is a
// thin wrapper: each tool returns the same JSON contract as the CLI command
// of the same name, including the unresolved-references recall gauge, so
// agents get identical answers whichever surface they use.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/trace"
)

// semanticsParagraph is embedded in context/impact/trace descriptions so
// agents understand verification_state without reading plan docs.
const semanticsParagraph = "Edges carry verification_state: `verified` edges are confirmed by runtime " +
	"or declared contracts — do not re-verify. `candidate` edges are static-only — " +
	"one cheap grep confirms them. `observed_only_gap` edges were seen at runtime " +
	"but missed by static analysis — treat as real. The verification_summary and " +
	"unresolved sections are always present; empty means clean, absent means error. " +
	"The trust section reports this workspace's last measured eval recall; " +
	"measured=false or stale=true means answers here are unaudited — weigh the " +
	"unresolved section more heavily. " +
	"epistemic.verdict is `exact` or `lower_bound`; when `lower_bound`, epistemic.causes " +
	"names which section below explains why — check that section instead of re-deriving " +
	"the reason yourself."

// minVerificationPasses reports whether an edge's VerificationState meets the
// requested threshold. Default "any" passes all states including empty
// (pre-fusion edges). "observed" requires runtime evidence (verified or
// observed_only_gap). "declared"/"verified" require the fully-confirmed state.
// With the current state set, "declared" and "verified" are equivalent; the
// distinction is reserved for a future declared-contract-only sub-state.
func minVerificationPasses(state, minVerification string) bool {
	switch minVerification {
	case "", "any":
		return true
	case "observed":
		return state == graph.StateVerified || state == graph.StateObservedOnlyGap
	case "declared", "verified":
		return state == graph.StateVerified
	}
	return true
}

// Store is the subset of graph.SQLiteStore the MCP tools need.
type Store interface {
	SearchNodes(ctx context.Context, query string, limit int) ([]*graph.Node, error)
	ListUnresolvedRefs(ctx context.Context) ([]graph.UnresolvedRef, error)
	GetMeta(ctx context.Context, key string) (string, error)
}

// Server wires the query layer behind MCP tool handlers. The store and index
// are swappable so a long-lived stdio session picks up reindexes.
type Server struct {
	mu         sync.RWMutex
	store      Store
	idx        *graph.AdjacencyIndex
	searcher   *semantic.Searcher // optional; nil → FTS-only fallback
	staleAfter time.Duration      // workspace evidence.stale_after (0 = no stale check)
	ops        *ops.Store         // nil → tool-call audit logging disabled (UB.2)

	// fleetSearchers holds one Searcher per locally-resolved fleet member
	// (GR.3's search federation), keyed by service name — nil/empty when
	// this workspace is not a fleet member, or has only itself as a
	// "member". The search tool federates across all of them by default
	// (docs/global-fleet-registry-plan.md's federation-scope decision) and
	// narrows to one via searchInput.Service.
	fleetSearchers map[string]*semantic.Searcher

	// fleetUnresolvedRefs is every locally-resolved fleet member's own
	// unresolved-ref ledger, unioned by the caller (cmd/polyflow/mcp.go's
	// runMCP, via fleetAwareUnresolvedRefs) the same way idx already unions
	// every member's nodes/edges — the ledger isn't part of either table,
	// so idx being fleet-aware doesn't make this fleet-aware for free. nil
	// when not a fleet member (or the caller never wired it): the deadcode
	// tool falls back to store.ListUnresolvedRefs, the single-store
	// behavior this had before fleet mode existed.
	fleetUnresolvedRefs []graph.UnresolvedRef
}

// SetSearcher wires a hybrid Searcher. Call after New; safe to call while
// serving (protected by mu). When nil, search falls back to FTS-only SearchNodes.
func (s *Server) SetSearcher(sr *semantic.Searcher) {
	s.mu.Lock()
	s.searcher = sr
	s.mu.Unlock()
}

// SetFleetSearchers wires one Searcher per locally-resolved fleet member
// (GR.3), keyed by service name, for the search tool to federate across.
// Pass nil or an empty map to disable federation (search falls back to the
// single Searcher set via SetSearcher).
func (s *Server) SetFleetSearchers(searchers map[string]*semantic.Searcher) {
	s.mu.Lock()
	s.fleetSearchers = searchers
	s.mu.Unlock()
}

// SetFleetUnresolvedRefs wires the fleet-wide unresolved-ref ledger (every
// locally-resolved member's own graph.Store.ListUnresolvedRefs, unioned) —
// see the fleetUnresolvedRefs field doc. Pass nil to fall back to the
// single active store's own ledger (the deadcode tool's pre-fleet-mode
// behavior).
func (s *Server) SetFleetUnresolvedRefs(refs []graph.UnresolvedRef) {
	s.mu.Lock()
	s.fleetUnresolvedRefs = refs
	s.mu.Unlock()
}

// SetOps wires the tool-call audit store into the server. Safe to call at
// any time; nil disables audit logging.
func (s *Server) SetOps(o *ops.Store) {
	s.mu.Lock()
	s.ops = o
	s.mu.Unlock()
}

func (s *Server) opsStore() *ops.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ops
}

// Reload swaps in a freshly built store and index (after `polyflow index`
// rewrote the graph database). Also invalidates the vector matrix cache.
func (s *Server) Reload(store Store, idx *graph.AdjacencyIndex) {
	s.mu.Lock()
	s.store = store
	s.idx = idx
	sr := s.searcher
	s.mu.Unlock()
	if sr != nil {
		sr.Invalidate()
	}
}

// snapshot returns a consistent store+index+searcher triple for one tool call.
func (s *Server) snapshot() (Store, *graph.AdjacencyIndex, *semantic.Searcher) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store, s.idx, s.searcher
}

// fleetSearchersSnapshot returns the fleet-member searcher map wired via
// SetFleetSearchers, for the search tool's federation path.
func (s *Server) fleetSearchersSnapshot() map[string]*semantic.Searcher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fleetSearchers
}

// New builds an MCP server exposing the polyflow query tools. The returned
// *Server handle supports Reload; the *mcp.Server is what runs the session.
// staleAfter propagates the workspace evidence.stale_after threshold (0 = no
// stale check — caller can pass the workspace default when loading config).
//
// When enabled is false the server registers ONLY a `status` probe tool and
// skips the seven query tools, so an agent session runs with a genuinely
// polyflow-free tool list — the control arm of a token A/B (see `polyflow mcp
// off`). index/status/serve stay fully functional regardless.
func New(store Store, idx *graph.AdjacencyIndex, version string, staleAfter time.Duration, enabled bool) (*mcp.Server, *Server) {
	s := &Server{store: store, idx: idx, staleAfter: staleAfter}

	srv := mcp.NewServer(&mcp.Implementation{Name: "polyflow", Version: version}, nil)

	if !enabled {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "status",
			Description: "polyflow is DISABLED for this session (run `polyflow mcp on`, then " +
				"reconnect/restart the session). No code-graph tools are available.",
		}, auditTool(s, "status", s.disabledProbe))
		return srv, s
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name: "investigate",
		Description: "Investigate a symptom or feature and get the root-cause neighbourhood in one " +
			"call: the resolved node with its source inlined, its callers and callees, the flows it " +
			"sits on, and the short coverage_unresolved list. Prefer this over search/context/trace/" +
			"read when the task is \"understand X / find why X\" — it resolves and assembles the whole " +
			"picture so you don't sequence those calls yourself. The returned edges are the resolved " +
			"set; the only thing to verify by grep is coverage_unresolved. If target_candidates is " +
			"non-empty, re-query with target_service/target_type to pin the intended node. " +
			semanticsParagraph,
	}, auditTool(s, "investigate", s.investigate))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search the indexed code graph for nodes (functions, methods, variables, " +
			"HTTP handlers, …), flow chains, or doc chunks matching a query. Query may be " +
			"natural language. Leads with the matching nodes, each carrying an inline source " +
			"snippet — so one call shows you the code, no separate read needed. A flows hit's " +
			"entry node is the starting point for trace. Use this to find the exact node before " +
			"calling context, impact, or trace. Searches the current workspace by " +
			"default; pass service='*' to search the whole fleet, or service='<member>' " +
			"to scope to one fleet member.",
	}, auditTool(s, "search", s.search))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "context",
		Description: "Show the call context around a node: upstream callers, downstream callees, " +
			"and cross-service edges. Pass files instead of target to get the ranked files related " +
			"to those file(s) (graph neighborhood, direct references first) — answers 'where is " +
			"the code connected to X' without grep exploration. The unresolved section lists " +
			"references in the traversed files the indexer could not resolve — verify those " +
			"manually, edges may be missing. " +
			"Structural plumbing (Rails filter-chain wiring, mixins, containment, JSX/DOM render-tree " +
			"edges, test-file edges) is hidden by default, keyed off task (generate shows render_tree, " +
			"others hide all five); pass include_noise to restore specific classes or 'all'. hidden_by_class in the " +
			"response tallies what was hidden by class, present whenever anything was — never silently dropped. " +
			"Set max_tokens to cap output size (over budget, per-node detail rolls up per file), " +
			"summary to force the rollup, snippet_lines to inline source snippets per node. " +
			"If target_candidates is non-empty in the response, re-query with target_service to pin the right node. " +
			semanticsParagraph,
	}, auditTool(s, "context", s.context))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "impact",
		Description: "Show the blast radius of changing a node or a file: everything that " +
			"transitively depends on it, entry points, and affected services. Directly answers " +
			"'what is impacted if I change X'. Defaults to direction=backward ('what breaks if I " +
			"change this'), which does NOT include what the target itself calls into. If the " +
			"question is 'what else do I need to touch/change' rather than strictly 'what would " +
			"break', pass direction=both — backward alone misses things the target depends on " +
			"(e.g. a model association a controller action guards on). Treat every file/node in the " +
			"result as a confirmed hit — do not re-verify it with grep or a Read 'just to check': " +
			"the ONLY thing worth manually checking is the unresolved section, which lists references " +
			"the indexer could not resolve (the blast radius may be under-reported there). An empty " +
			"unresolved section means the answer is complete as given. Output defaults to a compact budget: " +
			"small blast radii return full per-node detail, large ones auto-roll-up per file, each " +
			"line reporting direct_nodes/contained_nodes and a sample caller so you can tell a real " +
			"hit from container fan-out. Structural plumbing (Rails filter-chain wiring, mixins, " +
			"containment, JSX/DOM render-tree edges, test-file edges) is hidden from the default result entirely " +
			"(impact has no task concept, so this is unconditional); pass include_noise to restore " +
			"specific classes or 'all'. hidden_by_class in the response tallies what was hidden by " +
			"class, present whenever anything was — never silently dropped. Set max_tokens to raise or lower that cap (negative = " +
			"unlimited), summary to force the rollup, snippet_lines to inline source snippets per " +
			"node. " +
			"If target_candidates is non-empty in the response, re-query with target_service to pin the right node. " +
			semanticsParagraph,
	}, auditTool(s, "impact", s.impact))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "trace",
		Description: "Trace multi-hop flows from a node as linear chains (A -> B -> C), " +
			"including cross-service hops. Treat every hop as a confirmed answer — do not re-verify " +
			"it with grep or Read once trace has named it. The ONLY thing worth manually checking is " +
			"the unresolved section, which lists references the indexer could not resolve (chains may " +
			"be incomplete there); an empty unresolved section means the chain is complete as given. " +
			"If a chain dead-ends at a node with no further edges, that is very likely the real end of " +
			"the call graph (e.g. a queue binding with no wired consumer in the source), not a gap in " +
			"this tool — prefer that reading over spending many grep/Read calls hunting for code that " +
			"may not exist. " +
			"Chains containing structural plumbing (Rails filter-chain wiring, mixins, containment, " +
			"JSX/DOM render-tree edges, test-file edges) are hidden by default, keyed off task (generate shows " +
			"render_tree, others hide all five); pass include_noise to restore specific classes or " +
			"'all'. hidden_by_class in the response tallies chains hidden by class, present whenever " +
			"anything was — never silently dropped. " +
			"If target_candidates is non-empty in the response, re-query with target_service to pin the right node. " +
			semanticsParagraph,
	}, auditTool(s, "trace", s.trace))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "flows",
		Description: "Resolve an end-to-end flow from a starting point (node id, 'GET /path' route, " +
			"file, or a natural-language feature description) across service boundaries: HTTP, jobs, " +
			"pub/sub, gRPC, renders, and calls. Prefer this over trace/context/impact when the question " +
			"is 'how does X flow across services' rather than 'what is adjacent to X' — it resolves the " +
			"whole path in one call instead of find→trace→cross-service→repeat. Start with `entrypoints` " +
			"or `flows`; treat `verified`/`candidate` hops as authoritative and only grep the endpoints " +
			"listed under coverage.unresolved — that is the token-saving contract of this tool. A hub " +
			"node with many same-type branches into one service rolls up into a single hop " +
			"(verification_state \"rollup\") instead of dumping every branch. Set max_tokens to cap " +
			"output size (over budget, flows collapse to a sample plus the coverage tally, which is " +
			"never trimmed). If target_candidates is non-empty, re-query with target_service to pin the " +
			"right node. " + semanticsParagraph,
	}, auditTool(s, "flows", s.flows))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "entrypoints",
		Description: "Catalog entry nodes (HTTP routes, broker subscribers, background workers, gRPC " +
			"and GraphQL server handlers) filterable by service and a feature keyword. Maps a request or " +
			"feature to a starting node with no grep — use this before `flows` to find where a flow " +
			"begins. CLI commands and scheduled/cron tasks are not catalogued yet (no such node type " +
			"exists in the graph).",
	}, auditTool(s, "entrypoints", s.entrypoints))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "deadcode",
		Description: "List function/method/variable nodes with no live inbound edge (a caller, a reader), " +
			"plus dead Rails views, excluding nodes already classified as entry points (HTTP handlers, " +
			"routes, workers, subscribers, gRPC/GraphQL handlers, and functions tagged as entrypoints). " +
			"Set include_types to also flag struct/interface/type_alias declarations nothing references. " +
			"Set transitive to flag code reachable only from other dead code (a function whose only " +
			"callers are themselves dead) — sound on Go/TS, a lead only on Ruby where the static call " +
			"graph is partial. A candidate list for removal, not a certainty: dynamic dispatch, " +
			"reflection, exported package-public API, and other call shapes the static graph can't see " +
			"all show up here as false positives — verify a hit before deleting it, the same way you " +
			"would with any other polyflow answer's unresolved section. Scope with service/file to " +
			"narrow a large-fleet scan.",
	}, auditTool(s, "deadcode", s.deadcode))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "resolve",
		Description: "Resolve a natural-language description or partial name to ranked candidate nodes, " +
			"with the same target_service/target_type disambiguation context/impact/trace/flows use. Call " +
			"this first when unsure which node a query will land on — it cuts a round-trip versus calling " +
			"context/impact/trace/flows and re-querying after seeing target_candidates.",
	}, auditTool(s, "resolve", s.resolve))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read",
		Description: "Return the EXACT source lines of a symbol (function, method, class, struct, " +
			"interface) by node id — its true span, not the whole file. Use after search/hierarchy/context/" +
			"resolve give you an id, instead of opening the file. span_known=false means the exact end was " +
			"unknown and a bounded window was returned; max_lines caps runaway spans. Pass targets (a list, " +
			"up to 20) instead of target to read several symbols — possibly from different files — in one " +
			"call instead of one round trip each; results come back in request order under results, and a " +
			"single unresolved target is reported in that entry's error field without failing the others.",
	}, auditTool(s, "read", s.read))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "hierarchy",
		Description: "Return the structural shape of the workspace: service → directory → file → " +
			"top-level symbols, with roll-up counts. Use this FIRST to orient in an unfamiliar repo " +
			"instead of ls/find/grep — one call replaces directory exploration. Scope with service/path; " +
			"raise depth to 3 for symbols. Symbol-level `id` feeds directly into read, context, or impact.",
	}, auditTool(s, "hierarchy", s.hierarchy))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "unknown_edges",
		Description: "List confidence-bearing edges (http_call, publishes, …) at or below a " +
			"confidence tier, fleet-wide, in one call — the bulk-audit counterpart to calling `context` " +
			"on the synthetic `unresolved` node, which only covers one traversal's budget. Default " +
			"min_confidence is \"unknown\"; pass \"partial\"/\"inferred\"/\"static\" to widen the report. " +
			"A producer with a better-resolved edge elsewhere in the fleet-merged graph (e.g. its own " +
			"local store said unknown but bridge.db resolved it cross-service) is excluded — it is not " +
			"actually unresolved fleet-wide. Scope with service/edge_type to narrow a large-fleet scan. " +
			"from_id feeds directly into read/context/trace for the producer call site.",
	}, auditTool(s, "unknown_edges", s.unknownEdges))

	return srv, s
}

// disabledProbeInput is the (empty) argument set for the disabled-mode probe.
type disabledProbeInput struct{}

// disabledProbe is the only tool registered when polyflow is toggled off. It
// exists so the server still starts and the model can see, unambiguously, that
// the code-graph tools are intentionally absent for this session.
func (s *Server) disabledProbe(ctx context.Context, req *mcp.CallToolRequest, in disabledProbeInput) (*mcp.CallToolResult, any, error) {
	return jsonResult(map[string]string{
		"status": "disabled",
		"detail": "polyflow query tools are disabled for this session. Run `polyflow mcp on`, " +
			"then reconnect/restart the session to re-enable search/context/impact/trace/flows/entrypoints/resolve/deadcode.",
	})
}

// auditTool wraps a tool handler with UB.2 tool-call audit recording
// (source: "mcp"): params is the input struct as JSON, result is the tool's
// full response payload as JSON (mirroring jsonResult's TextContent). Every
// registered tool handler shares this one signature
// (ctx, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error), which is
// what makes one generic wrapper cover all of them instead of duplicating
// recording logic per tool. MCP runs over stdio with no HTTP clients to
// notify, so unlike the UI's audit middleware this never broadcasts SSE.
func auditTool[In any](s *Server, name string, fn func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		o := s.opsStore()
		if o == nil {
			return fn(ctx, req, in)
		}

		start := time.Now()
		res, out, err := fn(ctx, req, in)
		dur := time.Since(start)

		paramsJSON, _ := json.Marshal(in)
		status, errMsg, result := "ok", "", ""
		if err != nil {
			status = "error"
			errMsg = err.Error()
		} else if res != nil {
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					result = tc.Text
					break
				}
			}
		}

		if _, _, rerr := o.RecordCall(ctx, ops.Call{
			Source:     "mcp",
			Tool:       name,
			Params:     string(paramsJSON),
			DurationMS: dur.Milliseconds(),
			Status:     status,
			Error:      errMsg,
			Result:     result,
		}); rerr != nil {
			fmt.Fprintf(os.Stderr, "polyflow: ops record failed: %v\n", rerr)
		}

		return res, out, err
	}
}

// jsonResult marshals v into a text content block, the same JSON the CLI
// emits for the equivalent command.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

// effectiveDepth maps the MCP depth convention onto the internal one. Over
// JSON an omitted depth arrives as 0, so unlike the CLI, 0 cannot mean
// unlimited here: omitted/0 → def, -1 → unlimited (internal 0).
func effectiveDepth(depth, def int) int {
	switch {
	case depth < 0:
		return 0
	case depth == 0:
		return def
	}
	return depth
}

// defaultImpactBudget is the token budget applied to the MCP impact tool when
// the caller does not specify max_tokens. Unlike the CLI (where 0 means
// unlimited), an MCP consumer is an agent paying for every token it ingests, so
// the default is a compact budget: small blast radii still return full per-node
// detail (they fit the budget), while large ones auto-roll-up to the per-file
// summary instead of dumping the verbose form into the agent's context.
const defaultImpactBudget = impact.DefaultBudget

// multiRootBudgetCeiling caps the token budget honoured on a merged
// multi-service impact (impact.BuildMulti unions N full blast radii, so the
// payload scales with N). An explicit max_tokens<=0 (unlimited) or an
// oversized positive budget, honoured as-is, produced a measured 71.8KB
// payload on a 2-service merge — over Claude Code's own tool-output
// threshold, which silently truncates to a 2KB preview + on-disk pointer and
// forces extra round-trips to recover the data. Clamping straight down to
// defaultImpactBudget (2000) avoided that regression but also discarded a
// caller's reasonable, bounded request (e.g. 15000-20000): the agent asking
// for more headroom got the same compact rollup as an agent asking for
// nothing, well below the threshold that actually caused the regression.
// This ceiling sits comfortably under that ~60-70KB trip point (≈15000
// tokens at the 4-bytes/token estimate) while still respecting a caller's
// explicit request up to that point.
const multiRootBudgetCeiling = 15000

// defaultContextBudget mirrors defaultImpactBudget's reasoning for the
// context tool: an unset max_tokens must still mean "compact default", not
// unlimited — context was the one MCP tool still falling through to a raw
// unbounded ApplyBudget(0) when a caller left max_tokens unset, silently
// returning full per-node detail (source snippets included) for however
// large the traversal happened to be, unlike trace/impact/investigate which
// all default to a bounded budget already.
const defaultContextBudget = 2000

// effectiveBudget maps an MCP max_tokens input to an impact.ApplyBudget budget.
// 0 (unset) → the compact default; a negative value → 0 (unlimited, opt-in);
// any positive value is honoured as-is.
func effectiveBudget(maxTokens int) int {
	return effectiveBudgetWithDefault(maxTokens, defaultImpactBudget)
}

// effectiveBudgetWithDefault is effectiveBudget generalized over the
// per-tool default (impact/trace share defaultImpactBudget; context has its
// own, smaller defaultContextBudget).
func effectiveBudgetWithDefault(maxTokens, def int) int {
	switch {
	case maxTokens == 0:
		return def
	case maxTokens < 0:
		return 0
	}
	return maxTokens
}

// resolveNode finds the best node match for a search query with optional
// pre-filters, mirroring the CLI's target resolution (graph.ResolveTarget).
// idx is the server's fleet-aware merged index (s.snapshot(), already
// unioning every locally-resolved fleet member's full graph) — wrapped
// together with the local store via graph.FleetSearcher so a query can
// resolve to an exact ID or label match that exists only in a sibling
// fleet member's own store, not just this workspace's or the bridge's.
// The bool return is exactMatch — false means query matched nothing by name
// and the returned node is a full-text-search guess; see
// graph.ResolutionNote for the caller-facing warning built from it.
func resolveNode(ctx context.Context, store Store, idx *graph.AdjacencyIndex, query, targetService, targetType string) (*graph.Node, []graph.TargetCandidate, bool, error) {
	return graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, query, targetService, targetType)
}

// resolveAllServiceRoots detects the case an exact-label query resolved
// ambiguously across >1 service (e.g. a client-side call and its
// server-side handler sharing a name across an HTTP contract, like
// "RemoveConfig" existing in both maple-agent and maple-manager) and, only when
// the caller did NOT pin target_service, resolves one root per distinct
// service so impact.BuildMulti can union their blast radii into a single
// response.
//
// Previously an agent had to make one impact call per service — each paying
// the full MCP round-trip and JSON envelope — to reconstruct what is really
// one blast radius spanning a contract boundary. Pinning target_service
// still returns exactly the single-service answer it always did: this only
// changes the unqualified-query default, so no existing capability is lost.
// Within-service ambiguity (candidates all in one service — a genuinely
// different symbol, not a shared contract name) is left untouched; only
// cross-service duplication is auto-merged.
func resolveAllServiceRoots(ctx context.Context, store Store, idx *graph.AdjacencyIndex, query, targetType, targetService string, root *graph.Node, candidates []graph.TargetCandidate) []*graph.Node {
	if targetService != "" || len(candidates) <= 1 {
		return []*graph.Node{root}
	}
	seenService := map[string]bool{root.Service: true}
	roots := []*graph.Node{root}
	for _, c := range candidates {
		if seenService[c.Service] {
			continue
		}
		seenService[c.Service] = true
		r, _, _, err := resolveNode(ctx, store, idx, query, c.Service, targetType)
		if err == nil && r != nil {
			roots = append(roots, r)
		}
	}
	return roots
}

// ─── search ──────────────────────────────────────────────────────────────────

type searchInput struct {
	Query   string `json:"query" jsonschema:"search query (matches node labels and file paths)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max results (default 20)"`
	Kind    string `json:"kind,omitempty" jsonschema:"restrict results: 'file' for file search, or a node type (function, method, variable, http_handler, ...)"`
	Service string `json:"service,omitempty" jsonschema:"search scope: empty (default) = the current workspace only; '*' = federate across the whole fleet; a fleet member name = just that member"`
}

type searchOutput struct {
	Nodes []*graph.Node       `json:"nodes,omitempty"`
	Files []graph.FileSummary `json:"files,omitempty"`
}

// runSearch is the search tool's retrieval step. Scope is decided by
// semantic.ScopedSearch (shared with the web handler and CLI): service ==
// "" is the current workspace only, service == "*" federates across the
// fleet, and a member name narrows to that one member.
func (s *Server) runSearch(ctx context.Context, searcher *semantic.Searcher, query, service string, limit int) (semantic.Response, error) {
	return semantic.ScopedSearch(ctx, searcher, s.fleetSearchersSnapshot(), query, service, limit)
}

func (s *Server) search(ctx context.Context, req *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
	store, idx, searcher := s.snapshot()
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	if in.Kind == "file" {
		return jsonResult(searchOutput{Files: graph.ListFiles(idx, in.Query, limit)})
	}

	// Use hybrid FTS+vector search when a Searcher is wired (S.2).
	// kind filtering is handled post-fusion for unfiltered queries;
	// explicit kind requests fall through to FTS SearchNodes for type precision.
	if searcher != nil && in.Kind == "" {
		resp, err := s.runSearch(ctx, searcher, in.Query, in.Service, limit)
		if err != nil {
			return nil, nil, err
		}
		// §3: nodes are the primary answer — never return zero of them when FTS
		// has matches. Backfill from the node-only index before shaping.
		if len(resp.Nodes) == 0 {
			if nodes, nerr := store.SearchNodes(ctx, in.Query, limit); nerr == nil {
				for _, n := range nodes {
					resp.Nodes = append(resp.Nodes, semantic.Hit{
						Entity:    semantic.Entity{ID: n.ID, Type: "node", NodeID: n.ID, File: n.File, Line: n.Line},
						Retrieval: "lexical",
					})
				}
			}
		}
		// §2/§3: cap the flow/doc flood so nodes stay visible, and inline a
		// source snippet per node so the first call shows code. Shared with the
		// CLI search command.
		semantic.ShapeSearchResponse(&resp, ".", semantic.SearchFlowCap, semantic.SearchDocCap, semantic.SearchSnippetLines)
		return jsonResult(resp)
	}

	// Fallback: FTS-only SearchNodes. Used when no Searcher is wired or
	// a specific node type (kind) is requested.
	fetchLimit := limit
	if in.Kind != "" {
		fetchLimit = limit * 10
	}
	nodes, err := store.SearchNodes(ctx, in.Query, fetchLimit)
	if err != nil {
		return nil, nil, err
	}
	if in.Kind != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if string(n.Type) == in.Kind {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
		// FTS-only path: re-sort by how directly each label answers the query
		// so a real match beats a lexical cousin ("do-build" vs "do-cancel").
		sort.SliceStable(nodes, func(i, j int) bool {
			return semantic.LabelRelevance(nodes[i].Label, in.Query) > semantic.LabelRelevance(nodes[j].Label, in.Query)
		})
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}
	}
	return jsonResult(searchOutput{Nodes: nodes})
}

// ─── context ─────────────────────────────────────────────────────────────────

type contextInput struct {
	Target          string   `json:"target,omitempty" jsonschema:"search query for the target node (use this or files)"`
	TargetService   string   `json:"target_service,omitempty" jsonschema:"restrict target resolution to this service (resolves cross-service ambiguity; use when target_candidates is non-empty)"`
	TargetType      string   `json:"target_type,omitempty" jsonschema:"restrict target resolution to this node type (function, component, ...)"`
	Files           []string `json:"files,omitempty" jsonschema:"file path(s): return ranked related files (graph neighborhood) instead of node context"`
	Service         string   `json:"service,omitempty" jsonschema:"with files: restrict seed file resolution to a service"`
	Limit           int      `json:"limit,omitempty" jsonschema:"with files: max related files returned (default 20, -1 = unlimited)"`
	Task            string   `json:"task,omitempty" jsonschema:"task type: impact (callers only), generate (callees only), debug or refactor (both; default debug)"`
	Depth           int      `json:"depth,omitempty" jsonschema:"max traversal depth (node mode default 5, files mode default 2, -1 = unlimited)"`
	MaxTokens       int      `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer (0 = default ~2000; pass a negative value for unlimited); over budget, per-node detail rolls up per file"`
	Summary         bool     `json:"summary,omitempty" jsonschema:"emit the file-grouped rollup instead of per-node detail"`
	SnippetLines    int      `json:"snippet_lines,omitempty" jsonschema:"inline N source lines per node in detail output (default 4; negative = off; the max_tokens budget still caps total size)"`
	MinVerification string   `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool     `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
	IncludeNoise    []string `json:"include_noise,omitempty" jsonschema:"noise classes to show: filter_chain, mixin, containment, render_tree, test_code, or all/none. Overrides the task-based default entirely (never merges with it). Default hides all five classes except when task=generate, which shows render_tree"`
}

func (s *Server) context(ctx context.Context, req *mcp.CallToolRequest, in contextInput) (*mcp.CallToolResult, any, error) {
	if (in.Target == "") == (len(in.Files) == 0) {
		return nil, nil, fmt.Errorf("provide exactly one of target or files")
	}
	store, idx, searcher := s.snapshot()
	_ = searcher

	// Files mode: rank the files related to the seed file(s).
	if len(in.Files) > 0 {
		limit := in.Limit
		switch {
		case limit < 0:
			limit = 0
		case limit == 0:
			limit = 20
		}
		result, err := pfcontext.BuildFiles(idx, in.Service, in.Files, effectiveDepth(in.Depth, 2), limit)
		if err != nil {
			return nil, nil, err
		}
		unresolved, err := store.ListUnresolvedRefs(ctx)
		if err != nil {
			return nil, nil, err
		}
		result.AttachUnresolved(unresolved)
		result.ApplyBudget(effectiveBudgetWithDefault(in.MaxTokens, defaultContextBudget))
		return jsonResult(result)
	}

	task := in.Task
	if task == "" {
		task = "debug"
	}
	if task != "impact" && task != "generate" && task != "debug" && task != "refactor" {
		return nil, nil, fmt.Errorf("unknown task type: %s (use: impact, generate, debug, refactor)", task)
	}
	depth := effectiveDepth(in.Depth, 5)
	include, err := graph.ResolveNoiseInclude(in.IncludeNoise, task)
	if err != nil {
		return nil, nil, err
	}

	root, candidates, exactMatch, err := resolveNode(ctx, store, idx, in.Target, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}

	result := pfcontext.Build(idx, root.ID, task, depth, in.VerboseSources, s.staleAfter, include)
	result.TargetCandidates = candidates
	result.Status = graph.AmbiguityStatus(candidates)
	result.ResolutionNote = graph.ResolutionNote(in.Target, exactMatch)
	result.Trust, _ = graph.LoadTrustStamp(ctx, store)
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	result.FinalizeEpistemic()
	if in.MinVerification != "" && in.MinVerification != "any" {
		result.Upstream = filterTraceNodes(result.Upstream, in.MinVerification)
		result.Downstream = filterTraceNodes(result.Downstream, in.MinVerification)
	}
	// §2: snippets default ON so the first context call shows code. 0 (unset) →
	// default; negative → off. The max_tokens budget still caps total size.
	result.InlineSnippets(".", defaultSnippetLines(in.SnippetLines))
	return jsonResult(result.ApplyBudget(effectiveBudgetWithDefault(in.MaxTokens, defaultContextBudget), in.Summary))
}

// defaultSnippetLines maps the snippet_lines input to an effective count:
// 0 (unset) → the default, negative → off (0), positive → as given. Lets
// snippets default on while preserving an explicit opt-out (IA §2).
func defaultSnippetLines(n int) int {
	switch {
	case n == 0:
		return semantic.SearchSnippetLines
	case n < 0:
		return 0
	default:
		return n
	}
}

// ─── impact ──────────────────────────────────────────────────────────────────

type impactInput struct {
	Target          string   `json:"target,omitempty" jsonschema:"search query for the target node (use this or file)"`
	TargetService   string   `json:"target_service,omitempty" jsonschema:"narrow to ONE service's blast radius. Leave unset even when target_candidates is non-empty: an unqualified query already unions the blast radius across every service where the symbol matches exactly (e.g. a shared HTTP-contract name on both the client and server side) in this single call — do not re-call once per candidate service, it is redundant. Only set this when you specifically want just one service's slice"`
	TargetType      string   `json:"target_type,omitempty" jsonschema:"restrict target resolution to this node type (function, component, ...)"`
	File            string   `json:"file,omitempty" jsonschema:"file path: report impact at file granularity instead of node granularity"`
	Direction       string   `json:"direction,omitempty" jsonschema:"forward, backward, or both (default backward). backward answers 'what breaks if I change this'; forward answers 'what does this reach' — what you need to read. Use both for a 'what else do I need to touch/change' question — backward alone will not surface things the target itself depends on"`
	Depth           int      `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
	Service         string   `json:"service,omitempty" jsonschema:"filter results to a specific service"`
	MaxTokens       int      `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer; defaults to a compact budget that rolls large blast radii up per file. Small results still return full per-node detail. A merged multi-service answer (query auto-merges when a name matches >1 service, e.g. a shared HTTP-contract symbol) honours your requested budget up to 15000 tokens even if you pass more or an unlimited negative value — one merged call at max_tokens 15000 is cheaper and more complete than splitting into one target_service call per candidate, and is the preferred way to call this. Only set target_service when you specifically want just one service's slice, or need more than 15000 tokens of detail for that one service"`
	Summary         bool     `json:"summary,omitempty" jsonschema:"force the file-grouped rollup instead of per-node detail, regardless of size"`
	SnippetLines    int      `json:"snippet_lines,omitempty" jsonschema:"inline N source lines per node in detail output (default 4; negative = off; the max_tokens budget still caps total size)"`
	MinVerification string   `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool     `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
	IncludeNoise    []string `json:"include_noise,omitempty" jsonschema:"noise classes to show: filter_chain, mixin, containment, render_tree, test_code, or all/none. impact has no task concept, so the default (unset) always hides all five classes"`
}

func (s *Server) impact(ctx context.Context, req *mcp.CallToolRequest, in impactInput) (*mcp.CallToolResult, any, error) {
	if (in.Target == "") == (in.File == "") {
		return nil, nil, fmt.Errorf("provide exactly one of target or file")
	}
	depth := effectiveDepth(in.Depth, 10)
	include, err := graph.ResolveNoiseInclude(in.IncludeNoise, "impact")
	if err != nil {
		return nil, nil, err
	}

	store, idx, searcher := s.snapshot()
	_ = searcher
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}

	if in.File != "" {
		out, err := impact.BuildFile(idx, in.File, impact.Options{
			Depth:     depth,
			Service:   in.Service,
			Direction: in.Direction,
			Policy:    graph.BlastRadiusPolicy(),
		})
		if err != nil {
			return nil, nil, err
		}
		out.AttachUnresolved(unresolved)
		out.ApplyBudget(effectiveBudget(in.MaxTokens))
		return jsonResult(out)
	}

	root, candidates, exactMatch, err := resolveNode(ctx, store, idx, in.Target, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}
	roots := resolveAllServiceRoots(ctx, store, idx, in.Target, in.TargetType, in.TargetService, root, candidates)
	opts := impact.Options{
		Depth:          depth,
		Service:        in.Service,
		Direction:      in.Direction,
		Policy:         graph.BlastRadiusPolicy(),
		VerboseSources: in.VerboseSources,
		StaleAfter:     s.staleAfter,
		Include:        include,
	}
	var out *impact.Result
	if len(roots) > 1 {
		out = impact.BuildMulti(idx, roots, opts)
	} else {
		out = impact.Build(idx, root, opts)
	}
	out.TargetCandidates = candidates
	out.Status = graph.AmbiguityStatus(candidates)
	out.ResolutionNote = graph.ResolutionNote(in.Target, exactMatch)
	out.Trust, _ = graph.LoadTrustStamp(ctx, store)
	out.AttachUnresolved(unresolved)
	out.FinalizeEpistemic()
	if in.MinVerification != "" && in.MinVerification != "any" {
		out.Callers = filterCallers(out.Callers, in.MinVerification)
		out.TotalCallers = len(out.Callers)
	}
	out.InlineSnippets(".", defaultSnippetLines(in.SnippetLines))
	budget := effectiveBudget(in.MaxTokens)
	if len(roots) > 1 && (budget == 0 || budget > multiRootBudgetCeiling) {
		// See multiRootBudgetCeiling: cap the merged path at a safe ceiling
		// rather than collapsing to the compact default, so a caller's
		// bounded request (e.g. 15000-20000) is still honoured up to that
		// ceiling instead of being silently downgraded to 2000.
		// --target-service still gets the caller's requested budget verbatim
		// on the single-root path above.
		budget = multiRootBudgetCeiling
	}
	return jsonResult(out.ApplyBudget(budget, in.Summary))
}

// ─── trace ───────────────────────────────────────────────────────────────────

type traceInput struct {
	Root            string `json:"root" jsonschema:"search query for the root node"`
	TargetService   string `json:"target_service,omitempty" jsonschema:"restrict root resolution to this service (resolves cross-service ambiguity; use when target_candidates is non-empty)"`
	TargetType      string `json:"target_type,omitempty" jsonschema:"restrict root resolution to this node type (function, component, ...)"`
	Direction       string `json:"direction,omitempty" jsonschema:"trace direction: forward, backward, or both (default forward)"`
	Depth           int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
	MinVerification string `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool   `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
	MaxTokens       int    `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer; defaults to a compact budget that trims chains then nodes to fit. direction=both with a deep depth on a busy hub node (e.g. a shared queue/exchange) can otherwise produce a result too large for your own tool-output limit to even return. Pass a negative value for unlimited detail"`
	Detail          bool   `json:"detail,omitempty" jsonschema:"return full per-hop metadata (types, node_meta, sources) instead of the default compact arrow-chain text (file:line label -[edge_type]-> file:line label -> ...); costs substantially more tokens, use only when you need struct shapes, provenance, or exact line-level edge metadata"`

	IncludeNoise  []string `json:"include_noise,omitempty" jsonschema:"noise classes to show: filter_chain, mixin, containment, render_tree, test_code, or all/none. Overrides the task-based default entirely (never merges with it). Default hides all five classes except when task=generate, which shows render_tree"`
	Task          string   `json:"task,omitempty" jsonschema:"task type used ONLY to pick a noise-visibility default when include_noise is unset: impact, generate, debug, refactor (default debug)"`
	ExploreChains int      `json:"explore_chains,omitempty" jsonschema:"how many candidate chains to enumerate before giving up (default 500 = 5x the 100-chain display cap). Raise this if a root gated by heavy filter_chain/mixin fan-out returns few or no visible chains even though a real behavioral chain exists further down the same subtree"`
}

func (s *Server) trace(ctx context.Context, req *mcp.CallToolRequest, in traceInput) (*mcp.CallToolResult, any, error) {
	direction := in.Direction
	if direction == "" {
		direction = "forward"
	}
	if direction != "forward" && direction != "backward" && direction != "both" {
		return nil, nil, fmt.Errorf("unknown direction: %s (use: forward, backward, both)", direction)
	}
	depth := effectiveDepth(in.Depth, 10)

	task := in.Task
	if task == "" {
		task = "debug"
	}
	if task != "impact" && task != "generate" && task != "debug" && task != "refactor" {
		return nil, nil, fmt.Errorf("unknown task type: %s (use: impact, generate, debug, refactor)", task)
	}
	include, err := graph.ResolveNoiseInclude(in.IncludeNoise, task)
	if err != nil {
		return nil, nil, err
	}

	store, idx, searcher := s.snapshot()
	_ = searcher
	root, candidates, exactMatch, err := resolveNode(ctx, store, idx, in.Root, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}

	result := trace.Run(idx, root.ID, direction, depth, in.VerboseSources, s.staleAfter, include, in.ExploreChains)
	if result == nil {
		return nil, nil, fmt.Errorf("root node %s not in graph", root.ID)
	}
	result.TargetCandidates = candidates
	result.Status = graph.AmbiguityStatus(candidates)
	result.ResolutionNote = graph.ResolutionNote(in.Root, exactMatch)
	result.Trust, _ = graph.LoadTrustStamp(ctx, store)
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	result.FinalizeEpistemic()
	if in.MinVerification != "" && in.MinVerification != "any" {
		result.Nodes = filterHops(result.Nodes, in.MinVerification)
		result.Chains = filterChains(result.Chains, in.MinVerification)
	}
	budgeted := result.ApplyBudget(effectiveBudget(in.MaxTokens))
	if in.Detail {
		return jsonResult(budgeted)
	}
	return jsonResult(budgeted.Compact())
}

// ─── min_verification filter helpers ─────────────────────────────────────────

// filterCallers removes impact callers whose edge VerificationState does not
// meet the threshold. The VerificationSummary on the parent Result is built
// from all edges before filtering, so filtered counts remain visible.
func filterCallers(callers []impact.Caller, minVerification string) []impact.Caller {
	out := callers[:0:len(callers)]
	for _, c := range callers {
		if minVerificationPasses(c.VerificationState, minVerification) {
			out = append(out, c)
		}
	}
	return out
}

// filterTraceNodes removes context TraceNodes whose edge VerificationState
// does not meet the threshold.
func filterTraceNodes(nodes []pfcontext.TraceNode, minVerification string) []pfcontext.TraceNode {
	out := nodes[:0:len(nodes)]
	for _, n := range nodes {
		if minVerificationPasses(n.VerificationState, minVerification) {
			out = append(out, n)
		}
	}
	return out
}

// filterHops removes trace flat-hops whose edge VerificationState does not
// meet the threshold.
func filterHops(hops []trace.Hop, minVerification string) []trace.Hop {
	out := hops[:0:len(hops)]
	for _, h := range hops {
		if minVerificationPasses(h.VerificationState, minVerification) {
			out = append(out, h)
		}
	}
	return out
}

// filterChains removes chains that contain any hop whose edge VerificationState
// does not meet the threshold. A chain is kept only when all of its hops pass,
// preserving chain integrity (a broken chain is less useful than an absent one).
func filterChains(chains []trace.Chain, minVerification string) []trace.Chain {
	out := chains[:0:len(chains)]
	for _, ch := range chains {
		keep := true
		for _, h := range ch.Hops {
			// The first hop in a chain has no incoming edge (EdgeType==""); skip it.
			if h.EdgeType == "" {
				continue
			}
			if !minVerificationPasses(h.VerificationState, minVerification) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, ch)
		}
	}
	return out
}
