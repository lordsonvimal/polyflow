package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// handleStack handles GET /api/stack.
func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	deps, err := s.db.ListDependencies(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": graph.BuildStack(idx, deps)})
}

// evalHealth is the eval section of GET /api/health.
type evalHealth struct {
	Present bool           `json:"present"`
	Repos   []evalRepoInfo `json:"repos,omitempty"`
}

type evalRepoInfo struct {
	Name   string  `json:"name"`
	Recall float64 `json:"recall"`
}

// loadEvalHealth reads eval/baseline.json relative to the workspace root (the
// polyflow.yml directory) when present. Absence is a valid, expected state —
// surfaced as Present:false, never implied away (rule 4).
func (s *Server) loadEvalHealth() evalHealth {
	root := filepath.Dir(s.configPathOrDefault())
	raw, err := os.ReadFile(filepath.Join(root, "eval", "baseline.json"))
	if err != nil {
		return evalHealth{Present: false}
	}
	var report eval.MultiReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return evalHealth{Present: false}
	}
	repos := make([]evalRepoInfo, 0, len(report.Reports))
	for _, rep := range report.Reports {
		repos = append(repos, evalRepoInfo{Name: rep.Repo, Recall: rep.Recall})
	}
	return evalHealth{Present: true, Repos: repos}
}

// handleHealth handles GET /api/health.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	nodes, edges, err := s.db.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	schemaVersion, _ := s.db.GetMeta(ctx, "schema_version")

	indexedAt := ""
	if raw, err := s.db.GetMeta(ctx, "last_indexed"); err == nil && raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			indexedAt = time.Unix(unix, 0).UTC().Format(time.RFC3339)
		}
	}

	parseErrors, err := s.db.ListParseErrors(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	unresolved, err := s.db.ListUnresolvedRefs(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	trust, _ := graph.LoadTrustStamp(ctx, s.db)

	writeJSON(w, http.StatusOK, map[string]any{
		"index": map[string]any{
			"indexed_at":     indexedAt,
			"schema_version": schemaVersion,
			"nodes":          nodes,
			"edges":          edges,
			"parse_errors":   len(parseErrors),
		},
		"coverage":         graph.BuildVerificationSummary(idx.AllEdges()),
		"unresolved_total": len(unresolved),
		"eval":             s.loadEvalHealth(),
		"trust":            trust,
	})
}

// handleUnresolved handles GET /api/unresolved?service=&kind=&q=&page=&limit=
func (s *Server) handleUnresolved(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	all, err := s.db.ListUnresolvedRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filtered := graph.FilterUnresolvedRefs(all, q.Get("service"), q.Get("kind"), q.Get("q"))

	total := len(filtered)
	start := (page - 1) * limit
	var pageItems []graph.UnresolvedRef
	if start < total {
		end := start + limit
		if end > total {
			end = total
		}
		pageItems = filtered[start:end]
	}
	if pageItems == nil {
		pageItems = []graph.UnresolvedRef{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"refs":  pageItems,
		"total": total,
		"page":  page,
	})
}
