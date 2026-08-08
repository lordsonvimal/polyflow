package eval

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/semantic"
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
	GeneratedAt time.Time       `json:"generated_at"`
	Reports     []Report        `json:"repos"`
	Skipped     []SkippedCorpus `json:"skipped,omitempty"`
	Broken      []BrokenCorpus  `json:"broken,omitempty"`
}

// ErrCorpusUnavailable marks the one failure that is a legitimate skip: the
// corpus DB could not be opened because the repo was never cloned or indexed.
// Every other failure means the corpus is present but broken, which must reach
// the gate rather than be filed away as an absence.
var ErrCorpusUnavailable = errors.New("corpus unavailable")

// BrokenCorpus records a corpus that was available but failed to run — an
// unreadable manifest, an index that would not build, or a case that could not
// be executed (e.g. its target file is no longer in the index).
//
// This is deliberately not a SkippedCorpus. A skip is exempt from the gate for
// local-only repos, and routing real defects through that exemption is how the
// fleet corpus sat dead for weeks while `eval --gate` still exited 0.
type BrokenCorpus struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
}

// SkippedCorpus records a corpus that was unavailable (not cloned/indexed).
type SkippedCorpus struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Reason string `json:"reason"`
	// LocalOnly marks a path-based repo (manifest has path:, no url:) — one
	// that only exists on the author's machine (e.g. a private clone). The
	// gate's missing_repo condition exempts these: CI cannot clone them, so
	// their absence is an expected skip, not a broken pipeline. URL repos
	// get no such exemption — a failed clone/index must fail the gate.
	LocalOnly bool `json:"local_only,omitempty"`
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
			m, mErr := LoadManifest(dir)
			name := dir
			localOnly := false
			if mErr == nil {
				name = m.Repo.Name
				localOnly = m.Repo.URL == "" && m.Repo.Path != ""
			}
			// Only an absent corpus is a skip. A corpus that is present but
			// fails to run is a defect and must reach the gate.
			if !errors.Is(err, ErrCorpusUnavailable) {
				out.Broken = append(out.Broken, BrokenCorpus{
					Name:   name,
					Dir:    dir,
					Reason: err.Error(),
				})
				continue
			}
			out.Skipped = append(out.Skipped, SkippedCorpus{
				Name:      name,
				Dir:       dir,
				Reason:    err.Error(),
				LocalOnly: localOnly,
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
		return nil, fmt.Errorf("%w: open graph DB at %s (run `polyflow index` first): %v",
			ErrCorpusUnavailable, dbPath, err)
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
		cr, err := runCase(ctx, store, idx, unresolvedFileSet, c, newPathCanon(repoRoot))
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		results = append(results, cr)
	}

	report := AggregateReport(m.Repo.Name, results)
	return &report, nil
}

// pinCandidateByFile selects the declaration a node case meant, from the
// ambiguous set ResolveTarget reports when a label is shared.
//
// Without it a case like `target: index` in the lobsters corpus resolved to
// whichever of 20 same-named controller actions sorted first — measuring
// CabinetController#index for a case written about SearchController#index, and
// scoring recall 0.000 forever. Neither `service` nor `node_type` can separate
// them: they are all `function` nodes in one service.
//
// An unmatched or ambiguous suffix is an error rather than a silent fallback to
// root; a corpus case that no longer names a real declaration must fail loudly,
// since its whole purpose is to be a fixed reference point.
func pinCandidateByFile(ctx context.Context, store *graph.SQLiteStore, root *graph.Node, candidates []graph.TargetCandidate, wantFile string) (*graph.Node, error) {
	// ResolveTarget leaves candidates empty when the label was unambiguous; the
	// single match still has to satisfy the pin.
	if len(candidates) == 0 {
		if root != nil && strings.HasSuffix(filepath.ToSlash(root.File), wantFile) {
			return root, nil
		}
		return nil, fmt.Errorf("sole match is %s", rootFile(root))
	}
	var matched []graph.TargetCandidate
	for _, cand := range candidates {
		if strings.HasSuffix(filepath.ToSlash(cand.File), wantFile) {
			matched = append(matched, cand)
		}
	}
	switch len(matched) {
	case 1:
		return store.GetNode(ctx, matched[0].ID)
	case 0:
		return nil, fmt.Errorf("no candidate declared there (%d candidates)", len(candidates))
	default:
		return nil, fmt.Errorf("%d candidates match that suffix — make it more specific", len(matched))
	}
}

func rootFile(n *graph.Node) string {
	if n == nil {
		return "<none>"
	}
	return n.File
}

func runCase(ctx context.Context, store *graph.SQLiteStore, idx *graph.AdjacencyIndex, unresolvedFiles map[string]bool, c Case, pc *pathCanon) (CaseResult, error) {
	var returned []string
	switch c.Kind {
	case "node":
		root, candidates, err := graph.ResolveTarget(ctx, store, c.Target, c.Service, c.NodeType)
		if err != nil {
			return CaseResult{}, fmt.Errorf("node not found for target %q: %w", c.Target, err)
		}
		if c.TargetFile != "" {
			root, err = pinCandidateByFile(ctx, store, root, candidates, c.TargetFile)
			if err != nil {
				return CaseResult{}, fmt.Errorf("target %q in %q: %w", c.Target, c.TargetFile, err)
			}
		}
		out := impact.Build(idx, root, 10, "", false, 0)
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
	case "semantic":
		// Semantic cases score entity labels, not file paths — canonicalising
		// them would be meaningless.
		return runSemanticCase(ctx, store, c)
	default:
		return CaseResult{}, fmt.Errorf("unknown case kind %q", c.Kind)
	}
	// Impact cases compare file paths, so put both sides — and the unresolved
	// ledger that decides honest-vs-silent — into one coordinate system first.
	return Score(c.ID,
		pc.keys(returned),
		pc.keys(c.ExpectedImpacted),
		pc.keys(c.MustNotMiss),
		pc.keySet(unresolvedFiles),
	), nil
}

// runSemanticCase executes a kind=semantic eval case (S.4): NL query →
// top-10 entity labels in the requested section, scored against expect_any_of.
// Gracefully handles DBs without entities_fts (pre-S.1 schema) by scoring
// as 0 results (honest miss) rather than returning an error.
func runSemanticCase(ctx context.Context, store *graph.SQLiteStore, c Case) (CaseResult, error) {
	semStore := semantic.NewStore(store.DB())
	embedder, _ := semantic.DefaultStaticEmbedder()
	searcher := semantic.NewSearcher(semStore, embedder, nil)

	resp, err := searcher.Search(ctx, c.Query, 10)
	if err != nil {
		// DB lacks entities_fts (pre-S.1 schema) or other search failure.
		// Score as 0 results — honest miss, not an eval run error.
		cr := Score(c.ID, nil, c.ExpectAnyOf, c.MustNotMiss, nil)
		cr.Kind = "semantic"
		return cr, nil
	}

	var hits []semantic.Hit
	switch c.Section {
	case "nodes":
		hits = resp.Nodes
	case "flows":
		hits = resp.Flows
	case "docs":
		hits = resp.Docs
	default:
		return CaseResult{}, fmt.Errorf("unknown section %q in semantic case %s", c.Section, c.ID)
	}

	returned := make([]string, 0, len(hits))
	for _, h := range hits {
		returned = append(returned, semanticHitLabel(h, c.Section))
	}
	cr := Score(c.ID, returned, c.ExpectAnyOf, c.MustNotMiss, nil)
	cr.Kind = "semantic"
	return cr, nil
}

// semanticHitLabel extracts a stable, line-number-free identifier from a
// search hit. For nodes and flows this is the name component of the entity ID
// (format: service:file:type:name:line → parts[3]). For docs it is the file path.
func semanticHitLabel(hit semantic.Hit, section string) string {
	switch section {
	case "nodes":
		return nodeIDLabel(hit.Entity.ID)
	case "flows":
		return nodeIDLabel(hit.Entity.NodeID)
	case "docs":
		return hit.Entity.File
	}
	return hit.Entity.ID
}

// nodeIDLabel extracts the name component from a node ID string of the form
// service:file:type:name:line. Returns the raw ID if the format is unexpected.
func nodeIDLabel(nodeID string) string {
	parts := strings.SplitN(nodeID, ":", 5)
	if len(parts) >= 4 {
		return parts[3]
	}
	if len(parts) >= 1 {
		return parts[len(parts)-1]
	}
	return nodeID
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
