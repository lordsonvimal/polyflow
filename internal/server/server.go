package server

import (
	"fmt"
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Server is the polyflow HTTP API and UI server.
type Server struct {
	db      graph.Store
	idx     *graph.AdjacencyIndex
	mux     *http.ServeMux
	devMode bool
}

// New creates a Server backed by the given store and adjacency index.
func New(db graph.Store, idx *graph.AdjacencyIndex) *Server {
	s := &Server{db: db, idx: idx, mux: http.NewServeMux()}
	s.registerRoutes()
	return s
}

// NewDev creates a Server with CORS enabled for Vite dev (port 5173).
func NewDev(db graph.Store, idx *graph.AdjacencyIndex) *Server {
	s := &Server{db: db, idx: idx, mux: http.NewServeMux(), devMode: true}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/graph", s.handleGraph)
	s.mux.HandleFunc("GET /api/graph/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/graph/trace", s.handleTrace)
	s.mux.HandleFunc("GET /api/node/{id}", s.handleNode)
	s.mux.HandleFunc("GET /api/node/{id}/source", s.handleNodeSource)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	// Serve the built SolidJS frontend
	s.mux.Handle("/", http.FileServer(http.Dir("web/dist")))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.devMode {
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:5173")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
	}
	s.mux.ServeHTTP(w, r)
}

// Start begins listening on the given port (127.0.0.1 only by default).
func (s *Server) Start(port int) error {
	return s.StartOn("127.0.0.1", port)
}

// StartOn listens on an explicit host:port. Use "0.0.0.0" for LAN exposure.
func (s *Server) StartOn(host string, port int) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	fmt.Printf("polyflow server listening on http://localhost:%d\n", port)
	return http.ListenAndServe(addr, s)
}
