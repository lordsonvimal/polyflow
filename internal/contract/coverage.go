package contract

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// KindCoverage holds the matched and unresolved producer counts for one contract kind.
type KindCoverage struct {
	Kind       string `json:"kind"`
	Matched    int    `json:"matched"`    // edges emitted with static or inferred confidence
	Unresolved int    `json:"unresolved"` // ledger entries + unknown_edge emits
	Dynamic    int    `json:"dynamic"`    // dynamic_<kind> ledger entries (G.6 surfacing)
	Indirect   int    `json:"indirect"`   // edges resolved via alias or wrapper (G.7)
}

// ComputeCoverage returns per-kind coverage stats from a Link result.
// rules is used to derive the edge-type→kind mapping; the result carries
// the actual matched edges and ledger entries.
func ComputeCoverage(rules []Rule, result Result) []KindCoverage {
	edgeTypeToKind := make(map[graph.EdgeType]string, len(rules))
	var kindOrder []string
	kindSeen := map[string]bool{}
	for _, r := range rules {
		edgeTypeToKind[r.Edge.Type] = string(r.Kind)
		if !kindSeen[string(r.Kind)] {
			kindSeen[string(r.Kind)] = true
			kindOrder = append(kindOrder, string(r.Kind))
		}
	}

	matched := map[string]int{}
	unresolved := map[string]int{}

	indirect := map[string]int{}
	for _, e := range result.Edges {
		k := edgeTypeToKind[e.Type]
		if k == "" {
			continue
		}
		if e.Confidence == graph.ConfidenceUnknown {
			unresolved[k]++
		} else {
			matched[k]++
		}
		// G.7: count edges resolved via alias/wrapper indirection.
		if via := e.Meta["via"]; via == "alias" || via == "wrapper" {
			indirect[k]++
		}
	}
	dynamic := map[string]int{}
	for _, u := range result.Unresolved {
		// dynamic_<kind> unresolved refs are counted in the Dynamic column of
		// their base kind (e.g. dynamic_url → http, dynamic_topic → kafka/nats).
		// They also appear as their own "kind" row for full ledger visibility.
		isDynamic := false
		for _, dk := range []string{
			"dynamic_url", "dynamic_topic", "dynamic_queue",
			"dynamic_channel", "dynamic_event",
		} {
			if u.Kind == dk {
				isDynamic = true
				break
			}
		}
		if isDynamic {
			dynamic[u.Kind]++
		} else {
			unresolved[u.Kind]++
		}
		if !kindSeen[u.Kind] {
			kindSeen[u.Kind] = true
			kindOrder = append(kindOrder, u.Kind)
		}
	}

	sort.Strings(kindOrder)
	out := make([]KindCoverage, 0, len(kindOrder))
	for _, k := range kindOrder {
		out = append(out, KindCoverage{
			Kind:       k,
			Matched:    matched[k],
			Unresolved: unresolved[k],
			Dynamic:    dynamic[k],
			Indirect:   indirect[k],
		})
	}
	return out
}
