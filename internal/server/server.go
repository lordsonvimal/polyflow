package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	webui "github.com/lordsonvimal/polyflow/web"
)

// Server is the polyflow HTTP API and UI server.
type Server struct {
	db        graph.Store
	idx       *graph.AdjacencyIndex
	idxMu     sync.RWMutex
	searcher  *semantic.Searcher // nil → FTS-only fallback
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

// SetSearcher wires a hybrid Searcher into the server. Safe to call at any time.
func (s *Server) SetSearcher(sr *semantic.Searcher) {
	s.idxMu.Lock()
	s.searcher = sr
	s.idxMu.Unlock()
}

// Reload swaps the adjacency index and broadcasts a graph_updated SSE event.
// Also invalidates the vector matrix cache when a Searcher is wired.
func (s *Server) Reload(idx *graph.AdjacencyIndex) {
	s.idxMu.Lock()
	s.idx = idx
	sr := s.searcher
	s.idxMu.Unlock()
	if sr != nil {
		sr.Invalidate()
	}
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
	// Serve the built SolidJS frontend from the embedded FS so `serve` works
	// from any working directory (not just the source-tree root).
	dist, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		// Only fails if the embed directive changed; fall back to 404 rather
		// than panicking so the API stays available.
		s.mux.Handle("/", http.NotFoundHandler())
		return
	}
	s.mux.Handle("/", http.FileServer(http.FS(dist)))
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
