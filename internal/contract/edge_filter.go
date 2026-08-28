package contract

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FilterEdgesByConfidence returns every confidence-bearing edge in idx at or
// below minConfidence (unknown, partial, inferred, static — the same tier
// order ConfidenceRank defines), sorted by the producer node's
// (service, file, line).
//
// A fleet-merged index can carry two edges for the same producer: its own
// local store's view (e.g. "unknown" — a single repo alone can't see
// another service's routes) and bridge.db's wider-visibility view of the
// identical call site (e.g. "inferred", once the cross-service route is
// visible). Both are correct for the scope that produced them, but a
// producer with any better-than-threshold edge elsewhere in idx is not
// actually unresolved fleet-wide — including its stale local-only edge
// anyway would overcount by exactly the producers a wider view already
// answers for. This is filtered out here, once, rather than by every
// caller (cmd/polyflow's `status --unknown-edges` and the MCP
// `unknown_edges` tool both call this).
func FilterEdgesByConfidence(idx *graph.AdjacencyIndex, minConfidence string) []graph.Edge {
	threshold := ConfidenceRank(minConfidence)

	// bestByProducer tracks the best confidence seen per producer (From
	// node) across confidence-bearing edges only — edges with no confidence
	// at all (e.g. plain "calls") are excluded so they can't spuriously
	// "resolve" an unrelated http_call producer that merely shares a From
	// node.
	bestByProducer := make(map[string]int)
	for _, e := range idx.AllEdges() {
		if e.Confidence == "" {
			continue
		}
		if r := ConfidenceRank(e.Confidence); r > bestByProducer[e.From] {
			bestByProducer[e.From] = r
		}
	}

	var matched []graph.Edge
	for _, e := range idx.AllEdges() {
		if e.Confidence == "" {
			continue
		}
		if ConfidenceRank(e.Confidence) > threshold {
			continue
		}
		if bestByProducer[e.From] > threshold {
			continue
		}
		matched = append(matched, e)
	}

	sortEdgesByProducerLocation(idx, matched)
	return matched
}

// sortEdgesByProducerLocation orders edges by their producer node's
// (service, file, line) so a flat report groups file-adjacent edges
// together instead of scattering them in ID order.
func sortEdgesByProducerLocation(idx *graph.AdjacencyIndex, edges []graph.Edge) {
	sort.Slice(edges, func(i, j int) bool {
		ni, nj := idx.Nodes[edges[i].From], idx.Nodes[edges[j].From]
		if ni == nil || nj == nil {
			return edges[i].ID < edges[j].ID
		}
		if ni.Service != nj.Service {
			return ni.Service < nj.Service
		}
		if ni.File != nj.File {
			return ni.File < nj.File
		}
		return ni.Line < nj.Line
	})
}
