package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleGraph handles GET /api/graph?page=<n>&limit=<n>
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	const defaultLimit = 500
	const maxLimit = 2000

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// Collect all nodes from the in-memory index
	allNodes := make([]*graph.Node, 0, len(s.idx.Nodes))
	for _, n := range s.idx.Nodes {
		allNodes = append(allNodes, n)
	}

	// Paginate
	start := (page - 1) * limit
	if start >= len(allNodes) {
		writeJSON(w, http.StatusOK, CytoscapeGraph{
			Nodes: []CytoscapeNode{},
			Edges: []CytoscapeEdge{},
		})
		return
	}
	end := min(start+limit, len(allNodes))
	pageNodes := allNodes[start:end]

	// Build a set of node IDs in this page
	nodeSet := make(map[string]bool, len(pageNodes))
	for _, n := range pageNodes {
		nodeSet[n.ID] = true
	}

	// Collect edges where both endpoints are in the page
	var pageEdges []*graph.Edge
	for fromID := range nodeSet {
		for _, e := range s.idx.OutEdges[fromID] {
			if nodeSet[e.To] {
				pageEdges = append(pageEdges, e)
			}
		}
	}

	writeJSON(w, http.StatusOK, ToCytoscapeJSON(pageNodes, pageEdges))
}

// handleSearch handles GET /api/graph/search?q=<query>&limit=<n>
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

// handleNodeSource handles GET /api/node/{id}/source
func (s *Server) handleNodeSource(w http.ResponseWriter, r *http.Request) {
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

	src, err := os.ReadFile(node.File)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read source file: %s", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"source": string(src)})
}

// handleTrace handles GET /api/graph/trace?root=<id>&direction=<forward|backward|both>&depth=<n>
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if root == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'root'")
		return
	}

	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "forward"
	}

	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	if depth <= 0 {
		depth = 10
	}
	if depth > 50 {
		depth = 50
	}

	if _, ok := s.idx.Nodes[root]; !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	// Traverse in-memory; collect result node IDs
	nodeSet := map[string]bool{root: true}

	switch direction {
	case "forward":
		for _, r := range graph.Descendants(s.idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	case "backward":
		for _, r := range graph.Ancestors(s.idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	default: // "both"
		for _, r := range graph.Descendants(s.idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
		for _, r := range graph.Ancestors(s.idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	}

	// Fetch full node objects
	nodes := make([]*graph.Node, 0, len(nodeSet))
	for id := range nodeSet {
		if n, ok := s.idx.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}

	// Collect edges where both endpoints are in the result set
	var edges []*graph.Edge
	for fromID := range nodeSet {
		for _, e := range s.idx.OutEdges[fromID] {
			if nodeSet[e.To] {
				edges = append(edges, e)
			}
		}
	}

	writeJSON(w, http.StatusOK, ToCytoscapeJSON(nodes, edges))
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
