package server

import (
	"context"
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
	dbPath     string           // graph.db path; used only by GET /api/setup/status (UO.7)

	// selectWorkspace implements POST /api/setup/select (UO.8): given a
	// known registry entry's local path, re-point this running server at
	// that workspace. nil → 501 (only wired by `polyflow serve`, which owns
	// the process-restart mechanics; e.g. NewDev's Vite-CORS test harness
	// leaves it unset).
	selectWorkspace SelectWorkspaceFunc

	// Fleet mode (Tier GR.6, revised): idx is the union of every locally-
	// resolved fleet member's own full graph plus the fleet's bridge.db
	// cross-service edges (not just the workspace `serve` started in) —
	// browsing, search, impact, trace, and context all see the whole fleet
	// by default, no "active member" switch required. fleetMerge is nil
	// when this workspace isn't a registered fleet member.
	fleetMerge     FleetMergeFunc
	fleetEnsure    FleetEnsureFunc
	fleetMembers   []string          // every member of the fleet, not just locally-resolved ones
	fleetResolved  map[string]bool   // member -> currently merged into idx
	fleetRoots     map[string]string // service -> checkout root, for relative node.File resolution
	fleetSearchers map[string]*semantic.Searcher
}

// SelectWorkspaceFunc re-points the running `polyflow serve` process at a
// different local workspace (UO.8). Every subsystem this server wires up
// (store, index, searcher, jobs, capture, fleet merge, the graph.db
// fsnotify watcher) is initialized once at startup keyed to the process's
// working directory (meta.DBDir/meta.ConfigFile are relative paths by
// design — see `polyflow index`'s own cwd-relative behavior), so
// re-initializing all of it in place would mean duplicating `runServe`'s
// entire wiring sequence and carefully tearing down the old one (open
// SQLite handles, the embedder's sidecar process, the fsnotify goroutine)
// without racing in-flight requests. Restarting the process is what
// `runServe` already does correctly by construction — SelectWorkspaceFunc
// hands off to a fresh `polyflow serve` on the same host:port and exits
// this one, so the OS reclaims every resource instead of this package
// re-deriving that cleanup by hand.
type SelectWorkspaceFunc func(localPath string) error

// SetSelectWorkspace wires POST /api/setup/select's implementation (UO.8).
// Safe to call at any time; a nil fn (the default) makes that endpoint
// report 501, e.g. under NewDev's Vite-dev harness which has no process to
// restart.
func (s *Server) SetSelectWorkspace(fn SelectWorkspaceFunc) {
	s.idxMu.Lock()
	s.selectWorkspace = fn
	s.idxMu.Unlock()
}

// FleetMergeFunc (re)builds the full-fleet view from scratch: opens every
// locally-resolved member's own store (registry.Load, GR.1), unions their
// nodes/edges into one idx along with the fleet's bridge.db, opens one
// Searcher per resolved member for federated search (GR.3's
// semantic.FederatedSearch), and reports which internal service name maps
// to which member's checkout root (for relative node.File resolution) and
// which top-level fleet members ended up resolved. Constructed and owned by
// cmd/polyflow (which has fleetsync/queryresolve/the embedder), not this
// package. Called once at startup and again after FleetEnsureFunc resolves
// a new member, so the merge always reflects the registry's current state
// rather than being maintained incrementally.
type FleetMergeFunc func(ctx context.Context) (idx *graph.AdjacencyIndex, roots map[string]string, searchers map[string]*semantic.Searcher, resolved []string, err error)

// FleetEnsureFunc resolves the named fleet member onto this machine —
// cloning it via GR.1's resolver if not already local — without itself
// rebuilding the merge; the caller (handleFleetActive) calls FleetMergeFunc
// again afterward so the merge picks it up.
type FleetEnsureFunc func(ctx context.Context, service string) error

// SetFleet wires fleet-wide browsing into the server (GR.6, revised).
// members is the full fleet membership list (including members never
// resolved on this machine yet — POST /api/fleet/active's ensure step
// triggers an on-demand clone for one of those). Safe to call at any time;
// a nil mergeFn disables the fleet endpoints (they report "not a fleet
// member" rather than 500).
func (s *Server) SetFleet(mergeFn FleetMergeFunc, ensureFn FleetEnsureFunc, members []string) {
	s.idxMu.Lock()
	s.fleetMerge = mergeFn
	s.fleetEnsure = ensureFn
	s.fleetMembers = members
	s.idxMu.Unlock()
}

// RefreshFleet re-runs FleetMergeFunc and swaps in the result — idx,
// per-service checkout roots, and federated searchers all update together
// so a request never sees roots/searchers out of sync with idx. Called once
// at `serve` startup (the default full-fleet view) and again by
// handleFleetActive after ensuring a new member is locally resolved. A nil
// fleetMerge (not a fleet member) is a no-op, not an error.
func (s *Server) RefreshFleet(ctx context.Context) error {
	s.idxMu.RLock()
	mergeFn := s.fleetMerge
	s.idxMu.RUnlock()
	if mergeFn == nil {
		return nil
	}
	idx, roots, searchers, resolved, err := mergeFn(ctx)
	if err != nil {
		return err
	}
	s.idxMu.Lock()
	s.idx = idx
	s.fleetRoots = roots
	s.fleetSearchers = searchers
	s.fleetResolved = make(map[string]bool, len(resolved))
	for _, svc := range resolved {
		s.fleetResolved[svc] = true
	}
	s.idxMu.Unlock()
	return nil
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

// SetDBPath records the graph.db path GET /api/setup/status stats to
// report whether the workspace has been indexed yet. Safe to call at any
// time.
func (s *Server) SetDBPath(path string) {
	s.idxMu.Lock()
	s.dbPath = path
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
	s.handle("GET /api/deadcode", s.handleDeadcode)
	s.handle("GET /api/scope", s.handleScope)
	s.handle("GET /api/stats", s.handleStats)
	s.handle("GET /api/export/mermaid", s.handleExportMermaid)
	s.handle("GET /api/toolcalls", s.handleListToolCalls)
	s.handle("DELETE /api/toolcalls", s.handleDeleteToolCalls)
	s.handle("GET /api/toolcalls/{id}/profile", s.handleGetToolCallProfile)
	s.handle("GET /api/settings", s.handleGetSettings)
	s.handle("PUT /api/settings", s.handlePutSettings)
	s.handle("POST /api/jobs", s.handleCreateJob)
	s.handle("GET /api/jobs", s.handleListJobs)
	s.handle("GET /api/jobs/{id}", s.handleGetJob)
	s.handle("GET /api/jobs/{id}/profile", s.handleGetJobProfile)
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
	s.handle("GET /api/patterns", s.handleListPatterns)
	s.handle("POST /api/patterns", s.handleAddPattern)
	s.handle("GET /api/fleet/status", s.handleFleetStatus)
	s.handle("GET /api/fleet/services", s.handleFleetServices)
	s.handle("POST /api/fleet/active", s.handleFleetActive)
	s.handle("GET /api/setup/status", s.handleSetupStatus)
	s.handle("GET /api/setup/registry", s.handleSetupRegistry)
	s.handle("POST /api/setup/select", s.handleSetupSelect)
	s.handle("POST /api/setup/apply", s.handleSetupApply)
	s.handle("GET /api/setup/agents", s.handleSetupAgents)
	s.handle("POST /api/setup/agent", s.handleSetupAgentApply)
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
