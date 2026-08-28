package server

import (
	"net/http"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// unknownEdgeEntry is the web API's shape for one low-confidence edge —
// the same information `polyflow status --unknown-edges` and the MCP
// `unknown_edges` tool report, described for JSON consumers instead of a
// terminal line. FromID feeds GET /api/node/{id} the same way search/
// hierarchy results already do.
type unknownEdgeEntry struct {
	Confidence string `json:"confidence"`
	Type       string `json:"type"`
	From       string `json:"from"`
	FromID     string `json:"from_id"`
	Service    string `json:"service,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	To         string `json:"to"`
	Label      string `json:"label,omitempty"`
}

// handleUnknownEdges handles GET /api/unknown-edges?min_confidence=&service=&edge_type=&page=&limit=
//
// Uses the fleet-merged s.idx (deadcodeapi.go's pattern), not s.db directly
// — unlike handleUnresolved (which is local-store-scoped by construction,
// since UnresolvedRef has no cross-service concept), most of what this
// endpoint exists to surface is cross-service bridge.db edges, so it must
// see the same merged graph search/context/impact/trace already do.
// contract.FilterEdgesByConfidence is the same function `polyflow status
// --unknown-edges` and the MCP `unknown_edges` tool call — this endpoint
// can't drift from either on what counts as "still unresolved."
func (s *Server) handleUnknownEdges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minConfidence := q.Get("min_confidence")
	if minConfidence == "" {
		minConfidence = graph.ConfidenceUnknown
	}
	service := q.Get("service")
	edgeType := q.Get("edge_type")

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	matched := contract.FilterEdgesByConfidence(idx, minConfidence)

	var all []unknownEdgeEntry
	byConfidence := map[string]int{}
	for _, e := range matched {
		if edgeType != "" && string(e.Type) != edgeType {
			continue
		}
		from := idx.Nodes[e.From]
		if service != "" && (from == nil || from.Service != service) {
			continue
		}
		entry := unknownEdgeEntry{Confidence: e.Confidence, Type: string(e.Type), Label: e.Label, FromID: e.From}
		if from != nil {
			entry.From = from.Label
			entry.Service = from.Service
			entry.File = from.File
			entry.Line = from.Line
		} else {
			entry.From = e.From
		}
		if to := idx.Nodes[e.To]; to != nil {
			entry.To = to.Label
		} else {
			entry.To = e.To
		}
		all = append(all, entry)
		byConfidence[e.Confidence]++
	}
	if all == nil {
		all = []unknownEdgeEntry{}
	}

	total := len(all)
	start := (page - 1) * limit
	var pageItems []unknownEdgeEntry
	if start < total {
		end := start + limit
		if end > total {
			end = total
		}
		pageItems = all[start:end]
	}
	if pageItems == nil {
		pageItems = []unknownEdgeEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"edges":         pageItems,
		"total":         total,
		"page":          page,
		"by_confidence": byConfidence,
	})
}
