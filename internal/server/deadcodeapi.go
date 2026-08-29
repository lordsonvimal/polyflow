package server

import (
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
)

// handleDeadcode handles GET /api/deadcode?service=&file=
func (s *Server) handleDeadcode(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	file := r.URL.Query().Get("file")

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	unresolved, err := s.db.ListUnresolvedRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, deadcode.Build(idx, deadcode.Options{Service: service, File: file, UnresolvedRefs: unresolved}))
}
