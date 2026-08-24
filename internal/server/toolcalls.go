package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

// handleListToolCalls handles
// GET /api/toolcalls?source=&tool=&status=&q=&since=&page=&limit=
func (s *Server) handleListToolCalls(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "tool-call audit log is not available")
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))

	result, err := s.ops.ListCalls(r.Context(), ops.ListFilter{
		Source: q.Get("source"),
		Tool:   q.Get("tool"),
		Status: q.Get("status"),
		Q:      q.Get("q"),
		Since:  q.Get("since"),
		Page:   page,
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"calls": result.Calls,
		"total": result.Total,
		"page":  result.Page,
	})
}

// handleGetToolCallProfile handles GET /api/toolcalls/{id}/profile — streams
// the raw pprof-format CPU profile captured for this call (UO.8), for
// opening in `go tool pprof` or the browser's pprof flamegraph viewer.
func (s *Server) handleGetToolCallProfile(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "tool-call audit log is not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	data, err := s.ops.GetToolCallProfile(r.Context(), id)
	if err != nil {
		if errors.Is(err, ops.ErrProfileNotFound) {
			writeError(w, http.StatusNotFound, "no CPU profile captured for this call")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="toolcall-%d.pprof"`, id))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleDeleteToolCalls handles DELETE /api/toolcalls (the UI's "clear all logs").
func (s *Server) handleDeleteToolCalls(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "tool-call audit log is not available")
		return
	}
	n, err := s.ops.DeleteAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

// handleGetSettings handles GET /api/settings. Ops/UI settings are stored in
// ops.db, never polyflow.yml (UB.4's /api/config is the separate workspace
// config path).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "ops settings are not available")
		return
	}
	retention, err := s.ops.GetRetention(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"tool_call_retention": retention})
}

type putSettingsBody struct {
	ToolCallRetention int `json:"tool_call_retention"`
}

// handlePutSettings handles PUT /api/settings body {"tool_call_retention": N}.
// Validates MinRetention <= N <= MaxRetention (422 naming the field on
// failure) and, if the new value is lower than the current row count, trims
// tool_calls immediately.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "ops settings are not available")
		return
	}

	var body putSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "invalid JSON body: " + err.Error(),
			"field": "tool_call_retention",
		})
		return
	}
	if body.ToolCallRetention < ops.MinRetention || body.ToolCallRetention > ops.MaxRetention {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "tool_call_retention must be an integer between 1 and 10000",
			"field": "tool_call_retention",
		})
		return
	}

	evicted, err := s.ops.SetRetention(r.Context(), body.ToolCallRetention)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(evicted) > 0 {
		s.broadcastToolCallEvicted(evicted)
	}
	writeJSON(w, http.StatusOK, map[string]int{"tool_call_retention": body.ToolCallRetention})
}
