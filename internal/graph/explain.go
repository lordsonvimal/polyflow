package graph

import "sort"

// EdgeExplanation is the full provenance picture for one edge (SA.1): the edge
// itself, its endpoints, every evidence SourceRef, the static layer/rule chain,
// and the ledger rows recorded at the same source site. It answers "why is this
// edge here" — the question a false edge or a datalog rollout makes routine.
type EdgeExplanation struct {
	Edge       Edge            `json:"edge"`
	From       *Node           `json:"from,omitempty"`
	To         *Node           `json:"to,omitempty"`
	Sources    []SourceRef     `json:"sources"`
	RuleChain  []RuleStep      `json:"rule_chain"`
	LedgerRows []UnresolvedRef `json:"ledger_rows,omitempty"`
}

// RuleStep is one (layer, rule) contribution, deduplicated across Sources.
type RuleStep struct {
	Layer    string `json:"layer,omitempty"`
	Rule     string `json:"rule"`
	Provider string `json:"provider"`
}

// ExplainEdge assembles the provenance picture for edgeID. ledger is the full
// unresolved-ref set (the caller reads it from the store); rows at the From
// node's file+line are attached. Returns false if the edge is not in idx.
func ExplainEdge(idx *AdjacencyIndex, ledger []UnresolvedRef, edgeID string) (*EdgeExplanation, bool) {
	var edge *Edge
	for _, list := range idx.OutEdges {
		for _, e := range list {
			if e.ID == edgeID {
				edge = e
				break
			}
		}
		if edge != nil {
			break
		}
	}
	if edge == nil {
		return nil, false
	}

	ex := &EdgeExplanation{
		Edge:    *edge,
		From:    idx.Nodes[edge.From],
		To:      idx.Nodes[edge.To],
		Sources: edge.Sources,
	}

	seen := map[RuleStep]bool{}
	for _, src := range edge.Sources {
		if src.Rule == "" && src.Layer == "" {
			continue
		}
		step := RuleStep{Layer: src.Layer, Rule: src.Rule, Provider: src.Provider}
		if !seen[step] {
			seen[step] = true
			ex.RuleChain = append(ex.RuleChain, step)
		}
	}
	sort.Slice(ex.RuleChain, func(i, j int) bool {
		if ex.RuleChain[i].Layer != ex.RuleChain[j].Layer {
			return ex.RuleChain[i].Layer < ex.RuleChain[j].Layer
		}
		return ex.RuleChain[i].Rule < ex.RuleChain[j].Rule
	})

	if ex.From != nil && ex.From.File != "" {
		for _, u := range ledger {
			if u.File == ex.From.File && u.Line == ex.From.Line {
				ex.LedgerRows = append(ex.LedgerRows, u)
			}
		}
		sort.Slice(ex.LedgerRows, func(i, j int) bool {
			if ex.LedgerRows[i].Kind != ex.LedgerRows[j].Kind {
				return ex.LedgerRows[i].Kind < ex.LedgerRows[j].Kind
			}
			return ex.LedgerRows[i].Name < ex.LedgerRows[j].Name
		})
	}

	return ex, true
}
