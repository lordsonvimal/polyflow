package server

import (
	"fmt"
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Server is the polyflow HTTP API and UI server.
type Server struct {
	db  graph.Store
	idx *graph.AdjacencyIndex
	mux *http.ServeMux
}

// New creates a Server backed by the given store and adjacency index.
func New(db graph.Store, idx *graph.AdjacencyIndex) *Server {
	s := &Server{db: db, idx: idx, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/node/{id}", s.handleNode)
	s.mux.HandleFunc("GET /api/trace", s.handleTrace)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	// Serve the built SolidJS frontend
	s.mux.Handle("/", http.FileServer(http.Dir("web/dist")))
}

// Start begins listening on the given port (127.0.0.1 only by default).
func (s *Server) Start(port int) error {
	return s.StartOn("127.0.0.1", port)
}

// StartOn listens on an explicit host:port. Use "0.0.0.0" for LAN exposure.
func (s *Server) StartOn(host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	fmt.Printf("polyflow server listening on http://localhost:%d\n", port)
	return http.ListenAndServe(addr, s.mux)
}
