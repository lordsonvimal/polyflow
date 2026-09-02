package semantic

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// FederatedSearch runs the same query against multiple fleet members' own
// Searchers (docs/global-fleet-registry-plan.md, GR.3's "search's federation
// scope" decision: an agent asking "where is X" inside a fleet member almost
// always wants the whole fleet's answer, not just the locally-active
// member's) and merges each section's results.
//
// Each member's Response is already itself RRF-fused internally (Search);
// a hit's raw FTS/vector rank is meaningless across different stores'
// corpora, so this pass fuses across members using each hit's within-section
// rank in its own member's Response — a second application of the same
// Reciprocal Rank Fusion formula (k=rrfK) Search uses, reused rather than
// reimplemented as a distinct algorithm. Node IDs are disjoint across
// members by construction (FR.0), so a given entity ID contributes from at
// most one member.
func FederatedSearch(ctx context.Context, searchers map[string]*Searcher, q string, limit int) (Response, error) {
	if len(searchers) == 0 {
		return Response{}, fmt.Errorf("federated search: no member searchers given")
	}
	if limit <= 0 {
		limit = 20
	}

	names := make([]string, 0, len(searchers))
	for svc := range searchers {
		names = append(names, svc)
	}
	sort.Strings(names) // bug-class rule 2: deterministic iteration order

	if len(names) == 1 {
		svc := names[0]
		resp, err := searchers[svc].Search(ctx, q, limit)
		if err != nil {
			return Response{}, err
		}
		tagService(resp.Nodes, svc)
		tagService(resp.Flows, svc)
		tagService(resp.Docs, svc)
		return resp, nil
	}

	perMember := make(map[string]Response, len(names))
	var notes []string
	var healthy, degraded []string
	for _, svc := range names {
		resp, err := searchers[svc].Search(ctx, q, limit)
		if err != nil {
			return Response{}, fmt.Errorf("search %s: %w", svc, err)
		}
		tagService(resp.Nodes, svc)
		tagService(resp.Flows, svc)
		tagService(resp.Docs, svc)
		perMember[svc] = resp
		if resp.Semantic != "" {
			notes = append(notes, svc+": "+resp.Semantic)
		}
		// A member whose vector arm is unavailable (no embeddings indexed /
		// skipped / embedder error — search.go stamps all of these
		// "unavailable: …") contributes only lexical hits. Merged into a
		// pool that other members answered semantically, those lexical
		// token-matches sort alongside real semantic hits (every RRF
		// rank-1 score is ~1/(k+1) regardless of match quality), so one
		// stale member poisons the whole fleet's ranking. Drop such
		// members from the merge as long as at least one member is
		// healthy; if every member is degraded, keep them all (a
		// uniformly-lexical ranking is at least internally fair).
		if strings.HasPrefix(resp.Semantic, "unavailable") {
			degraded = append(degraded, svc)
		} else {
			healthy = append(healthy, svc)
		}
	}

	mergeNames := names
	if len(healthy) > 0 && len(degraded) > 0 {
		mergeNames = healthy
		notes = append([]string{
			"excluded from ranking (no embeddings — run 'polyflow index' in each): " +
				strings.Join(degraded, ", "),
		}, notes...)
	}

	pick := func(r Response, section string) []Hit {
		switch section {
		case "nodes":
			return r.Nodes
		case "flows":
			return r.Flows
		default:
			return r.Docs
		}
	}
	merge := func(section string) []Hit {
		return mergeAcrossMembers(mergeNames, perMember, pick, section, limit)
	}

	return Response{
		Nodes:    merge("nodes"),
		Flows:    merge("flows"),
		Docs:     merge("docs"),
		Semantic: strings.Join(notes, "; "),
	}, nil
}

func mergeAcrossMembers(names []string, perMember map[string]Response, pick func(Response, string) []Hit, section string, limit int) []Hit {
	type acc struct {
		hit   Hit
		score float64
	}
	byID := make(map[string]*acc)
	for _, svc := range names {
		for rank, h := range pick(perMember[svc], section) {
			a, ok := byID[h.Entity.ID]
			if !ok {
				a = &acc{hit: h}
				byID[h.Entity.ID] = a
			}
			a.score += 1.0 / (float64(rrfK) + float64(rank+1))
			if h.Retrieval == "exact" {
				a.hit.Retrieval = "exact"
			}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]Hit, 0, len(byID))
	for _, id := range ids {
		a := byID[id]
		a.hit.Score = roundScore(a.score)
		out = append(out, a.hit)
	}

	sort.Slice(out, func(i, j int) bool {
		iEx := out[i].Retrieval == "exact"
		jEx := out[j].Retrieval == "exact"
		if iEx != jEx {
			return iEx
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Entity.ID < out[j].Entity.ID
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ScopedSearch is the shared federation-scope decision for every search
// entry point (the MCP search tool, the web /api/graph/search handler, and
// the CLI search command), so all three behave identically. The service
// argument selects the scope:
//
//	""            → the current workspace only (the local Searcher). DEFAULT.
//	"*" / "fleet" → federate across every locally-resolved fleet member.
//	"<name>"      → just that one fleet member (local Searcher if unknown).
//
// Before Tier GR.7 the default ("") federated fleet-wide; that surfaced
// unrelated repos on every query and let embedding-less members flood the
// ranking (see FederatedSearch). Fleet-wide search is now opt-in.
func ScopedSearch(ctx context.Context, local *Searcher, fleet map[string]*Searcher, q, service string, limit int) (Response, error) {
	switch service {
	case "":
		return local.Search(ctx, q, limit)
	case "*", "fleet", "all":
		if len(fleet) > 1 {
			return FederatedSearch(ctx, fleet, q, limit)
		}
		return local.Search(ctx, q, limit)
	default:
		if sr, ok := fleet[service]; ok {
			return sr.Search(ctx, q, limit)
		}
		return local.Search(ctx, q, limit)
	}
}

func tagService(hits []Hit, svc string) {
	for i := range hits {
		hits[i].Entity.Service = svc
	}
}
