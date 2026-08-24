package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// fleetServiceStatus is one row of GET /api/fleet/status — FR.7's sketch,
// each service's own services/<name>/graph.db meta table read independently
// of the merged fleet DB's single last_indexed timestamp (mirrors the CLI's
// perServiceLastIndexed in cmd/polyflow/main.go, FR.6).
type fleetServiceStatus struct {
	Service   string `json:"service"`
	IndexedAt string `json:"indexed_at,omitempty"`
	NodeCount int    `json:"node_count"`
	EdgeCount int    `json:"edge_count"`
	Indexed   bool   `json:"indexed"`
}

// handleFleetStatus handles GET /api/fleet/status — per-service staleness,
// sourced from each services/<name>/graph.db this workspace has produced via
// `polyflow index <service>` (FR.2). A service with no per-service DB on
// disk still gets a row (Indexed:false) rather than being silently omitted,
// since the UI needs to distinguish "never indexed on its own" from "no
// services configured".
func (s *Server) handleFleetStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := workspace.Load(s.configPathOrDefault())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load workspace config: "+err.Error())
		return
	}

	s.idxMu.RLock()
	dbPath := s.dbPath
	s.idxMu.RUnlock()
	dbDir := meta.DBDir
	if dbPath != "" {
		dbDir = filepath.Dir(dbPath)
	}

	rows := make([]fleetServiceStatus, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		row := fleetServiceStatus{Service: svc.Name}
		svcDBPath := filepath.Join(dbDir, "services", svc.Name, meta.DBFile)
		if _, statErr := os.Stat(svcDBPath); statErr != nil {
			rows = append(rows, row)
			continue
		}
		row.Indexed = true

		store, openErr := graph.NewSQLiteStore(svcDBPath)
		if openErr != nil {
			rows = append(rows, row)
			continue
		}
		if nodes, edges, statsErr := store.Stats(r.Context()); statsErr == nil {
			row.NodeCount = nodes
			row.EdgeCount = edges
		}
		if raw, metaErr := store.GetMeta(r.Context(), "last_indexed"); metaErr == nil && raw != "" {
			if unix, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
				row.IndexedAt = time.Unix(unix, 0).UTC().Format(time.RFC3339)
			}
		}
		store.Close()
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{"services": rows})
}

// fleetMemberRow is one row of GET /api/fleet/services (Tier GR.6) — the
// git-backed registry's membership list, distinct from handleFleetStatus's
// FR.7-era services/<name>/graph.db model above. Unlike that endpoint this
// list includes every fleet member regardless of whether this machine has
// resolved it locally yet — selecting one is what triggers the clone.
type fleetMemberRow struct {
	Service string `json:"service"`
	Active  bool   `json:"active"`
}

// handleFleetServices handles GET /api/fleet/services. An empty list (not
// an error) means this workspace isn't a registered Tier-GR fleet member —
// SetFleet was never called.
func (s *Server) handleFleetServices(w http.ResponseWriter, r *http.Request) {
	s.idxMu.RLock()
	members := s.fleetMembers
	active := s.fleetActive
	s.idxMu.RUnlock()

	rows := make([]fleetMemberRow, 0, len(members))
	for _, name := range members {
		rows = append(rows, fleetMemberRow{Service: name, Active: name == active})
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": rows})
}

// fleetActiveRequest is POST /api/fleet/active's body.
type fleetActiveRequest struct {
	Service string `json:"service"`
}

// handleFleetActive handles POST /api/fleet/active — swaps which fleet
// member's own store backs db/idx/searcher (GR.6), via the FleetSwitchFunc
// cmd/polyflow wired in at startup. The old store is left open (the
// switcher owns and caches per-member stores across switches, so a repeat
// selection is free) rather than closed here.
func (s *Server) handleFleetActive(w http.ResponseWriter, r *http.Request) {
	var req fleetActiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" {
		writeError(w, http.StatusBadRequest, "missing service")
		return
	}

	s.idxMu.RLock()
	switchFn := s.fleetSwitch
	s.idxMu.RUnlock()
	if switchFn == nil {
		writeError(w, http.StatusServiceUnavailable, "this workspace is not a registered fleet member")
		return
	}

	store, idx, searcher, root, err := switchFn(r.Context(), req.Service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "switch fleet member: "+err.Error())
		return
	}

	s.idxMu.Lock()
	s.db = store
	s.idx = idx
	s.searcher = searcher
	s.sourceRoot = root
	s.fleetActive = req.Service
	s.idxMu.Unlock()

	s.Broadcast(`{"type":"graph_updated"}`)
	writeJSON(w, http.StatusOK, map[string]any{"active": req.Service})
}

// getNodeWithFallback returns id's node from the live SQLite store first,
// falling back to the in-memory idx. A bridge-merged cross-service node
// (GR.2/GR.3's buildFleetAwareIndex) is only ever recorded in idx — the
// bridge is unioned into it directly, never written into any member's own
// graph.db — so a plain s.db.GetNode 404s for any node reached via a
// cross-service edge, on the active member or not.
func (s *Server) getNodeWithFallback(ctx context.Context, id string) (*graph.Node, bool) {
	if node, err := s.db.GetNode(ctx, id); err == nil {
		return node, true
	}
	s.idxMu.RLock()
	defer s.idxMu.RUnlock()
	n, ok := s.idx.Nodes[id]
	return n, ok
}

// edgesWithFallback mirrors getNodeWithFallback for id's edges: the SQLite
// store's own edges first, falling back to idx's adjacency lists when the
// store has none (a bridge-merged node's edges live only in idx too).
func (s *Server) edgesWithFallback(ctx context.Context, id string) (from, to []*graph.Edge) {
	from, _ = s.db.ListEdgesFrom(ctx, id)
	to, _ = s.db.ListEdgesTo(ctx, id)
	if len(from) == 0 && len(to) == 0 {
		s.idxMu.RLock()
		defer s.idxMu.RUnlock()
		from = s.idx.OutEdges[id]
		to = s.idx.InEdges[id]
	}
	return from, to
}

// resolveSourcePath joins file with the active fleet member's checkout root
// (sourceRoot, set by handleFleetActive/SetFleet) when file is relative and
// a root is known. An absolute file, or an empty sourceRoot (the workspace
// `serve` started in — CWD-relative, unchanged from pre-GR.6 behavior), is
// returned as-is.
func (s *Server) resolveSourcePath(file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	s.idxMu.RLock()
	root := s.sourceRoot
	s.idxMu.RUnlock()
	if root == "" {
		return file
	}
	return filepath.Join(root, file)
}
