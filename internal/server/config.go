package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// configPathOrDefault returns the polyflow.yml path the config API reads and
// writes: the SetConfigPath value, or meta.ConfigFile (relative to the
// process's working directory) when unset.
func (s *Server) configPathOrDefault() string {
	s.idxMu.RLock()
	p := s.configPath
	s.idxMu.RUnlock()
	if p == "" {
		return meta.ConfigFile
	}
	return p
}

// configEtag hashes raw config bytes for the UB.4 optimistic-concurrency
// contract: a client's PUT etag must match the file's current content.
func configEtag(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// handleGetConfig handles GET /api/config.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	path := s.configPathOrDefault()
	raw, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "read config: "+err.Error())
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// A config that fails to parse still round-trips its raw text (the
	// editor needs to show and fix it); "parsed" is simply omitted.
	var parsed *workspace.WorkspaceConfig
	if cfg, err := workspace.Load(path); err == nil {
		parsed = cfg
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":   absPath,
		"raw":    string(raw),
		"parsed": parsed,
		"etag":   configEtag(raw),
	})
}

type putConfigBody struct {
	Raw  string `json:"raw"`
	ETag string `json:"etag"`
}

// handlePutConfig handles PUT /api/config body {"raw": "<yaml>", "etag": "<from GET>"}.
//
// PUT always takes and writes raw YAML text, never a re-marshaled struct:
// the UI's form mode edits the parsed structure client-side and PUTs full
// raw YAML back, so comments and formatting the form doesn't understand
// round-trip untouched. The server's only job is validate-then-write.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var body putConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	path := s.configPathOrDefault()
	current, err := os.ReadFile(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "read config: "+err.Error())
		return
	}
	currentEtag := configEtag(current)
	if body.ETag != currentEtag {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":        "config changed on disk",
			"current_etag": currentEtag,
		})
		return
	}

	// Validate against a temp file in the same directory as the real config
	// so relative service paths resolve exactly as they will after the
	// write (workspace.Load resolves relative paths against the config
	// file's own directory, not the process CWD).
	tmp, err := os.CreateTemp(filepath.Dir(path), ".polyflow-config-validate-*.yml")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create validation temp file: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(body.Raw); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, "write validation temp file: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "close validation temp file: "+err.Error())
		return
	}
	if _, err := workspace.Load(tmpPath); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	rename := path + ".tmp"
	if err := os.WriteFile(rename, []byte(body.Raw), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}
	if err := os.Rename(rename, path); err != nil {
		writeError(w, http.StatusInternalServerError, "rename config: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"etag": configEtag([]byte(body.Raw)),
		"ok":   true,
	})
}
