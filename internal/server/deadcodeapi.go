package server

import (
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
)

// handleDeadcode handles GET /api/deadcode?service=&file=&transitive=&include_types=
func (s *Server) handleDeadcode(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	file := r.URL.Query().Get("file")
	transitive := r.URL.Query().Get("transitive") == "true" || r.URL.Query().Get("transitive") == "1"
	includeTypes := r.URL.Query().Get("include_types") == "true" || r.URL.Query().Get("include_types") == "1"

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	unresolved, err := s.UnresolvedRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, deadcode.Build(idx, deadcode.Options{
		Service:        service,
		File:           file,
		Transitive:     transitive,
		IncludeTypes:   includeTypes,
		UnresolvedRefs: unresolved,
	}))
}
