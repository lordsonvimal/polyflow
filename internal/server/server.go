package server

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Server is the polyflow HTTP API and UI server.
type Server struct {
	db        graph.Store
	idx       *graph.AdjacencyIndex
	idxMu     sync.RWMutex
	mux       *http.ServeMux
	devMode   bool
	broadcast chan string
	clients   map[chan string]struct{}
	clientsMu sync.Mutex
}

// New creates a Server backed by the given store and adjacency index.
func New(db graph.Store, idx *graph.AdjacencyIndex) *Server {
	s := &Server{
		db:        db,
		idx:       idx,
		mux:       http.NewServeMux(),
		broadcast: make(chan string, 16),
		clients:   make(map[chan string]struct{}),
	}
	s.registerRoutes()
	go s.fanOut()
	return s
}

// NewDev creates a Server with CORS enabled for Vite dev (port 5173).
func NewDev(db graph.Store, idx *graph.AdjacencyIndex) *Server {
	s := &Server{
		db:        db,
		idx:       idx,
		mux:       http.NewServeMux(),
		devMode:   true,
		broadcast: make(chan string, 16),
		clients:   make(map[chan string]struct{}),
	}
	s.registerRoutes()
	go s.fanOut()
	return s
}

// Reload swaps the adjacency index and broadcasts a graph_updated SSE event.
func (s *Server) Reload(idx *graph.AdjacencyIndex) {
	s.idxMu.Lock()
	s.idx = idx
	s.idxMu.Unlock()
	select {
	case s.broadcast <- `{"type":"graph_updated"}`:
	default:
	}
}

func (s *Server) fanOut() {
	for msg := range s.broadcast {
		s.clientsMu.Lock()
		for ch := range s.clients {
			select {
			case ch <- msg:
			default:
			}
		}
		s.clientsMu.Unlock()
	}
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/graph", s.handleGraph)
	s.mux.HandleFunc("GET /api/graph/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/graph/trace", s.handleTrace)
	s.mux.HandleFunc("GET /api/node/{id}", s.handleNode)
	s.mux.HandleFunc("GET /api/variable/{id}/flow", s.handleVariableFlow)
	s.mux.HandleFunc("GET /api/node/{id}/source", s.handleNodeSource)
	s.mux.HandleFunc("GET /api/files", s.handleFiles)
	s.mux.HandleFunc("GET /api/file", s.handleFile)
	s.mux.HandleFunc("GET /api/file/impact", s.handleFileImpact)
	s.mux.HandleFunc("GET /api/stats", s.handleStats)
	s.mux.HandleFunc("GET /api/export/mermaid", s.handleExportMermaid)
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
