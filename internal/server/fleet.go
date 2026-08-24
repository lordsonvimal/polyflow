package server

import (
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
