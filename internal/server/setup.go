package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// handleSetupStatus handles GET /api/setup/status — the UO.7 setup-mode
// gate. It stats configPath/dbPath live rather than tracking a cached
// "setup mode" flag on Server, so it can never drift from what's actually
// on disk: a `polyflow index` run from a second terminal flips
// needs_index false the moment the fsnotify watcher's next status poll
// lands, same as every other cross-surface freshness guarantee in this
// server.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	configPath := s.configPathOrDefault()
	_, configErr := os.Stat(configPath)
	needsConfig := configErr != nil

	s.idxMu.RLock()
	dbPath := s.dbPath
	s.idxMu.RUnlock()
	needsIndex := true
	if dbPath != "" {
		if _, err := os.Stat(dbPath); err == nil {
			needsIndex = false
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"needs_config": needsConfig,
		"needs_index":  needsIndex,
		"config_path":  configPath,
		"db_path":      dbPath,
	})
}

// handleSetupApply handles POST /api/setup/apply, body: a workspace.Config
// JSON object (the shape returned by GET /api/jobs/{id} for a completed
// "init" discovery job's result, possibly edited by the user). It writes
// polyflow.yml via workspace.SaveInit — the exact function `polyflow init`
// calls — so setup-mode and the CLI produce byte-identical files.
func (s *Server) handleSetupApply(w http.ResponseWriter, r *http.Request) {
	var cfg workspace.WorkspaceConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(cfg.Services) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "at least one service is required",
			"field": "services",
		})
		return
	}

	configPath := s.configPathOrDefault()
	if err := workspace.SaveInit(configPath, &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": configPath, "ok": true})
}
