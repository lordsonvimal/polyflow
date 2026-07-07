package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleSearch handles GET /api/search?q=<query>&limit=<n>
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'q'")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}

	nodes, err := s.db.SearchNodes(r.Context(), q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

// handleNode handles GET /api/node/{id}
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing node id")
		return
	}

	node, err := s.db.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	edgesFrom, _ := s.db.ListEdgesFrom(r.Context(), id)
	edgesTo, _ := s.db.ListEdgesTo(r.Context(), id)

	writeJSON(w, http.StatusOK, map[string]any{
		"node":       node,
		"edges_from": edgesFrom,
		"edges_to":   edgesTo,
	})
}

// handleTrace handles GET /api/trace?from=<id>&to=<id>&depth=<n>
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	// TODO: implement BFS/DFS trace between two nodes
	writeError(w, http.StatusNotImplemented, "trace: not yet implemented")
}

// handleStats handles GET /api/stats
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	nodes, edges, err := s.db.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"nodes": nodes,
		"edges": edges,
	})
}

// handleEvents handles GET /api/events (Server-Sent Events for live index updates)
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, "data: {\"type\":\"connected\"}\n\n")
	flusher.Flush()

	<-r.Context().Done()
}
