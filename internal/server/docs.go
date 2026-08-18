package server

import (
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// handleDocsCLI handles GET /api/docs/cli — a generated CLI reference (UO.4).
// The command tree is set once at startup by cmd/polyflow (meta.SetCLIDocs,
// walked from the live cobra rootCmd), so this can never go stale relative
// to the actual binary; it is a single source of truth, not hand-maintained
// docs (per docs/plan-13-ui-ops.md UO.4).
func (s *Server) handleDocsCLI(w http.ResponseWriter, r *http.Request) {
	cmds := meta.CLIDocs()
	if cmds == nil {
		cmds = []meta.CLICommand{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds})
}
