package eval

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/meta"
)

// RunOptions configures an eval run.
type RunOptions struct {
	CorpusDir string // directory containing manifest.yaml
	CaseID    string // if non-empty, run only this case
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
	if m.Repo.Path != "" && m.Repo.Path != "." {
		repoRoot = m.Repo.Path
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
