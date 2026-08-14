package server

import (
	"net/http"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// handleImpactDiff handles
// GET /api/impact/diff?staged=<bool>&depth=<n>&service=<name> — a thin
// wrapper over `polyflow impact --diff`'s internals (internal/impact.
// BuildDiff), for UF.6's canvas Diff tab. Unlike the CLI, this never
// reindexes: the server's live index (kept fresh by the file watcher) is
// diffed as-is, so the endpoint stays cheap enough to call on every tab
// open. unmapped_hunks (including no_git_repo services) is always present
// in the response — never dropped (docs/phases.md rule 12).
func (s *Server) handleImpactDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	staged := q.Get("staged") == "true"
	service := q.Get("service")
	depth, _ := strconv.Atoi(q.Get("depth"))

	cfg, err := workspace.Load(s.configPathOrDefault())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load config: "+err.Error())
		return
	}

	svcDirs := make([]gitdiff.ServiceDir, len(cfg.Services))
	for i, svc := range cfg.Services {
		svcDirs[i] = gitdiff.ServiceDir{Name: svc.Name, Path: svc.Path}
	}
	roots := gitdiff.ResolveRoots(svcDirs)
	changes, err := gitdiff.MultiChanges(roots, staged)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "git diff: "+err.Error())
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	out := impact.BuildDiff(idx, changes, impact.Options{
		Depth:      depth,
		Service:    service,
		Policy:     graph.BlastRadiusPolicy(),
		StaleAfter: cfg.Evidence.StaleAfterDuration(),
	})
	out.AppendNoGitRepo(roots)
	if staged {
		out.Mode = "staged"
	}

	unresolved, err := s.db.ListUnresolvedRefs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.AttachUnresolved(unresolved)

	writeJSON(w, http.StatusOK, out)
}
