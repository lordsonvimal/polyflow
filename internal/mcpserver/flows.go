package mcpserver

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/lordsonvimal/polyflow/internal/yield"
)

// defaultFlowMaxHops caps traversal depth per flow when max_hops is unset,
// mirroring impact/trace's depth defaults.
const defaultFlowMaxHops = 8

// maxFlowPaths caps the number of enumerated flow paths (mirrors trace.MaxChains)
// so a densely connected root doesn't produce a combinatorial explosion.
const maxFlowPaths = 50

// hubFanoutThreshold: when a node's out-edges of one type into one service
// exceed this count, they collapse into a single rollup hop instead of one
// hop per branch — the "hub node with N fan-out rolls up per service" budget
// path the phase spec calls for (impact's per-file FileRollup is the same
// idea applied per-file instead of per-service/edge-type).
const hubFanoutThreshold = 8

// flowsInput mirrors the pinned FlowsInput surface from the systemic-gaps
// plan (X.4), extended with target_type for symmetry with context/impact/trace.
type flowsInput struct {
	Target          string `json:"target,omitempty" jsonschema:"node id | 'GET /path' | file path | natural-language feature description"`
	TargetService   string `json:"target_service,omitempty" jsonschema:"restrict target resolution to this service (resolves cross-service ambiguity; use when target_candidates is non-empty)"`
	TargetType      string `json:"target_type,omitempty" jsonschema:"restrict target resolution to this node type (http_handler, function, component, ...)"`
	Direction       string `json:"direction,omitempty" jsonschema:"downstream (default), upstream, or both"`
	MaxHops         int    `json:"max_hops,omitempty" jsonschema:"max hops per flow (default 8, -1 = unlimited)"`
	MinVerification string `json:"min_verification,omitempty" jsonschema:"filter flows by minimum verification level: verified, declared, observed, or any (default any — recall over precision)"`
	MaxTokens       int    `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the answer (0 = compact default, negative = unlimited); over budget, flows collapse to the coverage block plus a sample"`
	Detail          bool   `json:"detail,omitempty" jsonschema:"return full per-hop node/edge objects instead of the default compact arrow-chain text (file:line label -[edge_type]-> file:line label -> ...); costs substantially more tokens, use only when you need per-hop verification state or exact node IDs"`
}

// flowHop is one edge traversal within a flow path. From/To are node IDs.
type flowHop struct {
	From         string         `json:"from"`
	To           string         `json:"to"`
	Service      string         `json:"service"`
	Edge         graph.EdgeType `json:"edge"`
	Verification string         `json:"verification_state"`
}

// flowsOutput is the pinned FlowsOutput surface (Flows/Coverage/Unresolved),
// additively carrying TargetCandidates/Truncated/Budget for parity with the
// other query tools' disambiguation and budgeting contracts.
type flowsOutput struct {
	Flows            [][]flowHop             `json:"flows"`
	Coverage         yield.CoverageBlock     `json:"coverage"`
	Unresolved       []graph.UnresolvedRef   `json:"unresolved"`
	Truncated        bool                    `json:"truncated,omitempty"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	// ResolutionNote is set when Target came from a full-text-search guess
	// rather than a confirmed exact-label match — see graph.ResolutionNote.
	ResolutionNote string       `json:"resolution_note,omitempty"`
	Budget         *budget.Info `json:"budget,omitempty"`
}

// flowsSummary is the token-budgeted rollup emitted when the full flow set
// exceeds max_tokens: the coverage block (never trimmed — it's the whole
// point) plus a bounded sample of flows instead of the full set.
type flowsSummary struct {
	Summary          bool                    `json:"summary"`
	FlowCount        int                     `json:"flow_count"`
	SampleFlows      [][]flowHop             `json:"sample_flows"`
	Coverage         yield.CoverageBlock     `json:"coverage"`
	Unresolved       []graph.UnresolvedRef   `json:"unresolved"`
	Truncated        bool                    `json:"truncated,omitempty"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	ResolutionNote   string                  `json:"resolution_note,omitempty"`
	Budget           *budget.Info            `json:"budget,omitempty"`
}

func (s *Server) flows(ctx context.Context, req *mcp.CallToolRequest, in flowsInput) (*mcp.CallToolResult, any, error) {
	direction := in.Direction
	switch direction {
	case "":
		direction = "downstream"
	case "downstream", "upstream", "both":
	default:
		return nil, nil, fmt.Errorf("unknown direction: %s (use: downstream, upstream, both)", direction)
	}
	maxHops := effectiveDepth(in.MaxHops, defaultFlowMaxHops)

	store, idx, searcher := s.snapshot()
	root, candidates, exactMatch, err := resolveFlowTarget(ctx, store, idx, searcher, in.Target, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}

	var walkDirs []string
	switch direction {
	case "upstream":
		walkDirs = []string{"in"}
	case "both":
		walkDirs = []string{"out", "in"}
	default:
		walkDirs = []string{"out"}
	}

	var flowPaths [][]flowHop
	var edges []graph.Edge
	truncated := false
	for _, d := range walkDirs {
		fs, es, trunc := walkFlows(idx, root.ID, d, maxHops)
		flowPaths = append(flowPaths, fs...)
		edges = append(edges, es...)
		truncated = truncated || trunc
	}

	if in.MinVerification != "" && in.MinVerification != "any" {
		flowPaths = filterFlows(flowPaths, in.MinVerification)
	}

	unresolvedAll, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	files := map[string]bool{root.File: true}
	for _, fp := range flowPaths {
		for _, h := range fp {
			if n := idx.Nodes[h.From]; n != nil {
				files[n.File] = true
			}
			if n := idx.Nodes[h.To]; n != nil {
				files[n.File] = true
			}
		}
	}
	unresolved := graph.UnresolvedInFiles(graph.DropExternalFrameworkRefs(unresolvedAll, idx), files)

	out := &flowsOutput{
		Flows:            flowPaths,
		Coverage:         yield.ComputeCoverage(edges, unresolved),
		Unresolved:       unresolved,
		Truncated:        truncated,
		TargetCandidates: candidates,
		ResolutionNote:   graph.ResolutionNote(in.Target, exactMatch),
	}
	budgeted := applyFlowsBudget(out, effectiveBudget(in.MaxTokens))
	if in.Detail {
		return jsonResult(budgeted)
	}
	return jsonResult(compactFlows(budgeted, idx))
}

// compactFlowsOutput is flowsOutput's default wire shape: each flow path
// rendered as one arrow-chain string (file:line label -[edge_type]-> file:line
// label -> ...) instead of a []flowHop of raw node IDs per path. Mirrors
// trace.CompactResult for the same measured reason: a bench trial's raw flows
// response repeated long absolute-path node IDs on both ends of every hop,
// much of it identical to the adjacent hop's other end.
type compactFlowsOutput struct {
	Flows            []string                `json:"flows"`
	Coverage         yield.CoverageBlock     `json:"coverage"`
	Unresolved       []graph.UnresolvedRef   `json:"unresolved"`
	Truncated        bool                    `json:"truncated,omitempty"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	ResolutionNote   string                  `json:"resolution_note,omitempty"`
	Budget           *budget.Info            `json:"budget,omitempty"`
}

// compactFlowsSummary is flowsSummary's compact twin, used when the full flow
// set exceeded max_tokens and applyFlowsBudget already collapsed to a sample.
type compactFlowsSummary struct {
	Summary          bool                    `json:"summary"`
	FlowCount        int                     `json:"flow_count"`
	SampleFlows      []string                `json:"sample_flows"`
	Coverage         yield.CoverageBlock     `json:"coverage"`
	Unresolved       []graph.UnresolvedRef   `json:"unresolved"`
	Truncated        bool                    `json:"truncated,omitempty"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	ResolutionNote   string                  `json:"resolution_note,omitempty"`
	Budget           *budget.Info            `json:"budget,omitempty"`
}

// compactFlows converts applyFlowsBudget's result (either a full flowsOutput
// or a flowsSummary rollup) to its compact twin.
func compactFlows(res any, idx *graph.AdjacencyIndex) any {
	switch v := res.(type) {
	case *flowsOutput:
		return &compactFlowsOutput{
			Flows:            renderFlowChains(idx, v.Flows),
			Coverage:         v.Coverage,
			Unresolved:       v.Unresolved,
			Truncated:        v.Truncated,
			TargetCandidates: v.TargetCandidates,
			ResolutionNote:   v.ResolutionNote,
			Budget:           v.Budget,
		}
	case *flowsSummary:
		return &compactFlowsSummary{
			Summary:          v.Summary,
			FlowCount:        v.FlowCount,
			SampleFlows:      renderFlowChains(idx, v.SampleFlows),
			Coverage:         v.Coverage,
			Unresolved:       v.Unresolved,
			Truncated:        v.Truncated,
			TargetCandidates: v.TargetCandidates,
			ResolutionNote:   v.ResolutionNote,
			Budget:           v.Budget,
		}
	default:
		return res
	}
}

func renderFlowChains(idx *graph.AdjacencyIndex, flows [][]flowHop) []string {
	out := make([]string, len(flows))
	for i, fp := range flows {
		out[i] = renderFlowChain(idx, fp)
	}
	return out
}

// renderFlowChain renders one flow path as an arrow-chain string, matching
// trace.renderChain's format so the two tools read the same way.
func renderFlowChain(idx *graph.AdjacencyIndex, hops []flowHop) string {
	if len(hops) == 0 {
		return ""
	}
	var b strings.Builder
	first := idx.Nodes[hops[0].From]
	prevService := hops[0].Service
	if first != nil {
		prevService = first.Service
		fmt.Fprintf(&b, "(%s) %s", first.Service, flowNodeLocLabel(first, hops[0].From))
	} else {
		b.WriteString(hops[0].From)
	}
	for _, h := range hops {
		fmt.Fprintf(&b, " -[%s]-> ", h.Edge)
		to := idx.Nodes[h.To]
		if to == nil {
			b.WriteString(h.To)
			continue
		}
		if to.Service != prevService {
			fmt.Fprintf(&b, "‖%s‖ ", to.Service)
			prevService = to.Service
		}
		b.WriteString(flowNodeLocLabel(to, h.To))
	}
	return b.String()
}

// flowNodeLocLabel renders "file:line label" for a node, falling back to the
// raw node ID when the node carries no label (synthetic/unresolved targets).
func flowNodeLocLabel(n *graph.Node, id string) string {
	label := n.Label
	if label == "" {
		label = id
	}
	if n.File == "" {
		return label
	}
	if n.Line > 0 {
		return fmt.Sprintf("%s:%d %s", n.File, n.Line, label)
	}
	return fmt.Sprintf("%s %s", n.File, label)
}

// applyFlowsBudget mirrors impact.Result.ApplyBudget's detail-or-rollup
// decision: full Flows when it fits maxTokens, otherwise a bounded sample
// with the (never-trimmed) coverage block carrying the authoritative tally.
func applyFlowsBudget(out *flowsOutput, maxTokens int) any {
	if maxTokens <= 0 {
		return out
	}
	if est := budget.Estimate(out); est <= maxTokens {
		out.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: est, Level: budget.LevelDetail}
		return out
	}

	sample := out.Flows
	s := &flowsSummary{
		Summary:          true,
		FlowCount:        len(out.Flows),
		Coverage:         out.Coverage,
		Unresolved:       out.Unresolved,
		Truncated:        out.Truncated,
		TargetCandidates: out.TargetCandidates,
		ResolutionNote:   out.ResolutionNote,
		Budget:           &budget.Info{MaxTokens: maxTokens, Level: budget.LevelSummary},
	}
	s.Budget.AppendNote("full flow set exceeds the token budget; showing a sample plus the coverage tally")
	// The prefix cut runs against a shrunk budget, reserving flowBackfillReserve
	// for the backfill pass below — same shape as impact/summary.go's file
	// backfill and trace.ApplyBudget's node backfill. Without the reservation,
	// TrimToFit's binary search leaves only incidental slack, starving
	// backfill down to whatever near-empty flow happens to be cheapest
	// instead of a real short flow just past the cut.
	cutBudget := int(float64(maxTokens) * (1 - flowBackfillReserve))
	keep := budget.TrimToFit(len(sample), cutBudget, func(n int) int {
		s.SampleFlows = sample[:n]
		return budget.Estimate(s)
	})
	s.SampleFlows = sample[:keep]
	used := budget.Estimate(s)

	admitted := budget.Backfill(len(sample), keep, maxTokens, used, func(i int) int {
		return budget.Estimate(sample[i])
	})
	for _, i := range admitted {
		s.SampleFlows = append(s.SampleFlows, sample[i])
	}

	if omitted := len(sample) - len(s.SampleFlows); omitted > 0 {
		note := fmt.Sprintf("%d more flow(s) omitted to fit the budget", omitted)
		if len(admitted) > 0 {
			note = fmt.Sprintf("%s (%d cheap flow(s) admitted out of order to use leftover budget)", note, len(admitted))
		}
		s.Budget.AppendNote(note)
	}
	s.Budget.EstimatedTokens = budget.Estimate(s)
	return s
}

// flowBackfillReserve is the share of a token budget set aside so the
// backfill pass in applyFlowsBudget has room to admit a cheap flow the
// prefix cut skipped over. Mirrors impact/summary.go's fileBackfillReserve.
const flowBackfillReserve = 0.10

// filterFlows drops flows containing any hop whose VerificationState does not
// meet the threshold — a flow is kept only when every hop passes, matching
// trace's filterChains "broken chain is less useful than an absent one" rule.
func filterFlows(flows [][]flowHop, minVerification string) [][]flowHop {
	out := flows[:0:len(flows)]
	for _, fp := range flows {
		keep := true
		for _, h := range fp {
			if !minVerificationPasses(h.Verification, minVerification) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, fp)
		}
	}
	return out
}

// flowNeighborGroup buckets a node's outgoing (or incoming, for upstream
// traversal) flow edges by (edge type, neighbor service) — the unit that
// either expands into individual hops or rolls up into one hub hop.
type flowNeighborGroup struct {
	edgeType graph.EdgeType
	service  string
	edges    []*graph.Edge
}

// groupFlowNeighbors returns nodeID's flow-relevant edges (yield.IsFlowEdge)
// in the given direction, grouped by (type, neighbor service) and sorted
// deterministically: groups by (type, service), edges within a group by
// neighbor ID.
func groupFlowNeighbors(idx *graph.AdjacencyIndex, nodeID, direction string) []flowNeighborGroup {
	var edges []*graph.Edge
	if direction == "in" {
		edges = idx.InEdges[nodeID]
	} else {
		edges = idx.OutEdges[nodeID]
	}

	type key struct{ edgeType, service string }
	groups := map[key]*flowNeighborGroup{}
	var order []key
	for _, e := range edges {
		if !yield.IsFlowEdge(e.Type) {
			continue
		}
		neighborID := e.To
		if direction == "in" {
			neighborID = e.From
		}
		n := idx.Nodes[neighborID]
		if n == nil {
			continue
		}
		k := key{string(e.Type), n.Service}
		g, ok := groups[k]
		if !ok {
			g = &flowNeighborGroup{edgeType: e.Type, service: n.Service}
			groups[k] = g
			order = append(order, k)
		}
		g.edges = append(g.edges, e)
	}

	sort.Slice(order, func(i, j int) bool {
		if order[i].edgeType != order[j].edgeType {
			return order[i].edgeType < order[j].edgeType
		}
		return order[i].service < order[j].service
	})

	out := make([]flowNeighborGroup, 0, len(order))
	for _, k := range order {
		g := *groups[k]
		sort.Slice(g.edges, func(i, j int) bool {
			ni, nj := g.edges[i].To, g.edges[j].To
			if direction == "in" {
				ni, nj = g.edges[i].From, g.edges[j].From
			}
			return ni < nj
		})
		out = append(out, g)
	}
	return out
}

// walkFlows DFS-enumerates simple flow paths from rootID in the given
// direction ("out" or "in"), following only yield.IsFlowEdge edges, up to
// maxDepth hops (<=0 = unlimited) and maxFlowPaths total paths. A group whose
// edge count exceeds hubFanoutThreshold collapses into one rollup hop
// (bug-class #12: the excess is still accounted for, in edges, not silently
// dropped) instead of expanding every branch.
func walkFlows(idx *graph.AdjacencyIndex, rootID, direction string, maxDepth int) (flows [][]flowHop, edges []graph.Edge, truncated bool) {
	onPath := map[string]bool{rootID: true}
	var path []flowHop

	// emit snapshots the current DFS path into a flow. Upstream ("in") walks
	// build the path root-outward (nearest-to-root hop first); reverse it so
	// every flow reads source → destination in real flow order, matching
	// trace's buildChain convention for backward chains.
	emit := func() {
		if len(path) == 0 {
			return
		}
		cp := make([]flowHop, len(path))
		copy(cp, path)
		if direction == "in" {
			for i, j := 0, len(cp)-1; i < j; i, j = i+1, j-1 {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
		flows = append(flows, cp)
	}

	var walk func(nodeID string, depth int)
	walk = func(nodeID string, depth int) {
		if len(flows) >= maxFlowPaths {
			truncated = true
			return
		}
		if maxDepth > 0 && depth >= maxDepth {
			emit()
			if len(path) > 0 {
				truncated = true
			}
			return
		}

		groups := groupFlowNeighbors(idx, nodeID, direction)
		if len(groups) == 0 {
			emit()
			return
		}

		for _, g := range groups {
			if len(flows) >= maxFlowPaths {
				truncated = true
				return
			}

			if len(g.edges) > hubFanoutThreshold {
				for _, e := range g.edges {
					edges = append(edges, *e)
				}
				path = append(path, flowHop{
					From:         nodeID,
					To:           fmt.Sprintf("(%d more %s targets in %s)", len(g.edges), g.edgeType, g.service),
					Service:      g.service,
					Edge:         g.edgeType,
					Verification: "rollup",
				})
				emit()
				path = path[:len(path)-1]
				continue
			}

			for _, e := range g.edges {
				neighborID := e.To
				if direction == "in" {
					neighborID = e.From
				}
				if onPath[neighborID] {
					continue // cycle guard
				}
				edges = append(edges, *e)
				path = append(path, flowHop{From: e.From, To: e.To, Service: g.service, Edge: e.Type, Verification: e.VerificationState})
				onPath[neighborID] = true
				walk(neighborID, depth+1)
				onPath[neighborID] = false
				path = path[:len(path)-1]
				if len(flows) >= maxFlowPaths {
					truncated = true
					return
				}
			}
		}
	}

	walk(rootID, 0)
	return flows, edges, truncated
}

// httpMethodRe matches a "METHOD /path" style target, e.g. "GET /api/x" —
// http_handler node labels are stamped in exactly this shape.
var httpMethodRe = regexp.MustCompile(`(?i)^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+\S`)

// entrypointNodeTypes lists the node types entrypoints/flows treat as flow
// starting points: routes, brokers subscribers, background workers, gRPC and
// GraphQL server handlers. There is no cobra/CLI-command or scheduled-task
// NodeType in the graph yet (verified against internal/graph/model.go) — the
// contract engine does not mint one, so CLI commands and cron schedules are
// out of scope for this catalog until a parser phase adds that node class.
var entrypointNodeTypes = map[graph.NodeType]bool{
	graph.NodeTypeHTTPHandler:     true,
	graph.NodeTypeSubscriber:      true,
	graph.NodeTypeWorker:          true,
	graph.NodeTypeGRPCHandler:     true,
	graph.NodeTypeGraphQLResolver: true,
}

// resolveFlowTarget resolves a flows/resolve "target" query into a starting
// node, in order: exact node ID → "METHOD /path" HTTP route (resolved
// against http_handler labels) → file path (first entrypoint declared in
// that file, by line) → natural-language search (hybrid Searcher when wired)
// → the same FTS/ResolveTarget fallback context/impact/trace use, so
// target_candidates disambiguation is identical across tools.
//
// The bool return is exactMatch: true for every branch above except the
// final ResolveTarget fallback, where it passes through that call's own
// exactMatch — the only branch capable of the "matched nothing, silently
// substituted a full-text-search guess" failure graph.ResolutionNote warns
// about. The other branches (literal ID, file path, semantic search hit) are
// each an intentional, confident resolution strategy in their own right, not
// a last-resort guess.
func resolveFlowTarget(ctx context.Context, store Store, idx *graph.AdjacencyIndex, searcher *semantic.Searcher, query, targetService, targetType string) (*graph.Node, []graph.TargetCandidate, bool, error) {
	if n, ok := idx.Nodes[query]; ok {
		return n, []graph.TargetCandidate{}, true, nil
	}

	if httpMethodRe.MatchString(query) {
		tt := targetType
		if tt == "" {
			tt = string(graph.NodeTypeHTTPHandler)
		}
		return graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, query, targetService, tt)
	}

	if looksLikeFilePath(query) {
		if eps := entrypointsInFile(idx, query, targetService); len(eps) > 0 {
			return eps[0], []graph.TargetCandidate{}, true, nil
		}
	}

	if searcher != nil {
		resp, err := searcher.Search(ctx, query, 5)
		if err == nil {
			hits := resp.Nodes
			if len(hits) == 0 {
				hits = resp.Flows
			}
			if len(hits) > 0 {
				if n := idx.Nodes[hits[0].Entity.NodeID]; n != nil {
					return n, []graph.TargetCandidate{}, true, nil
				}
				if n := idx.Nodes[hits[0].Entity.ID]; n != nil {
					return n, []graph.TargetCandidate{}, true, nil
				}
			}
		}
	}

	return graph.ResolveTarget(ctx, graph.FleetSearcher{Store: store, Idx: idx}, query, targetService, targetType)
}

func looksLikeFilePath(q string) bool {
	return strings.Contains(q, "/") && strings.Contains(q, ".") && !strings.Contains(q, " ")
}

// entrypointsInFile returns the entrypoint nodes declared in file, optionally
// restricted to service, sorted by line then ID for determinism.
func entrypointsInFile(idx *graph.AdjacencyIndex, file, service string) []*graph.Node {
	var out []*graph.Node
	for _, n := range idx.Nodes {
		if n.File != file || !entrypointNodeTypes[n.Type] {
			continue
		}
		if service != "" && n.Service != service {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}
