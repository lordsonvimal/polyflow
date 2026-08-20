package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/budget"
	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// bundleElementReq is one entry of the POST /api/context/bundle request's
// "elements" array.
type bundleElementReq struct {
	Kind string   `json:"kind"` // node | edge | flow | group
	IDs  []string `json:"ids"`
}

// bundleRequest is the POST /api/context/bundle request body (UB.6, pinned
// JSON contract).
type bundleRequest struct {
	Elements  []bundleElementReq `json:"elements"`
	Mode      string             `json:"mode"` // "viewed" | "expanded"
	Depth     int                `json:"depth"`
	Snippets  bool               `json:"snippets"`
	MaxTokens int                `json:"max_tokens"`
}

// bundleResponse is the POST /api/context/bundle response body.
type bundleResponse struct {
	Markdown       string   `json:"markdown"`
	TokensEstimate int      `json:"tokens_estimate"`
	Truncated      bool     `json:"truncated"`
	Omitted        []string `json:"omitted"`
}

// bundleEntry is one node participating in a bundleBlock, with the role it
// plays there ("target", "upstream via calls", "producer chain", …).
type bundleEntry struct {
	Node *graph.Node
	Role string
}

// bundleChain is one hop-list rendered under the "## Flow" section.
type bundleChain struct {
	Hops []graph.FlowHop
}

// bundleBlock is the rendering of one requested (kind, id) pair — the unit
// that gets dropped whole ("smallest-value-last") when trimming to fit
// max_tokens.
type bundleBlock struct {
	ID      string
	Entries []bundleEntry
	Chains  []bundleChain
}

const bundleMaxSnippetLines = 60

// estimateTokens applies the same ~4-bytes-per-token heuristic budget.Estimate
// uses for JSON, directly to the markdown text (budget.Estimate only accepts
// values it can json.Marshal, which would misrepresent plain-text length via
// escaping/quoting).
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// handleContextBundle handles POST /api/context/bundle: the UI's "copy
// context" for any node/edge/flow/group selection, built entirely on
// context.Build, graph.Seam and flowsThrough — never a reimplemented
// traversal.
func (s *Server) handleContextBundle(w http.ResponseWriter, r *http.Request) {
	var req bundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Elements) == 0 {
		writeError(w, http.StatusBadRequest, "elements must be non-empty")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "viewed"
	}
	if mode != "viewed" && mode != "expanded" {
		writeError(w, http.StatusBadRequest, `mode must be "viewed" or "expanded"`)
		return
	}
	depth := req.Depth
	if depth <= 0 {
		depth = 3
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	// Rule 12 / test contract: an unknown id is an error naming it, never
	// silently skipped.
	var unknown []string
	for _, el := range req.Elements {
		switch el.Kind {
		case "node", "group", "flow":
			for _, id := range el.IDs {
				if idx.Nodes[id] == nil {
					unknown = append(unknown, id)
				}
			}
		case "edge":
			for _, id := range el.IDs {
				if graph.FindEdgeByID(idx, id) == nil {
					unknown = append(unknown, id)
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "unknown element kind: "+el.Kind)
			return
		}
	}
	if len(unknown) > 0 {
		writeError(w, http.StatusNotFound, "unknown id(s): "+strings.Join(unknown, ", "))
		return
	}

	unresolved, err := s.db.ListUnresolvedRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	root := filepath.Dir(s.configPathOrDefault())
	blocks := buildBundleBlocks(idx, req.Elements, mode, depth)

	resp := renderBundleResponse(root, blocks, req.Snippets, req.MaxTokens, unresolved)
	writeJSON(w, http.StatusOK, resp)
}

// buildBundleBlocks resolves every requested (kind, id) pair into one
// bundleBlock apiece, in request order — "viewed" mode yields exactly the
// given ids; "expanded" mode additionally pulls in each element's closure
// (node -> context.Build at depth; edge -> its UB.5 seam; flow -> its
// chains). Deviation: kind "flow" and kind "group" have no dedicated "viewed"
// reading in the plan text — a flow IS its chain (there is nothing narrower
// to show), so both modes resolve it via flowsThrough; "group" has no
// backing node type anywhere in the graph (grepped, zero hits), so each id
// in its IDs list is treated exactly like a "node" element.
func buildBundleBlocks(idx *graph.AdjacencyIndex, elements []bundleElementReq, mode string, depth int) []bundleBlock {
	var blocks []bundleBlock
	for _, el := range elements {
		for _, id := range el.IDs {
			switch el.Kind {
			case "node", "group":
				blocks = append(blocks, buildNodeBlock(idx, id, mode, depth))
			case "edge":
				blocks = append(blocks, buildEdgeBlock(idx, id, mode))
			case "flow":
				blocks = append(blocks, buildFlowBlock(idx, id))
			}
		}
	}
	return blocks
}

func buildNodeBlock(idx *graph.AdjacencyIndex, id, mode string, depth int) bundleBlock {
	n := idx.Nodes[id]
	entries := []bundleEntry{{Node: n, Role: "target"}}
	if mode == "expanded" {
		res := pfcontext.Build(idx, id, "debug", depth, false, 0, graph.DefaultNoiseInclude("debug"))
		seen := map[string]bool{id: true}
		for _, tn := range res.Upstream {
			if seen[tn.ID] {
				continue
			}
			seen[tn.ID] = true
			if un := idx.Nodes[tn.ID]; un != nil {
				entries = append(entries, bundleEntry{Node: un, Role: roleLabel("upstream", tn.EdgeType)})
			}
		}
		for _, tn := range res.Downstream {
			if seen[tn.ID] {
				continue
			}
			seen[tn.ID] = true
			if dn := idx.Nodes[tn.ID]; dn != nil {
				entries = append(entries, bundleEntry{Node: dn, Role: roleLabel("downstream", tn.EdgeType)})
			}
		}
	}
	return bundleBlock{ID: id, Entries: entries}
}

func roleLabel(direction, edgeType string) string {
	if edgeType == "" {
		return direction
	}
	return fmt.Sprintf("%s via %s", direction, edgeType)
}

func buildEdgeBlock(idx *graph.AdjacencyIndex, id, mode string) bundleBlock {
	e := graph.FindEdgeByID(idx, id)
	var entries []bundleEntry
	if from := idx.Nodes[e.From]; from != nil {
		entries = append(entries, bundleEntry{Node: from, Role: fmt.Sprintf("edge source (%s)", e.Type)})
	}
	if to := idx.Nodes[e.To]; to != nil {
		entries = append(entries, bundleEntry{Node: to, Role: fmt.Sprintf("edge target (%s)", e.Type)})
	}

	var chains []bundleChain
	if mode == "expanded" {
		if seam, err := graph.Seam(idx, id); err == nil && seam != nil {
			for _, p := range seam.Producers {
				if len(p.Chain) > 0 {
					chains = append(chains, bundleChain{Hops: p.Chain})
				}
				for _, h := range p.Chain {
					if hn := idx.Nodes[h.NodeID]; hn != nil {
						entries = append(entries, bundleEntry{Node: hn, Role: "producer chain"})
					}
				}
			}
			for _, c := range seam.Consumers {
				if len(c.Chain) > 0 {
					chains = append(chains, bundleChain{Hops: c.Chain})
				}
				for _, h := range c.Chain {
					if hn := idx.Nodes[h.NodeID]; hn != nil {
						entries = append(entries, bundleEntry{Node: hn, Role: "consumer chain"})
					}
				}
			}
		}
	}
	return bundleBlock{ID: id, Entries: dedupEntries(entries), Chains: chains}
}

// buildFlowBlock treats id as the "through" node of a UB.5 flows/through
// query — the only single-id flow lookup the API has.
func buildFlowBlock(idx *graph.AdjacencyIndex, id string) bundleBlock {
	n := idx.Nodes[id]
	entries := []bundleEntry{{Node: n, Role: "flow target"}}
	var chains []bundleChain
	if res := flowsThrough(idx, n, 20); res != nil {
		for _, f := range res.Flows {
			chains = append(chains, bundleChain{Hops: f.Chain})
			for _, h := range f.Chain {
				if hn := idx.Nodes[h.NodeID]; hn != nil {
					entries = append(entries, bundleEntry{Node: hn, Role: "flow hop"})
				}
			}
		}
	}
	return bundleBlock{ID: id, Entries: dedupEntries(entries), Chains: chains}
}

// dedupEntries keeps the first occurrence of each node id, preserving order.
func dedupEntries(entries []bundleEntry) []bundleEntry {
	seen := map[string]bool{}
	out := make([]bundleEntry, 0, len(entries))
	for _, e := range entries {
		if e.Node == nil || seen[e.Node.ID] {
			continue
		}
		seen[e.Node.ID] = true
		out = append(out, e)
	}
	return out
}

// renderBundleResponse picks the output shape for max_tokens: the full
// render when it fits, else snippets are trimmed first, then whole blocks
// smallest-value-last (the tail of the request-ordered block list) — the
// budget.TrimToFit precedent used throughout the codebase.
func renderBundleResponse(root string, blocks []bundleBlock, snippets bool, maxTokens int, unresolved []graph.UnresolvedRef) bundleResponse {
	render := func(bs []bundleBlock, withSnippets bool) string {
		return renderBundleMarkdown(root, bs, withSnippets, unresolved)
	}

	markdown := render(blocks, snippets)
	tokens := estimateTokens(markdown)
	if maxTokens <= 0 || tokens <= maxTokens {
		return bundleResponse{Markdown: markdown, TokensEstimate: tokens, Truncated: false, Omitted: []string{}}
	}

	truncated := true
	var omitted []string

	if snippets {
		noSnippet := render(blocks, false)
		if estimateTokens(noSnippet) <= maxTokens {
			return bundleResponse{Markdown: noSnippet, TokensEstimate: estimateTokens(noSnippet), Truncated: truncated, Omitted: []string{}}
		}
	}

	kept := budget.TrimToFit(len(blocks), maxTokens, func(n int) int {
		return estimateTokens(render(blocks[:n], false))
	})
	for _, b := range blocks[kept:] {
		omitted = append(omitted, b.ID)
	}
	markdown = render(blocks[:kept], false)
	if len(omitted) > 0 {
		markdown += fmt.Sprintf("\n> Truncated at %d tokens: omitted %s\n", maxTokens, strings.Join(omitted, ", "))
	}
	if omitted == nil {
		omitted = []string{}
	}
	return bundleResponse{Markdown: markdown, TokensEstimate: estimateTokens(markdown), Truncated: truncated, Omitted: omitted}
}

// renderBundleMarkdown lays out the pinned markdown shape: a summary header,
// per-service "## <service>" sections containing per-file "### <path>
// (<lang>)" sections with one element each, a "## Flow" hop-list section,
// a "## Unresolved" section scoped to the touched files, and a footer line.
func renderBundleMarkdown(root string, blocks []bundleBlock, withSnippets bool, allUnresolved []graph.UnresolvedRef) string {
	var names []string
	for _, b := range blocks {
		if len(b.Entries) > 0 {
			names = append(names, b.Entries[0].Node.Label)
		}
	}

	type fileKey struct{ service, file string }
	var svcOrder []string
	svcSeen := map[string]bool{}
	fileOrder := map[string][]string{}
	fileSeen := map[fileKey]bool{}
	langByFile := map[fileKey]string{}
	entriesByFile := map[fileKey][]bundleEntry{}
	seenNode := map[string]bool{}
	touchedFiles := map[string]bool{}

	for _, b := range blocks {
		for _, e := range b.Entries {
			if e.Node == nil || seenNode[e.Node.ID] {
				continue
			}
			seenNode[e.Node.ID] = true
			n := e.Node
			touchedFiles[n.File] = true
			if !svcSeen[n.Service] {
				svcSeen[n.Service] = true
				svcOrder = append(svcOrder, n.Service)
			}
			fk := fileKey{n.Service, n.File}
			if !fileSeen[fk] {
				fileSeen[fk] = true
				fileOrder[n.Service] = append(fileOrder[n.Service], n.File)
				langByFile[fk] = n.Language
			}
			entriesByFile[fk] = append(entriesByFile[fk], e)
		}
	}

	var sb strings.Builder
	sb.WriteString("# Context: " + strings.Join(names, ", ") + "\n\n")

	for _, svc := range svcOrder {
		sb.WriteString("## " + svc + "\n\n")
		for _, file := range fileOrder[svc] {
			fk := fileKey{svc, file}
			sb.WriteString(fmt.Sprintf("### %s (%s)\n\n", file, langByFile[fk]))
			for _, e := range entriesByFile[fk] {
				n := e.Node
				endLine := n.EndLine
				if endLine <= 0 {
					endLine = n.Line
				}
				sb.WriteString(fmt.Sprintf("**%s** `%s:%d–%d`\n", n.Label, n.File, n.Line, endLine))
				sb.WriteString("role: " + e.Role + "\n")
				if withSnippets {
					if span, _ := budget.SnippetSpan(root, n.File, n.Line, n.EndLine, bundleMaxSnippetLines); span != "" {
						sb.WriteString("```" + n.Language + "\n" + span + "\n```\n")
					}
				}
				sb.WriteString("\n")
			}
		}
	}

	var chainLines []string
	for _, b := range blocks {
		for _, c := range b.Chains {
			if line := renderChainLine(c); line != "" {
				chainLines = append(chainLines, line)
			}
		}
	}
	if len(chainLines) > 0 {
		sb.WriteString("## Flow\n\n")
		for _, l := range chainLines {
			sb.WriteString("- " + l + "\n")
		}
		sb.WriteString("\n")
	}

	files := make(map[string]bool, len(touchedFiles))
	for f := range touchedFiles {
		files[f] = true
	}
	scoped := graph.UnresolvedInFiles(allUnresolved, files)
	if len(scoped) > 0 {
		sb.WriteString("## Unresolved\n\n")
		for _, u := range scoped {
			sb.WriteString(fmt.Sprintf("- %s `%s:%d` (%s)\n", u.Name, u.File, u.Line, u.Kind))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("_polyflow context bundle, %d nodes, ~%d tokens_\n", len(seenNode), estimateTokens(sb.String())))
	return sb.String()
}

// renderChainLine renders one flow chain as "A —edge_type→ B —edge_type→ C",
// pinned by the plan's worked example.
func renderChainLine(c bundleChain) string {
	if len(c.Hops) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, h := range c.Hops {
		if i > 0 {
			edgeType := h.EdgeType
			if edgeType == "" {
				edgeType = "flow"
			}
			sb.WriteString(fmt.Sprintf(" —%s→ ", edgeType))
		}
		sb.WriteString(h.Label)
	}
	return sb.String()
}
