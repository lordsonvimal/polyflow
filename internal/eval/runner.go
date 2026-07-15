package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/meta"
)

// RunOptions configures an eval run.
type RunOptions struct {
	CorpusDir string // directory containing manifest.yaml
	CaseID    string // if non-empty, run only this case
	// CachePath overrides the derived eval/.cache/<name> path for URL repos.
	CachePath string
}

// MultiReport holds scored reports for all corpus repos in one run.
type MultiReport struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Reports     []Report         `json:"repos"`
	Skipped     []SkippedCorpus  `json:"skipped,omitempty"`
}

// SkippedCorpus records a corpus that was unavailable (not cloned/indexed).
type SkippedCorpus struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
}

// RunAll finds all corpus dirs under root and runs each in sequence.
// If a corpus DB is not available (repo not cloned or not indexed) it is
// recorded in Skipped with the reason — the eval never silently passes.
func RunAll(ctx context.Context, root string) (*MultiReport, error) {
	dirs, err := FindCorpusDirs(root)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no corpus directories (with manifest.yaml) found under %s", root)
	}
	out := &MultiReport{GeneratedAt: time.Now().UTC()}
	for _, dir := range dirs {
		r, err := Run(ctx, RunOptions{CorpusDir: dir})
		if err != nil {
			// Surface unavailable repos as explicit skips (never silent).
			m, mErr := LoadManifest(dir)
			name := dir
			if mErr == nil {
				name = m.Repo.Name
			}
			out.Skipped = append(out.Skipped, SkippedCorpus{
				Name:   name,
				Dir:    dir,
				Reason: err.Error(),
			})
			continue
		}
		out.Reports = append(out.Reports, *r)
	}
	return out, nil
}

// cachePath returns the local path where a URL repo's clone lives.
// Convention: eval/.cache/<repo-name>
func cachePath(name string) string {
	return filepath.Join("eval", ".cache", name)
}

// Run loads a corpus manifest, executes each case against the graph, and
// returns a scored Report.
//
// The graph DB is opened from <manifest.repo.path>/.polyflow/graph.db
// (or the current directory when repo.path is empty or ".").
func Run(ctx context.Context, opts RunOptions) (*Report, error) {
	m, err := LoadManifest(opts.CorpusDir)
	if err != nil {
		return nil, err
	}

	repoRoot := "."
	switch {
	case opts.CachePath != "":
		repoRoot = opts.CachePath
	case m.Repo.Path != "" && m.Repo.Path != ".":
		repoRoot = m.Repo.Path
	case m.Repo.URL != "":
		// URL repo: expect it to be cloned by `make eval-corpus` first.
		repoRoot = cachePath(m.Repo.Name)
	}

	dbPath := filepath.Join(repoRoot, meta.DBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open graph DB at %s (run `polyflow index` first): %w", dbPath, err)
	}
	defer store.Close()

	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("build graph index: %w", err)
	}

	unresolvedRefs, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unresolved refs: %w", err)
	}
	unresolvedFileSet := make(map[string]bool, len(unresolvedRefs))
	for _, u := range unresolvedRefs {
		unresolvedFileSet[u.File] = true
	}

	var results []CaseResult
	for _, c := range m.Cases {
		if opts.CaseID != "" && c.ID != opts.CaseID {
			continue
		}
		cr, err := runCase(ctx, store, idx, unresolvedFileSet, c)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		results = append(results, cr)
	}

	report := AggregateReport(m.Repo.Name, results)
	return &report, nil
}

func runCase(ctx context.Context, store *graph.SQLiteStore, idx *graph.AdjacencyIndex, unresolvedFiles map[string]bool, c Case) (CaseResult, error) {
	var returned []string
	switch c.Kind {
	case "node":
		nodes, err := store.SearchNodes(ctx, c.Target, 5)
		if err != nil || len(nodes) == 0 {
			return CaseResult{}, fmt.Errorf("node not found for target %q", c.Target)
		}
		out := impact.Build(idx, nodes[0], 10, "")
		returned = nodeImpactFiles(out)
	case "file":
		out, err := impact.BuildFile(idx, "", c.Target, "backward", 10)
		if err != nil {
			return CaseResult{}, fmt.Errorf("file impact: %w", err)
		}
		returned = fileImpactFiles(out)
	case "diff":
		// Diff cases require E.2 corpus infrastructure (clone + patch apply).
		return CaseResult{}, fmt.Errorf("diff cases not supported until Phase E.2")
	default:
		return CaseResult{}, fmt.Errorf("unknown case kind %q", c.Kind)
	}
	return Score(c.ID, returned, c.ExpectedImpacted, c.MustNotMiss, unresolvedFiles), nil
}

// nodeImpactFiles collects unique file paths from a node-level impact result.
func nodeImpactFiles(r *impact.Result) []string {
	seen := make(map[string]bool)
	var files []string
	push := func(f string) {
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	if r.Target != nil {
		push(r.Target.File)
	}
	for _, c := range r.Callers {
		push(c.File)
	}
	return files
}

// fileImpactFiles collects impacted file paths from a file-level impact result.
func fileImpactFiles(r *impact.FileResult) []string {
	seen := make(map[string]bool)
	var files []string
	for _, e := range r.Impacted {
		if !seen[e.File] {
			seen[e.File] = true
			files = append(files, e.File)
		}
	}
	return files
}
