package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/trace"
)

// handleFlowsEntrypoints handles GET /api/flows/entrypoints?service=&kind=
func (s *Server) handleFlowsEntrypoints(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	kind := r.URL.Query().Get("kind")

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	writeJSON(w, http.StatusOK, graph.Entrypoints(idx, service, kind))
}

// handleFlowsThrough handles GET /api/flows/through/{id}?limit=
func (s *Server) handleFlowsThrough(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing node id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	target, ok := idx.Nodes[id]
	if !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	result := flowsThrough(idx, target, limit)
	writeJSON(w, http.StatusOK, result)
}

// flowsThrough finds every entrypoint that reaches target, and for each the
// forward chain(s) from that entrypoint passing through target. It reuses
// trace.Run for both the backward walk (to discover candidate entrypoint
// roots) and the forward walk (to build the through-chain) rather than
// reimplementing traversal.
//
// Both walks use the default (noise-filtered) include set rather than
// AllNoiseInclude: unlike impact/corpus callers of trace.Run, which
// deliberately want completeness, a flow chain is meant to read as one
// causal path — containment/import/mixin/filter-chain edges aren't part of
// that story, and left visible they dominate chain enumeration's fixed
// 100-chain display cap (module-graph import fan-out in particular) before
// enumeration ever reaches the real behavioral edge, so backward
// root-discovery would silently miss real entrypoints.
func flowsThrough(idx *graph.AdjacencyIndex, target *graph.Node, limit int) *graph.FlowsThroughResult {
	noise := graph.DefaultNoiseInclude("debug")
	rootIDs := map[string]bool{}
	if back := trace.Run(idx, target.ID, "backward", 0, false, 0, noise, 0); back != nil {
		// A type-based entrypoint (http_handler, subscriber, route, worker,
		// grpc_handler, graphql_resolver) qualifies regardless of whether
		// something else calls it — an HTTP route registered by a router
		// setup function is still an entrypoint, not a mid-chain hop. So
		// every hop that independently classifies as an entrypoint is a
		// candidate root, not just the chain's terminal (zero-incoming-edge)
		// node: requiring terminal-only meant a route several calls deep
		// under main() was never recognized, and the walk kept going all the
		// way up to main — whose own forward fan-out is too large to find
		// its way back down to any single target within the chain-display
		// cap. Terminal nodes are still covered: they're just Hops[0] of
		// their own chain, included by classifying every hop here too.
		for _, c := range back.Chains {
			for _, h := range c.Hops {
				n, ok := idx.Nodes[h.ID]
				if !ok {
					continue
				}
				if _, _, ok := graph.ClassifyEntrypoint(n); ok {
					rootIDs[h.ID] = true
				}
			}
		}
	}
	if _, _, ok := graph.ClassifyEntrypoint(target); ok {
		rootIDs[target.ID] = true
	}

	var roots []string
	for id := range rootIDs {
		roots = append(roots, id)
	}
	sort.Strings(roots)

	var flows []graph.FlowEntry
	truncated := false
	for _, rid := range roots {
		rn := idx.Nodes[rid]
		kind, _, ok := graph.ClassifyEntrypoint(rn)
		if !ok {
			continue
		}
		fwd := trace.Run(idx, rid, "forward", 0, false, 0, noise, 0)
		if fwd == nil {
			continue
		}
		for _, c := range fwd.Chains {
			if !chainContainsID(c, target.ID) {
				continue
			}
			if len(flows) >= limit {
				truncated = true
				break
			}
			flows = append(flows, graph.FlowEntry{
				Entrypoint: graph.EntrypointRefFromNode(rn, kind),
				Chain:      toFlowHops(c.Hops),
			})
		}
		if truncated {
			break
		}
	}
	if flows == nil {
		flows = []graph.FlowEntry{}
	}
	deadEnd := len(flows) == 0 && !graph.HasOutgoingFlowEdge(idx, target.ID)
	return &graph.FlowsThroughResult{Flows: flows, Truncated: truncated, DeadEnd: deadEnd}
}

func chainContainsID(c trace.Chain, id string) bool {
	for _, h := range c.Hops {
		if h.ID == id {
			return true
		}
	}
	return false
}

func toFlowHops(hops []trace.Hop) []graph.FlowHop {
	out := make([]graph.FlowHop, len(hops))
	for i, h := range hops {
		out[i] = graph.FlowHop{
			NodeID:            h.ID,
			Label:             h.Label,
			Service:           h.Service,
			EdgeType:          h.EdgeType,
			EdgeLabel:         h.EdgeLabel,
			CrossService:      h.CrossService,
			Confidence:        h.Confidence,
			VerificationState: h.VerificationState,
		}
	}
	return out
}

// handleFlowsPaths handles GET /api/flows/paths?from=&to=&k=&max_depth=
func (s *Server) handleFlowsPaths(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'from' or 'to'")
		return
	}
	k, _ := strconv.Atoi(q.Get("k"))
	maxDepth, _ := strconv.Atoi(q.Get("max_depth"))

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	result, err := graph.KShortestFlowPaths(idx, from, to, k, maxDepth)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleFlowsRefine handles
// GET /api/flows/refine?waypoints=<id,id,…>&direction=forward
func (s *Server) handleFlowsRefine(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := q.Get("waypoints")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'waypoints'")
		return
	}
	direction := q.Get("direction")
	if direction == "" {
		direction = "forward"
	}

	var waypoints []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			waypoints = append(waypoints, id)
		}
	}
	if len(waypoints) == 0 {
		writeError(w, http.StatusBadRequest, "missing query parameter 'waypoints'")
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	result, err := graph.RefineWaypoints(idx, waypoints, direction)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleNodeLinks handles
// GET /api/node/{id}/links?direction=upstream|downstream&depth=1&kind=&service=&offset=&limit=
// — the UF.8 link explorer's paginated single-direction adjacency.
func (s *Server) handleNodeLinks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing node id")
		return
	}
	q := r.URL.Query()
	direction := q.Get("direction")
	if direction != "upstream" && direction != "downstream" {
		direction = "downstream"
	}
	depth, _ := strconv.Atoi(q.Get("depth"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	if _, ok := idx.Nodes[id]; !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	result := graph.LinkExplorerAdjacency(idx, id, direction, depth, offset, limit, q.Get("kind"), q.Get("service"))
	writeJSON(w, http.StatusOK, result)
}

// handleSeam handles GET /api/seam/{edge-id}
func (s *Server) handleSeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing edge id")
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	result, err := graph.Seam(idx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleServiceChannels handles GET /api/services/channels?from=&to=
func (s *Server) handleServiceChannels(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	result, err := graph.ServiceChannels(idx, from, to)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
