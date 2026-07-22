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
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/trace"
)

// semanticsParagraph is embedded in context/impact/trace descriptions so
// agents understand verification_state without reading plan docs.
const semanticsParagraph = "Edges carry verification_state: `verified` edges are confirmed by runtime " +
	"or declared contracts — do not re-verify. `candidate` edges are static-only — " +
	"one cheap grep confirms them. `observed_only_gap` edges were seen at runtime " +
	"but missed by static analysis — treat as real. The verification_summary and " +
	"unresolved sections are always present; empty means clean, absent means error."

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
}

// Server wires the query layer behind MCP tool handlers. The store and index
// are swappable so a long-lived stdio session picks up reindexes.
type Server struct {
	mu         sync.RWMutex
	store      Store
	idx        *graph.AdjacencyIndex
	searcher   *semantic.Searcher // optional; nil → FTS-only fallback
	staleAfter time.Duration      // workspace evidence.stale_after (0 = no stale check)
}

// SetSearcher wires a hybrid Searcher. Call after New; safe to call while
// serving (protected by mu). When nil, search falls back to FTS-only SearchNodes.
func (s *Server) SetSearcher(sr *semantic.Searcher) {
	s.mu.Lock()
	s.searcher = sr
	s.mu.Unlock()
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

// New builds an MCP server exposing the polyflow query tools. The returned
// *Server handle supports Reload; the *mcp.Server is what runs the session.
// staleAfter propagates the workspace evidence.stale_after threshold (0 = no
// stale check — caller can pass the workspace default when loading config).
func New(store Store, idx *graph.AdjacencyIndex, version string, staleAfter time.Duration) (*mcp.Server, *Server) {
	s := &Server{store: store, idx: idx, staleAfter: staleAfter}

	srv := mcp.NewServer(&mcp.Implementation{Name: "polyflow", Version: version}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search the indexed code graph for nodes (functions, methods, variables, " +
			"HTTP handlers, …), flow chains, or doc chunks matching a query. " +
			"Query may be natural language; results include flows — a flows hit's entry " +
			"node is the starting point for trace. Use this to find the exact node " +
			"before calling context, impact, or trace.",
	}, s.search)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "context",
		Description: "Show the call context around a node: upstream callers, downstream callees, " +
			"and cross-service edges. Pass files instead of target to get the ranked files related " +
			"to those file(s) (graph neighborhood, direct references first) — answers 'where is " +
			"the code connected to X' without grep exploration. The unresolved section lists " +
			"references in the traversed files the indexer could not resolve — verify those " +
			"manually, edges may be missing. " +
			"Set max_tokens to cap output size (over budget, per-node detail rolls up per file), " +
			"summary to force the rollup, snippet_lines to inline source snippets per node. " +
			semanticsParagraph,
	}, s.context)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "impact",
		Description: "Show the blast radius of changing a node or a file: everything that " +
			"transitively depends on it, entry points, and affected services. Directly answers " +
			"'what is impacted if I change X'. The unresolved section lists references the " +
			"indexer could not resolve — verify those manually, the blast radius may be " +
			"under-reported where they appear. Output defaults to a compact budget: small blast " +
			"radii return full per-node detail, large ones auto-roll-up per file. Set max_tokens " +
			"to raise or lower that cap (negative = unlimited), summary to force the rollup, " +
			"snippet_lines to inline source snippets per node. " +
			semanticsParagraph,
	}, s.impact)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "trace",
		Description: "Trace multi-hop flows from a node as linear chains (A -> B -> C), " +
			"including cross-service hops. The unresolved section lists references the indexer " +
			"could not resolve — verify those manually, chains may be incomplete. " +
			semanticsParagraph,
	}, s.trace)

	return srv, s
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
const defaultImpactBudget = 2000

// effectiveBudget maps an MCP max_tokens input to an impact.ApplyBudget budget.
// 0 (unset) → the compact default; a negative value → 0 (unlimited, opt-in);
// any positive value is honoured as-is.
func effectiveBudget(maxTokens int) int {
	switch {
	case maxTokens == 0:
		return defaultImpactBudget
	case maxTokens < 0:
		return 0
	}
	return maxTokens
}

// resolveNode finds the best node match for a search query, mirroring the
// CLI's target resolution.
func resolveNode(ctx context.Context, store Store, query string) (*graph.Node, error) {
	nodes, err := store.SearchNodes(ctx, query, 5)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("node not found for query: %s", query)
	}
	return nodes[0], nil
}

// ─── search ──────────────────────────────────────────────────────────────────

type searchInput struct {
	Query string `json:"query" jsonschema:"search query (matches node labels and file paths)"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results (default 20)"`
	Kind  string `json:"kind,omitempty" jsonschema:"restrict results: 'file' for file search, or a node type (function, method, variable, http_handler, ...)"`
}

type searchOutput struct {
	Nodes []*graph.Node       `json:"nodes,omitempty"`
	Files []graph.FileSummary `json:"files,omitempty"`
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
		resp, err := searcher.Search(ctx, in.Query, limit)
		if err != nil {
			return nil, nil, err
		}
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
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}
	}
	return jsonResult(searchOutput{Nodes: nodes})
}

// ─── context ─────────────────────────────────────────────────────────────────

type contextInput struct {
	Target          string   `json:"target,omitempty" jsonschema:"search query for the target node (use this or files)"`
	Files           []string `json:"files,omitempty" jsonschema:"file path(s): return ranked related files (graph neighborhood) instead of node context"`
	Service         string   `json:"service,omitempty" jsonschema:"with files: restrict seed file resolution to a service"`
	Limit           int      `json:"limit,omitempty" jsonschema:"with files: max related files returned (default 20, -1 = unlimited)"`
	Task            string   `json:"task,omitempty" jsonschema:"task type: impact (callers only), generate (callees only), debug or refactor (both; default debug)"`
	Depth           int      `json:"depth,omitempty" jsonschema:"max traversal depth (node mode default 5, files mode default 2, -1 = unlimited)"`
	MaxTokens       int      `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer (0 = unlimited); over budget, per-node detail rolls up per file"`
	Summary         bool     `json:"summary,omitempty" jsonschema:"emit the file-grouped rollup instead of per-node detail"`
	SnippetLines    int      `json:"snippet_lines,omitempty" jsonschema:"inline N source lines per node in detail output (0 = off)"`
	MinVerification string   `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool     `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
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
		result.ApplyBudget(in.MaxTokens)
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

	root, err := resolveNode(ctx, store, in.Target)
	if err != nil {
		return nil, nil, err
	}

	result := pfcontext.Build(idx, root.ID, task, depth, in.VerboseSources, s.staleAfter)
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	if in.MinVerification != "" && in.MinVerification != "any" {
		result.Upstream = filterTraceNodes(result.Upstream, in.MinVerification)
		result.Downstream = filterTraceNodes(result.Downstream, in.MinVerification)
	}
	result.InlineSnippets(".", in.SnippetLines)
	return jsonResult(result.ApplyBudget(in.MaxTokens, in.Summary))
}

// ─── impact ──────────────────────────────────────────────────────────────────

type impactInput struct {
	Target          string `json:"target,omitempty" jsonschema:"search query for the target node (use this or file)"`
	File            string `json:"file,omitempty" jsonschema:"file path: report impact at file granularity instead of node granularity"`
	Direction       string `json:"direction,omitempty" jsonschema:"with file: forward, backward, or both (default backward)"`
	Depth           int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
	Service         string `json:"service,omitempty" jsonschema:"filter results to a specific service"`
	MaxTokens       int    `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer; defaults to a compact budget that rolls large blast radii up per file. Small results still return full per-node detail. Pass a negative value for unlimited detail"`
	Summary         bool   `json:"summary,omitempty" jsonschema:"force the file-grouped rollup instead of per-node detail, regardless of size"`
	SnippetLines    int    `json:"snippet_lines,omitempty" jsonschema:"inline N source lines per node in detail output (0 = off)"`
	MinVerification string `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool   `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
}

func (s *Server) impact(ctx context.Context, req *mcp.CallToolRequest, in impactInput) (*mcp.CallToolResult, any, error) {
	if (in.Target == "") == (in.File == "") {
		return nil, nil, fmt.Errorf("provide exactly one of target or file")
	}
	depth := effectiveDepth(in.Depth, 10)

	store, idx, searcher := s.snapshot()
	_ = searcher
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}

	if in.File != "" {
		direction := in.Direction
		if direction == "" {
			direction = "backward"
		}
		out, err := impact.BuildFile(idx, in.Service, in.File, direction, depth)
		if err != nil {
			return nil, nil, err
		}
		out.AttachUnresolved(unresolved)
		out.ApplyBudget(effectiveBudget(in.MaxTokens))
		return jsonResult(out)
	}

	root, err := resolveNode(ctx, store, in.Target)
	if err != nil {
		return nil, nil, err
	}
	out := impact.Build(idx, root, depth, in.Service, in.VerboseSources, s.staleAfter)
	out.AttachUnresolved(unresolved)
	if in.MinVerification != "" && in.MinVerification != "any" {
		out.Callers = filterCallers(out.Callers, in.MinVerification)
		out.TotalCallers = len(out.Callers)
	}
	out.InlineSnippets(".", in.SnippetLines)
	return jsonResult(out.ApplyBudget(effectiveBudget(in.MaxTokens), in.Summary))
}

// ─── trace ───────────────────────────────────────────────────────────────────

type traceInput struct {
	Root            string `json:"root" jsonschema:"search query for the root node"`
	Direction       string `json:"direction,omitempty" jsonschema:"trace direction: forward, backward, or both (default forward)"`
	Depth           int    `json:"depth,omitempty" jsonschema:"max traversal depth (default 10, -1 = unlimited)"`
	MinVerification string `json:"min_verification,omitempty" jsonschema:"filter edges by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	VerboseSources  bool   `json:"verbose_sources,omitempty" jsonschema:"return full SourceRef structs instead of compact provider:ref strings (increases token usage)"`
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

	store, idx, searcher := s.snapshot()
	_ = searcher
	root, err := resolveNode(ctx, store, in.Root)
	if err != nil {
		return nil, nil, err
	}

	result := trace.Run(idx, root.ID, direction, depth, in.VerboseSources, s.staleAfter)
	if result == nil {
		return nil, nil, fmt.Errorf("root node %s not in graph", root.ID)
	}
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.AttachUnresolved(unresolved)
	if in.MinVerification != "" && in.MinVerification != "any" {
		result.Nodes = filterHops(result.Nodes, in.MinVerification)
		result.Chains = filterChains(result.Chains, in.MinVerification)
	}
	return jsonResult(result)
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
