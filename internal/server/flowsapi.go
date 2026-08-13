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
func flowsThrough(idx *graph.AdjacencyIndex, target *graph.Node, limit int) *graph.FlowsThroughResult {
	rootIDs := map[string]bool{}
	if back := trace.Run(idx, target.ID, "backward", 0, false, 0); back != nil {
		for _, c := range back.Chains {
			if len(c.Hops) > 0 {
				rootIDs[c.Hops[0].ID] = true
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
		fwd := trace.Run(idx, rid, "forward", 0, false, 0)
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
	return &graph.FlowsThroughResult{Flows: flows, Truncated: truncated}
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
