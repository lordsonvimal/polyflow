package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/capture"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jobs"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	webui "github.com/lordsonvimal/polyflow/web"
)

// Server is the polyflow HTTP API and UI server.
type Server struct {
	db         graph.Store
	idx        *graph.AdjacencyIndex
	idxMu      sync.RWMutex
	searcher   *semantic.Searcher // nil → FTS-only fallback
	mux        *http.ServeMux
	devMode    bool
	broadcast  chan string
	clients    map[chan string]struct{}
	clientsMu  sync.Mutex
	ops        *ops.Store       // nil → tool-call audit logging disabled (UB.2)
	jobs       *jobs.Manager    // nil → jobs API disabled (UB.3)
	configPath string           // polyflow.yml path; "" → meta.ConfigFile (UB.4)
	capture    *capture.Manager // nil → capture/runtime API disabled (UB.7)
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

// SetOps wires the tool-call audit store into the server. Safe to call at
// any time; nil disables audit logging (handlers behave exactly as before
// UB.2). Call before serving traffic to avoid a race with in-flight requests.
func (s *Server) SetOps(o *ops.Store) {
	s.idxMu.Lock()
	s.ops = o
	s.idxMu.Unlock()
}

// SetJobs wires the UB.3 job manager into the server. Safe to call at any
// time; nil disables the jobs API (handlers return 503).
func (s *Server) SetJobs(j *jobs.Manager) {
	s.idxMu.Lock()
	s.jobs = j
	s.idxMu.Unlock()
}

// SetConfigPath wires the polyflow.yml path the UB.4 config API reads and
// writes. Safe to call at any time; an unset path falls back to
// meta.ConfigFile (relative to the process's working directory), matching
// every other CLI/server entry point's default.
func (s *Server) SetConfigPath(path string) {
	s.idxMu.Lock()
	s.configPath = path
	s.idxMu.Unlock()
}

// SetCapture wires the UB.7 capture session manager into the server. Safe
// to call at any time; nil disables the capture/runtime API (handlers
// return 503). The same on-disk session store (.polyflow/captures) is
// shared with the CLI's `capture`/`ingest`/`flows` subcommands, so a
// session started by either surface is visible and stoppable from the
// other.
func (s *Server) SetCapture(c *capture.Manager) {
	s.idxMu.Lock()
	s.capture = c
	s.idxMu.Unlock()
}

// Broadcast pushes a raw SSE message to every connected /api/events client,
// non-blocking (mirroring Reload's fan-out). Exported so internal/jobs
// (constructed and owned by the CLI's serve command, outside this package)
// can push job_progress/job_done events without this package depending on
// internal/jobs for anything but the *jobs.Manager type.
func (s *Server) Broadcast(msg string) {
	select {
	case s.broadcast <- msg:
	default:
	}
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
	// s.handle wraps every /api/* route (except /api/events and the static
	// SPA below) with UB.2 tool-call audit recording; /api/events is
	// excluded per the plan (an SSE stream, not a request/response call).
	s.handle("GET /api/graph", s.handleGraph)
	s.handle("GET /api/graph/search", s.handleSearch)
	s.handle("GET /api/graph/trace", s.handleTrace)
	s.handle("GET /api/node/{id}", s.handleNode)
	s.handle("GET /api/tree", s.handleTree)
	s.handle("GET /api/variable/{id}/flow", s.handleVariableFlow)
	s.handle("GET /api/node/{id}/source", s.handleNodeSource)
	s.handle("GET /api/files", s.handleFiles)
	s.handle("GET /api/file", s.handleFile)
	s.handle("GET /api/file/impact", s.handleFileImpact)
	s.handle("GET /api/impact/diff", s.handleImpactDiff)
	s.handle("GET /api/scope", s.handleScope)
	s.handle("GET /api/stats", s.handleStats)
	s.handle("GET /api/export/mermaid", s.handleExportMermaid)
	s.handle("GET /api/toolcalls", s.handleListToolCalls)
	s.handle("DELETE /api/toolcalls", s.handleDeleteToolCalls)
	s.handle("GET /api/settings", s.handleGetSettings)
	s.handle("PUT /api/settings", s.handlePutSettings)
	s.handle("POST /api/jobs", s.handleCreateJob)
	s.handle("GET /api/jobs", s.handleListJobs)
	s.handle("GET /api/jobs/{id}", s.handleGetJob)
	s.handle("DELETE /api/jobs/{id}", s.handleCancelJob)
	s.handle("GET /api/config", s.handleGetConfig)
	s.handle("PUT /api/config", s.handlePutConfig)
	s.handle("GET /api/flows/entrypoints", s.handleFlowsEntrypoints)
	s.handle("GET /api/flows/through/{id}", s.handleFlowsThrough)
	s.handle("GET /api/flows/paths", s.handleFlowsPaths)
	s.handle("GET /api/flows/refine", s.handleFlowsRefine)
	s.handle("GET /api/node/{id}/links", s.handleNodeLinks)
	s.handle("GET /api/seam/{id}", s.handleSeam)
	s.handle("GET /api/services/channels", s.handleServiceChannels)
	s.handle("GET /api/stack", s.handleStack)
	s.handle("GET /api/health", s.handleHealth)
	s.handle("GET /api/docs/cli", s.handleDocsCLI)
	s.handle("GET /api/unresolved", s.handleUnresolved)
	s.handle("POST /api/context/bundle", s.handleContextBundle)
	s.handle("POST /api/capture/start", s.handleCaptureStart)
	s.handle("POST /api/capture/stop", s.handleCaptureStop)
	s.handle("GET /api/capture/status", s.handleCaptureStatus)
	s.handle("POST /api/capture/ingest", s.handleCaptureIngest)
	s.handle("GET /api/runtime/flows", s.handleRuntimeFlows)
	s.handle("GET /api/runtime/coverage", s.handleRuntimeCoverage)
	s.handle("GET /api/reconcile/propose", s.handleReconcilePropose)
	s.handle("GET /api/views", s.handleListViews)
	s.handle("POST /api/views", s.handleCreateView)
	s.handle("PATCH /api/views/{id}", s.handleRenameView)
	s.handle("DELETE /api/views/{id}", s.handleDeleteView)
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
