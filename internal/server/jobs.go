package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/jobs"
	"github.com/lordsonvimal/polyflow/internal/ops"
)

type createJobBody struct {
	Kind string          `json:"kind"`
	Args json.RawMessage `json:"args"`
}

// handleCreateJob handles POST /api/jobs body {"kind": "index", "args": {...}}.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs are not available")
		return
	}

	var body createJobBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	job, err := s.jobs.Start(body.Kind, body.Args)
	if err != nil {
		var unknownKind jobs.ErrUnknownKind
		var evalDirMissing jobs.ErrEvalDirMissing
		var conflict jobs.ErrConflict
		switch {
		case errors.As(err, &unknownKind):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.As(err, &evalDirMissing):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": err.Error(),
				"path":  evalDirMissing.Path,
			})
		case errors.As(err, &conflict):
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": err.Error(),
				"job":   conflict.Running,
			})
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// handleListJobs handles GET /api/jobs?limit=.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs are not available")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.jobs.List(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []ops.Job{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

// handleGetJob handles GET /api/jobs/{id}.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs are not available")
		return
	}
	job, err := s.jobs.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ops.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// handleGetJobProfile handles GET /api/jobs/{id}/profile — streams the raw
// pprof-format CPU profile captured for this job (UO.8). Reads straight from
// ops.db rather than through jobs.Manager: profile bytes only exist once a
// job has finished (Manager.run's terminal UpsertJob), so there's nothing
// Manager's in-memory running-job view could add here.
func (s *Server) handleGetJobProfile(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs are not available")
		return
	}
	id := r.PathValue("id")
	data, err := s.ops.GetJobProfile(r.Context(), id)
	if err != nil {
		if errors.Is(err, ops.ErrProfileNotFound) {
			writeError(w, http.StatusNotFound, "no CPU profile captured for this job")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="job-%s.pprof"`, id))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleCancelJob handles DELETE /api/jobs/{id} — requests cancellation via
// the job's context.Context; the job transitions to "canceled" asynchronously
// once the underlying work observes it (see internal/jobs).
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeError(w, http.StatusServiceUnavailable, "jobs are not available")
		return
	}
	job, err := s.jobs.Cancel(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ops.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
