package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/budget"
	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// investigate composes resolveNode -> pfcontext.Build -> walkFlows ->
// budget.Snippet into one call (Tier IA §1) so an agent gets the resolved
// node, its source, its neighbourhood, and the flows it sits on without
// sequencing resolve/context/trace/read itself.
const (
	investigateDefaultBudget = 1500 // approx tokens, unset max_tokens
	investigateSnippetLines  = 6    // root snippet default
	investigateNeighborLines = 2    // callers/callees/flow-chain snippet lines
	investigateContextDepth  = 3
	investigateFlowMaxDepth  = 6
	investigateMaxFlows      = 5
	investigateMaxCandidates = 3
)

type investigateInput struct {
	Query         string `json:"query" jsonschema:"natural-language symptom or feature to investigate, e.g. 'promoting a black piece renders white in study/practice'"`
	TargetService string `json:"target_service,omitempty" jsonschema:"restrict resolution to this service (use when candidates are ambiguous)"`
	TargetType    string `json:"target_type,omitempty" jsonschema:"restrict resolution to this node type"`
	MaxTokens     int    `json:"max_tokens,omitempty" jsonschema:"approximate token budget for the whole answer (0 = default ~1500)"`
	SnippetLines  int    `json:"snippet_lines,omitempty" jsonschema:"source lines inlined for the resolved node (default 6; negative = off)"`
}

// investigateNode is one node in the resolved neighbourhood or a flow chain.
type investigateNode struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet,omitempty"`
}

// investigateFlow is one linear chain through Root, State summarizing the
// weakest hop's verification (rollup < candidate < observed_only_gap < verified).
type investigateFlow struct {
	Chain []investigateNode `json:"chain"`
	State string            `json:"state"`
}

type investigateOutput struct {
	Root       *graph.Node             `json:"root"`
	Snippet    string                  `json:"snippet,omitempty"`
	Candidates []resolveCandidate      `json:"candidates,omitempty"`
	Ambiguous  []graph.TargetCandidate `json:"target_candidates,omitempty"`
	Callers    []investigateNode       `json:"callers"`
	Callees    []investigateNode       `json:"callees"`
	Flows      []investigateFlow       `json:"flows,omitempty"`
	Unresolved []graph.UnresolvedRef   `json:"coverage_unresolved"`

	// VerificationSummary and Trust mirror the equivalent context/impact/trace
	// sections (T.0) — sourced from the same context.Build traversal this
	// call already runs internally, not a second computation.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	Trust               graph.TrustStamp          `json:"trust"`

	// Epistemic is the single trust verdict derived from Unresolved,
	// VerificationSummary and Trust (EE.0). Always present; survives any
	// token budget.
	Epistemic graph.Epistemic `json:"epistemic"`

	Note   string       `json:"note,omitempty"`
	Budget *budget.Info `json:"budget,omitempty"`
}

func (s *Server) investigate(ctx context.Context, req *mcp.CallToolRequest, in investigateInput) (*mcp.CallToolResult, any, error) {
	if in.Query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	store, idx, searcher := s.snapshot()

	root, targetCandidates, exactMatch, err := resolveNode(ctx, store, in.Query, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}

	lines := in.SnippetLines
	switch {
	case lines == 0:
		lines = investigateSnippetLines
	case lines < 0:
		lines = 0
	}
	snippet := budget.Snippet(".", root.File, root.Line, lines)

	ctxRes := pfcontext.Build(idx, root.ID, "debug", investigateContextDepth, false, s.staleAfter, graph.DefaultNoiseInclude("debug"))
	ctxRes.Trust, _ = graph.LoadTrustStamp(ctx, store)
	unresolvedAll, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctxRes.AttachUnresolved(unresolvedAll)
	ctxRes.FinalizeEpistemic()
	ctxRes.InlineSnippets(".", investigateNeighborLines)

	flowPaths, _, _ := walkFlows(idx, root.ID, "out", investigateFlowMaxDepth)

	out := &investigateOutput{
		Root:                root,
		Snippet:             snippet,
		Ambiguous:           targetCandidates,
		Callers:             toInvestigateNodes(ctxRes.Upstream),
		Callees:             toInvestigateNodes(ctxRes.Downstream),
		Flows:               collapseFlows(idx, flowPaths, investigateMaxFlows),
		Unresolved:          ctxRes.Unresolved,
		VerificationSummary: ctxRes.VerificationSummary,
		Trust:               ctxRes.Trust,
		Epistemic:           ctxRes.Epistemic,
	}
	switch {
	case !exactMatch:
		out.Note = graph.ResolutionNote(in.Query, exactMatch)
	case len(targetCandidates) > 0:
		out.Note = "ambiguous match: re-call with target_service/target_type to pin the intended node (see target_candidates)"
	}
	if searcher != nil {
		resp, serr := searcher.Search(ctx, in.Query, investigateMaxCandidates+1)
		if serr == nil {
			for _, h := range resp.Nodes {
				if len(out.Candidates) >= investigateMaxCandidates {
					break
				}
				n := idx.Nodes[h.Entity.NodeID]
				if n == nil {
					n = idx.Nodes[h.Entity.ID]
				}
				if n == nil || n.ID == root.ID {
					continue
				}
				out.Candidates = append(out.Candidates, resolveCandidate{
					ID: n.ID, Type: string(n.Type), Label: n.Label, Service: n.Service,
					File: n.File, Line: n.Line, Score: h.Score, Retrieval: h.Retrieval,
					Snippet: budget.Snippet(".", n.File, n.Line, resolveSnippetLines),
				})
			}
		}
	}

	return jsonResult(applyInvestigateBudget(out, effectiveInvestigateBudget(in.MaxTokens)))
}

// toInvestigateNodes maps a context traversal result to the compact
// investigateNode shape.
func toInvestigateNodes(nodes []pfcontext.TraceNode) []investigateNode {
	out := make([]investigateNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, investigateNode{
			ID: n.ID, Label: n.Label, Type: n.Type, File: n.File, Line: n.Line, Snippet: n.Snippet,
		})
	}
	return out
}

// collapseFlows maps walkFlows chains to investigateFlow, capped at maxFlows,
// each hop resolved to its full node (label/file/line/snippet) so the chain
// is self-describing without a further lookup.
func collapseFlows(idx *graph.AdjacencyIndex, paths [][]flowHop, maxFlows int) []investigateFlow {
	if len(paths) > maxFlows {
		paths = paths[:maxFlows]
	}
	out := make([]investigateFlow, 0, len(paths))
	for _, hops := range paths {
		if len(hops) == 0 {
			continue
		}
		chain := make([]investigateNode, 0, len(hops)+1)
		chain = append(chain, investigateNodeFromID(idx, hops[0].From))
		worst := verificationRank(hops[0].Verification)
		for _, h := range hops {
			chain = append(chain, investigateNodeFromID(idx, h.To))
			if r := verificationRank(h.Verification); r < worst {
				worst = r
			}
		}
		out = append(out, investigateFlow{Chain: chain, State: verificationRankLabel(worst)})
	}
	return out
}

func investigateNodeFromID(idx *graph.AdjacencyIndex, id string) investigateNode {
	n := idx.Nodes[id]
	if n == nil {
		return investigateNode{ID: id}
	}
	return investigateNode{
		ID: n.ID, Label: n.Label, Type: string(n.Type), File: n.File, Line: n.Line,
		Snippet: budget.Snippet(".", n.File, n.Line, investigateNeighborLines),
	}
}

// verificationRank orders hop states from least to most trustworthy so a
// flow's overall State reflects its weakest hop.
func verificationRank(state string) int {
	switch state {
	case graph.StateVerified:
		return 3
	case graph.StateObservedOnlyGap:
		return 2
	case "rollup":
		return 0
	default: // candidate, "" (pre-fusion)
		return 1
	}
}

func verificationRankLabel(rank int) string {
	switch rank {
	case 3:
		return graph.StateVerified
	case 2:
		return graph.StateObservedOnlyGap
	case 0:
		return "rollup"
	default:
		return graph.StateCandidate
	}
}

// effectiveInvestigateBudget maps an MCP max_tokens input to the investigate
// budget: 0 (unset) -> the compact default, negative -> unlimited, positive
// as given (mirrors effectiveBudget's contract for impact).
func effectiveInvestigateBudget(maxTokens int) int {
	switch {
	case maxTokens == 0:
		return investigateDefaultBudget
	case maxTokens < 0:
		return 0
	}
	return maxTokens
}

// applyInvestigateBudget degrades investigateOutput in the order fixed by
// §1.2 step 6: candidates first, then flow-chain detail (collapsed to
// endpoints), then neighbour snippets. coverage_unresolved (Unresolved) and
// the root snippet are never trimmed — they are the trust lever and the
// anchor payload the whole call exists to deliver.
func applyInvestigateBudget(out *investigateOutput, maxTokens int) *investigateOutput {
	if maxTokens <= 0 {
		return out
	}
	if est := budget.Estimate(out); est <= maxTokens {
		out.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: est, Level: budget.LevelDetail}
		return out
	}
	out.Budget = &budget.Info{MaxTokens: maxTokens, Level: budget.LevelSummary}

	if len(out.Candidates) > 0 {
		out.Candidates = nil
		out.Budget.AppendNote("candidates dropped to fit the token budget")
		if est := budget.Estimate(out); est <= maxTokens {
			out.Budget.EstimatedTokens = est
			return out
		}
	}

	if hasMultiHopFlow(out.Flows) {
		for i, f := range out.Flows {
			if len(f.Chain) > 2 {
				out.Flows[i].Chain = []investigateNode{f.Chain[0], f.Chain[len(f.Chain)-1]}
			}
		}
		out.Budget.AppendNote("flow chains collapsed to endpoints to fit the token budget")
		if est := budget.Estimate(out); est <= maxTokens {
			out.Budget.EstimatedTokens = est
			return out
		}
	}

	for i := range out.Callers {
		out.Callers[i].Snippet = ""
	}
	for i := range out.Callees {
		out.Callees[i].Snippet = ""
	}
	for i := range out.Flows {
		for j := range out.Flows[i].Chain {
			out.Flows[i].Chain[j].Snippet = ""
		}
	}
	out.Budget.AppendNote("neighbour snippets dropped to fit the token budget")
	out.Budget.EstimatedTokens = budget.Estimate(out)
	return out
}

func hasMultiHopFlow(flows []investigateFlow) bool {
	for _, f := range flows {
		if len(f.Chain) > 2 {
			return true
		}
	}
	return false
}
