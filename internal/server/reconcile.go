package server

import (
	"net/http"

	"github.com/lordsonvimal/polyflow/internal/evidence"
)

// handleReconcilePropose handles GET /api/reconcile/propose?kind=&key=&from=&to=
// — a read-only preview of the contract-rule YAML that `polyflow reconcile
// --propose-dir` would write for one observed_only_gap channel (Runtime
// coverage view's "propose contract rule" action). Nothing is written to
// disk; the operator copies the YAML out and saves it themselves.
func (s *Server) handleReconcilePropose(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	key := r.URL.Query().Get("key")
	if kind == "" || key == "" {
		writeError(w, http.StatusBadRequest, "kind and key are required")
		return
	}

	gap := evidence.EdgeSummary{
		Kind: kind,
		Key:  key,
		From: r.URL.Query().Get("from"),
		To:   r.URL.Query().Get("to"),
	}

	proposals := evidence.ProposeRules([]evidence.EdgeSummary{gap})
	if len(proposals) == 0 {
		writeError(w, http.StatusInternalServerError, "no proposal generated")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filename": proposals[0].Filename,
		"content":  proposals[0].Content,
	})
}
