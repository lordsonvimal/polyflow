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
// resolved it locally yet. Active means "currently merged into idx" —
// every locally-resolved member is active simultaneously (GR.6, revised:
// the whole fleet is browsable by default, not one member at a time) —
// POST /api/fleet/active on an unresolved one is what triggers its clone.
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
	resolved := s.fleetResolved
	s.idxMu.RUnlock()

	rows := make([]fleetMemberRow, 0, len(members))
	for _, name := range members {
		rows = append(rows, fleetMemberRow{Service: name, Active: resolved[name]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": rows})
}

// fleetActiveRequest is POST /api/fleet/active's body.
type fleetActiveRequest struct {
	Service string `json:"service"`
}

// handleFleetActive handles POST /api/fleet/active — ensures the named
// fleet member is resolved on this machine (cloning it via GR.1's resolver
// if not already local), then re-runs the full-fleet merge (RefreshFleet)
// so idx/roots/searchers pick it up. Every previously-resolved member stays
// merged in too — this widens the fleet-wide view, it doesn't switch away
// from anything.
func (s *Server) handleFleetActive(w http.ResponseWriter, r *http.Request) {
	var req fleetActiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Service == "" {
		writeError(w, http.StatusBadRequest, "missing service")
		return
	}

	s.idxMu.RLock()
	ensureFn := s.fleetEnsure
	s.idxMu.RUnlock()
	if ensureFn == nil {
		writeError(w, http.StatusServiceUnavailable, "this workspace is not a registered fleet member")
		return
	}

	if err := ensureFn(r.Context(), req.Service); err != nil {
		writeError(w, http.StatusInternalServerError, "resolve fleet member: "+err.Error())
		return
	}
	if err := s.RefreshFleet(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "refresh fleet merge: "+err.Error())
		return
	}

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

// resolveSourcePath joins file with the checkout root of whichever fleet
// member owns service (fleetRoots, built fresh on every RefreshFleet) when
// file is relative and a root is known for that service. An absolute file,
// or a service with no known root (a plain non-fleet workspace — every
// node.File there is already CWD-relative, unchanged from pre-GR.6
// behavior), is returned as-is.
func (s *Server) resolveSourcePath(service, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	s.idxMu.RLock()
	root := s.fleetRoots[service]
	s.idxMu.RUnlock()
	if root == "" {
		return file
	}
	return filepath.Join(root, file)
}
