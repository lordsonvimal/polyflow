package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/lordsonvimal/polyflow/internal/registry"
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

// handleSetupRegistry handles GET /api/setup/registry (UO.8): a read-only
// list of this machine's known local workspaces (internal/registry, GR.1/
// GR.3), so the setup wizard can offer "open one of these" as an
// alternative to discovering a brand-new workspace under some path — most
// useful right when needs_config is true because the server started
// outside any of them (e.g. a parent directory of several independently
// fleet-configured repos). Entries whose LocalPath no longer exists on disk
// are dropped rather than shown as a dead link.
func (s *Server) handleSetupRegistry(w http.ResponseWriter, r *http.Request) {
	regPath, err := registry.DefaultPath()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load registry: "+err.Error())
		return
	}

	type entry struct {
		Service   string   `json:"service"`
		LocalPath string   `json:"local_path"`
		IndexedAt string   `json:"indexed_at,omitempty"`
		Fleets    []string `json:"fleets,omitempty"`
	}
	entries := make([]entry, 0, len(reg.Entries))
	for _, e := range reg.Entries {
		if e.LocalPath == "" {
			continue
		}
		if _, statErr := os.Stat(e.LocalPath); statErr != nil {
			continue
		}
		indexed := ""
		if !e.IndexedAt.IsZero() {
			indexed = e.IndexedAt.Format("2006-01-02T15:04:05Z07:00")
		}
		entries = append(entries, entry{Service: e.Service, LocalPath: e.LocalPath, IndexedAt: indexed, Fleets: e.Fleets})
	}

	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleSetupSelect handles POST /api/setup/select (UO.8), body:
// {"path": "<a registry entry's local_path>"}. Delegates to
// Server.selectWorkspace (wired by `polyflow serve`, see
// SelectWorkspaceFunc for why this restarts the process rather than
// re-initializing in place) and reports 501 when nothing is wired, 400 for
// a path that isn't actually a directory this machine has.
func (s *Server) handleSetupSelect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	info, err := os.Stat(body.Path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "not a local directory: "+body.Path)
		return
	}

	s.idxMu.RLock()
	selectFn := s.selectWorkspace
	s.idxMu.RUnlock()
	if selectFn == nil {
		writeError(w, http.StatusNotImplemented, "workspace switching is not available in this mode")
		return
	}
	if err := selectFn(body.Path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
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
