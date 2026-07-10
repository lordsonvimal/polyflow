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

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	// Collect all nodes from the in-memory index
	allNodes := make([]*graph.Node, 0, len(idx.Nodes))
	for _, n := range idx.Nodes {
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
		for _, e := range idx.OutEdges[fromID] {
			if nodeSet[e.To] {
				pageEdges = append(pageEdges, e)
			}
		}
	}

	writeJSON(w, http.StatusOK, ToCytoscapeJSON(pageNodes, pageEdges))
}

// handleSearch handles GET /api/graph/search?q=<query>&limit=<n>&kind=<type>
// kind optionally restricts results to one node type (variable, function, …).
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
	kind := r.URL.Query().Get("kind")

	// Over-fetch when filtering so a sparse type still fills the limit.
	fetchLimit := limit
	if kind != "" {
		fetchLimit = limit * 10
	}
	nodes, err := s.db.SearchNodes(r.Context(), q, fetchLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kind != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if string(n.Type) == kind {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
		if len(nodes) > limit {
			nodes = nodes[:limit]
		}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// flowRef is one endpoint in a variable's flow summary — deliberately tiny
// so agents can pull a variable's full story in a few hundred tokens.
type flowRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// handleVariableFlow handles GET /api/variable/{id}/flow — who declares,
// reads, writes, captures, and receives this variable.
func (s *Server) handleVariableFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing variable id")
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	node, ok := idx.Nodes[id]
	if !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if node.Type != graph.NodeTypeVariable {
		writeError(w, http.StatusBadRequest, "node is not a variable")
		return
	}

	ref := func(nodeID string, meta map[string]string) *flowRef {
		n, ok := idx.Nodes[nodeID]
		if !ok {
			return nil
		}
		return &flowRef{ID: n.ID, Label: n.Label, File: n.File, Line: n.Line, Meta: meta}
	}

	var readers, writers, capturedBy, flows []flowRef
	for _, e := range idx.InEdges[id] {
		switch e.Type {
		case graph.EdgeTypeReads:
			if fr := ref(e.From, nil); fr != nil {
				readers = append(readers, *fr)
			}
		case graph.EdgeTypeWrites:
			if fr := ref(e.From, e.Meta); fr != nil {
				writers = append(writers, *fr)
			}
		case graph.EdgeTypeCaptures:
			if fr := ref(e.From, e.Meta); fr != nil {
				capturedBy = append(capturedBy, *fr)
			}
		}
	}
	for _, e := range idx.OutEdges[id] {
		if e.Type == graph.EdgeTypeFlowsTo {
			if fr := ref(e.To, e.Meta); fr != nil {
				flows = append(flows, *fr)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"variable":    node,
		"readers":     readers,
		"writers":     writers,
		"captured_by": capturedBy,
		"flows_to":    flows,
	})
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

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	if _, ok := idx.Nodes[root]; !ok {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}

	nodes, edges := traceSubgraph(idx, root, direction, depth)
	writeJSON(w, http.StatusOK, ToCytoscapeJSON(nodes, edges))
}

// handleFiles handles GET /api/files?q=<substr>&limit=<n>
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{"files": graph.ListFiles(idx, q, limit)})
}

// fileNodeRef is the token-frugal node shape returned by file endpoints.
type fileNodeRef struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Line  int    `json:"line"`
}

// handleFile handles GET /api/file?service=<name>&path=<path>
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'path'")
		return
	}
	service := r.URL.Query().Get("service")

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	nodes := graph.NodesInFile(idx, service, path)
	if len(nodes) == 0 {
		writeError(w, http.StatusNotFound, "file not found in index")
		return
	}

	refs := make([]fileNodeRef, 0, len(nodes))
	for _, n := range nodes {
		refs = append(refs, fileNodeRef{ID: n.ID, Type: string(n.Type), Label: n.Label, Line: n.Line})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file":    nodes[0].File,
		"service": nodes[0].Service,
		"nodes":   refs,
	})
}

// handleFileImpact handles
// GET /api/file/impact?service=<name>&path=<path>&direction=<forward|backward|both>&depth=<n>
func (s *Server) handleFileImpact(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter 'path'")
		return
	}
	service := r.URL.Query().Get("service")
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

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	seeds := graph.NodesInFile(idx, service, path)
	if len(seeds) == 0 {
		writeError(w, http.StatusNotFound, "file not found in index")
		return
	}
	entries := graph.FileImpact(idx, service, path, direction, depth)
	if entries == nil {
		entries = []graph.FileImpactEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file":      seeds[0].File,
		"service":   seeds[0].Service,
		"direction": direction,
		"depth":     depth,
		"impacted":  entries,
	})
}

// handleStats handles GET /api/stats
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	nodes, edges, err := s.db.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var semanticWarnings []string
	if raw, err := s.db.GetMeta(r.Context(), "semantic_warnings"); err == nil && raw != "" && raw != "[]" {
		_ = json.Unmarshal([]byte(raw), &semanticWarnings)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":            nodes,
		"edges":            edges,
		"semantic_warnings": semanticWarnings,
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

	ch := make(chan string, 8)
	s.clientsMu.Lock()
	s.clients[ch] = struct{}{}
	s.clientsMu.Unlock()

	defer func() {
		s.clientsMu.Lock()
		delete(s.clients, ch)
		s.clientsMu.Unlock()
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
