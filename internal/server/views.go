package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

// handleListViews handles GET /api/views (UO.5 saved views).
func (s *Server) handleListViews(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "saved views are not available")
		return
	}
	views, err := s.ops.ListViews(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": views})
}

type createViewBody struct {
	Name  string `json:"name"`
	State string `json:"state"` // opaque encoded ViewState JSON
}

// handleCreateView handles POST /api/views body {"name": "...", "state": "..."}.
func (s *Server) handleCreateView(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "saved views are not available")
		return
	}

	var body createViewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "name is required",
			"field": "name",
		})
		return
	}

	view, err := s.ops.CreateView(r.Context(), body.Name, body.State)
	if err != nil {
		if errors.Is(err, ops.ErrViewNameConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"view": view})
}

type renameViewBody struct {
	Name string `json:"name"`
}

// handleRenameView handles PATCH /api/views/{id} body {"name": "..."}.
func (s *Server) handleRenameView(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "saved views are not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid view id")
		return
	}
	var body renameViewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "name is required",
			"field": "name",
		})
		return
	}

	view, err := s.ops.RenameView(r.Context(), id, body.Name)
	if err != nil {
		switch {
		case errors.Is(err, ops.ErrViewNotFound):
			writeError(w, http.StatusNotFound, "view not found")
		case errors.Is(err, ops.ErrViewNameConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"view": view})
}

// handleDeleteView handles DELETE /api/views/{id}.
func (s *Server) handleDeleteView(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "saved views are not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid view id")
		return
	}
	if err := s.ops.DeleteView(r.Context(), id); err != nil {
		if errors.Is(err, ops.ErrViewNotFound) {
			writeError(w, http.StatusNotFound, "view not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
