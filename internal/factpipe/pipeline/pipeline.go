// Package pipeline is FX.6 — pipeline orchestration for the declarative
// framework layer (Tier FX, docs/declarative-framework-pipeline-plan.md). It
// pairs each embedded `rules/<lang>/<name>.dl` with
// `patterns/<lang>/<name>.yaml` into a Framework, gates the set to the
// frameworks a service actually depends on, and runs the four stages
// (extract → bridge → derive → emit) for one service's files.
//
// It is a subpackage of internal/factpipe rather than `internal/factpipe/
// pipeline.go` (as the plan's pinned path reads) because internal/patterns
// already imports internal/factpipe (FX.2's extract verbs), so the
// orchestrator — which needs both — cannot live in internal/factpipe itself.
//
// Deviations from the plan's pinned interface, forced by the existing tree:
//
//   - `Load(fsys fs.FS)` becomes `Load(ruleFS, patternFS fs.FS)` — rules and
//     patterns are two separate embed trees.
//   - `Active(m deps.Manifest)` takes `[]deps.Dependency` — the codebase's
//     resolved-dependency type; there is no `deps.Manifest`.
//   - `Framework.Patterns` holds the parsed `*patterns.PatternFile` (there is
//     no `patterns.CompiledPattern`); `Framework.Rules` is `*datalog.Program`
//     as pinned.
//   - `Result.Ledger` is `[]graph.UnresolvedRef` — what factpipe emit produces;
//     there is no `graph.LedgerRow`.
//
// Pipeline slot (see docs/architecture.md): Run executes after
// language-semantic analysis — FX.1's bridge needs `calls_edge` / `resolved` /
// `inherits` populated — and before cross-service contract matching, whose L4
// edges may read the framework edges Run produces.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/rubyast"
	patterndata "github.com/lordsonvimal/polyflow/patterns"
	"github.com/lordsonvimal/polyflow/rules"
)

var (
	embeddedOnce sync.Once
	embeddedReg  *Registry
	embeddedErr  error
)

// LoadEmbedded pairs the rule + pattern trees compiled into the binary. This is
// the production entry point; Load takes explicit filesystems for tests.
//
// The embedded rule+pattern trees never change at runtime, so the parse +
// datalog-compile work Load does is memoized process-wide (FX.8.PERF,
// docs/declarative-framework-pipeline-plan.md): internal/indexer calls this
// once per single-framework pass per service — 21 call sites at the time this
// cache was added — and without it every one of those re-parsed and
// re-compiled all ~30 frameworks from scratch.
func LoadEmbedded() (*Registry, error) {
	embeddedOnce.Do(func() {
		embeddedReg, embeddedErr = Load(rules.FS, patterndata.FS)
	})
	return embeddedReg, embeddedErr
}

// Gate is a framework's activation condition: the service must depend on
// Package, and (when set) its resolved version must satisfy VersionRange
// (Masterminds semver syntax). An empty Package means "always active".
type Gate struct {
	Package      string
	VersionRange string
}

func (g Gate) active(versions map[string]string) bool {
	if g.Package == "" {
		return true
	}
	v, ok := versions[g.Package]
	if !ok {
		return false
	}
	if g.VersionRange == "" {
		return true
	}
	return patterns.VersionInRange(v, g.VersionRange)
}

// Framework is one declarative framework pass: its tree-sitter patterns (with
// `facts:` blocks), its compiled datalog program, and its `emit:` specs. Built
// once by Load, shared read-only across every service Run.
type Framework struct {
	Name     string
	Language string
	Gate     Gate
	Patterns *patterns.PatternFile
	Rules    *datalog.Program
	Emits    []factpipe.CompiledEmit
	Resolves []factpipe.CompiledResolve
	Configs  []factpipe.CompiledConfig
	Tables   []factpipe.CompiledTable
	Hubs     []factpipe.CompiledHub
	Derives  []factpipe.CompiledDerive

	goals   []string                    // the emit relations, materialized by Eval
	matcher *patterns.TreeSitterMatcher // built once by loadFramework (FX.8.PERF); TreeSitterMatcher is mutex-guarded, safe to share across concurrent Run calls
}

// Registry is the full set of frameworks compiled from the embedded rule +
// pattern trees.
type Registry struct {
	frameworks []*Framework
}

// Load pairs every `<lang>/<name>.dl` in ruleFS with `<lang>/<name>.yaml` in
// patternFS. A `.dl` with no matching YAML, a YAML that does not parse, a YAML
// with no `emit:` block, or a rule set that does not compile is a hard error —
// a framework the binary cannot assemble degrades to "this relation is empty",
// indistinguishable from a codebase that genuinely has no such construct.
func Load(ruleFS, patternFS fs.FS) (*Registry, error) {
	var dlPaths []string
	err := fs.WalkDir(ruleFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".dl") {
			dlPaths = append(dlPaths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dlPaths)

	reg := &Registry{}
	for _, dlPath := range dlPaths {
		name := strings.TrimSuffix(path.Base(dlPath), ".dl")
		yamlPath := path.Join(path.Dir(dlPath), name+".yaml")
		if _, err := fs.Stat(patternFS, yamlPath); errors.Is(err, fs.ErrNotExist) {
			// A `.dl` with no paired pattern YAML is not a pipeline framework
			// (e.g. rules/ruby/rails_filters.dl, consumed directly by
			// internal/linker until FX.8 gives it a YAML). Skip it — only a
			// YAML that declares a framework but cannot be assembled is fatal.
			continue
		}
		fw, err := loadFramework(ruleFS, patternFS, dlPath)
		if err != nil {
			return nil, err
		}
		reg.frameworks = append(reg.frameworks, fw)
	}
	return reg, nil
}

func loadFramework(ruleFS, patternFS fs.FS, dlPath string) (*Framework, error) {
	name := strings.TrimSuffix(path.Base(dlPath), ".dl")
	langDir := path.Dir(dlPath)
	yamlPath := path.Join(langDir, name+".yaml")

	ydata, err := fs.ReadFile(patternFS, yamlPath)
	if err != nil {
		return nil, fmt.Errorf("factpipe: rule %s has no paired pattern %s: %w", dlPath, yamlPath, err)
	}
	var pf patterns.PatternFile
	if err := yaml.Unmarshal(ydata, &pf); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var doc struct {
		Emit []factpipe.EmitSpec `yaml:"emit"`
	}
	if err := yaml.Unmarshal(ydata, &doc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s emit: %w", yamlPath, err)
	}
	if len(doc.Emit) == 0 {
		return nil, fmt.Errorf("factpipe: pattern %s has no emit: block", yamlPath)
	}
	emits, err := factpipe.CompileEmitSpecs(doc.Emit)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var rdoc struct {
		Resolve []factpipe.ResolveSpec `yaml:"resolve"`
	}
	if err := yaml.Unmarshal(ydata, &rdoc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s resolve: %w", yamlPath, err)
	}
	resolves, err := factpipe.CompileResolveSpecs(rdoc.Resolve)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var cdoc struct {
		Config []factpipe.ConfigSpec `yaml:"config"`
	}
	if err := yaml.Unmarshal(ydata, &cdoc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s config: %w", yamlPath, err)
	}
	configs, err := factpipe.CompileConfigSpecs(cdoc.Config)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var tdoc struct {
		Table []factpipe.TableSpec `yaml:"table"`
	}
	if err := yaml.Unmarshal(ydata, &tdoc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s table: %w", yamlPath, err)
	}
	tables, err := factpipe.CompileTableSpecs(tdoc.Table)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var hdoc struct {
		Hub []factpipe.HubSpec `yaml:"hub"`
	}
	if err := yaml.Unmarshal(ydata, &hdoc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s hub: %w", yamlPath, err)
	}
	hubs, err := factpipe.CompileHubSpecs(hdoc.Hub)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	var ddoc struct {
		Derive []factpipe.DeriveSpec `yaml:"derive"`
	}
	if err := yaml.Unmarshal(ydata, &ddoc); err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s derive: %w", yamlPath, err)
	}
	derives, err := factpipe.CompileDeriveSpecs(ddoc.Derive)
	if err != nil {
		return nil, fmt.Errorf("factpipe: pattern %s: %w", yamlPath, err)
	}

	rdata, err := fs.ReadFile(ruleFS, dlPath)
	if err != nil {
		return nil, err
	}
	prog, err := datalog.Compile(rdata, dlPath)
	if err != nil {
		return nil, fmt.Errorf("factpipe: rule %s: %w", dlPath, err)
	}

	goals := make([]string, 0, len(emits))
	seen := map[string]bool{}
	for _, e := range emits {
		if !seen[e.Relation()] {
			seen[e.Relation()] = true
			goals = append(goals, e.Relation())
		}
	}

	lang := pf.Language
	if lang == "" {
		lang = path.Base(langDir)
	}

	reg := patterns.NewRegistry()
	reg.RegisterFile(&pf)
	matcher := patterns.NewTreeSitterMatcher(reg)

	return &Framework{
		Name:     name,
		Language: lang,
		Gate:     Gate{Package: pf.Package, VersionRange: pf.VersionRange},
		Patterns: &pf,
		Rules:    prog,
		Emits:    emits,
		Resolves: resolves,
		Configs:  configs,
		Tables:   tables,
		Hubs:     hubs,
		Derives:  derives,
		goals:    goals,
		matcher:  matcher,
	}, nil
}

// All returns every registered framework, gate ignored — for introspection.
func (r *Registry) All() []*Framework {
	return append([]*Framework(nil), r.frameworks...)
}

// ByName returns the named framework, gate ignored — for a caller that runs
// one specific framework directly rather than through Active's dependency
// gate (FX.8.15's ruby_job_inherit, which the retired Go pass ran
// unconditionally for every ruby service, no gem marker required).
func (r *Registry) ByName(name string) *Framework {
	for _, fw := range r.frameworks {
		if fw.Name == name {
			return fw
		}
	}
	return nil
}

// Active returns the frameworks whose gate is satisfied by svcDeps. A gated-out
// framework is never compiled into a run — cost is O(active), not O(all).
func (r *Registry) Active(svcDeps []deps.Dependency) []*Framework {
	versions := make(map[string]string, len(svcDeps))
	for _, d := range svcDeps {
		versions[d.Name] = d.Version
	}
	var out []*Framework
	for _, fw := range r.frameworks {
		if fw.Gate.active(versions) {
			out = append(out, fw)
		}
	}
	return out
}

// ParsedFile is one already-read source file handed to Run.
type ParsedFile struct {
	Path     string
	Language string // pattern language (e.g. "ruby", "go")
	Grammar  string // tree-sitter grammar; defaults to Language
	Src      []byte
}

// Result is everything the active frameworks produced for one service.
type Result struct {
	Edges      []graph.Edge
	Nodes      []graph.Node
	Unresolved []graph.UnresolvedRef
	Ledger     []graph.UnresolvedRef
	// Replaced/Deleted are FX.8.15's node-mutation channels (`replace:`/
	// `delete:` emit blocks) — a `mint:`-only framework never populates
	// either. Replaced maps an old node ID to the new node that supersedes
	// it (also present in Nodes); Deleted names node IDs to drop outright.
	// The caller performs the actual graph swap/delete.
	Replaced map[string]string
	Deleted  []string
	// Patches is FX.8.8's node-mutation channel (`patch:` emit blocks) — meta
	// overlays for existing nodes the caller merges in (not a rebuild). See
	// factpipe.NodePatch.
	Patches []factpipe.NodePatch
	// Resolved is FX.8.10's `resolved:` channel — "service\x00name" pairs the
	// caller uses to retract matching ledger rows an earlier pass recorded.
	Resolved []string
}

// RunStats optionally captures one Run call's per-phase wall time (XM.0,
// docs/factpipe-cross-framework-matching-plan.md) — bridge is the one-time
// GraphFacts cost, Derive/Emit are summed across every active framework's
// loop iteration. Extract is the one-time shared tree-sitter matching cost
// (XM.1's matchAll, now hoisted out of the per-framework loop) plus each
// framework's much cheaper per-framework fact-lowering step summed on top —
// still "total extract-phase cost," just no longer dominated by redundant
// per-framework query execution. nil (the default, via Run's variadic stats
// param) skips every time.Now() call so production callers pay nothing;
// only a caller that explicitly wants the breakdown (XM.0's at-scale
// benchmark) passes a non-nil pointer.
type RunStats struct {
	Bridge  time.Duration
	Extract time.Duration
	Derive  time.Duration
	Emit    time.Duration
	// PerFramework is XM.4's addition — total wall time (extract-lowering +
	// derive + emit; the per-framework loop body) keyed by fw.Name, so a
	// caller can find which 3-5 frameworks dominate instead of only the
	// all-frameworks-summed Extract/Derive/Emit totals above. nil unless st
	// is non-nil, same "caller must opt in" rule as the rest of RunStats.
	PerFramework map[string]time.Duration
}

// Run executes stages 1–4 for one service. graphSoFar is the language-semantic
// output; stage 2 (FX.1's GraphFacts) bridges it into base relations every
// framework's rules share. Output is deterministic given deterministic input.
// stats is optional (variadic so every existing 3-arg call site is
// unaffected) — pass a non-nil *RunStats to record XM.0's per-phase timing.
func Run(fws []*Framework, files []ParsedFile, graphSoFar graph.Snapshot, stats ...*RunStats) (Result, error) {
	var st *RunStats
	if len(stats) > 0 {
		st = stats[0]
	}

	base := factpipe.NewFactSet()
	if st != nil {
		t0 := time.Now()
		factpipe.GraphFacts(graphSoFar, base)
		st.Bridge += time.Since(t0)
	} else {
		factpipe.GraphFacts(graphSoFar, base)
	}

	svc := ""
	if len(graphSoFar.Nodes) > 0 {
		svc = graphSoFar.Nodes[0].Service
	}

	roots, releaseRoots := parseFileRoots(files)
	// releaseRoots must outlive every read of roots' trees, including the
	// captured *sitter.Node values inside matchesByFw that the concurrent
	// per-framework loop below (runFramework/applyMatches) reads from —
	// deferred to Run's return, not called right after matchAll.
	defer releaseRoots()

	// XM.1: one shared tree-sitter registry+matcher per language instead of
	// one per framework — see sharedMatchers/buildSharedMatchers/matchAll.
	sharedT0 := time.Now()
	matchesByFw, err := cachedSharedMatchers(fws).matchAll(files, roots)
	if err != nil {
		return Result{}, fmt.Errorf("factpipe: shared match: %w", err)
	}
	sharedElapsed := time.Since(sharedT0)
	if st != nil {
		st.Extract += sharedElapsed
	}

	// XM.2: index base once by predicate instead of linear-scanning it once
	// per framework — turns O(frameworks x base) into O(base + frameworks x
	// keep-set-size).
	baseIndex := indexByPred(base)

	// XM.6: convert every base predicate's facts into datalog.Tuple exactly
	// once per Run call instead of once per framework inside factRelations —
	// see buildBaseTuples.
	baseTuples := buildBaseTuples(baseIndex)

	// XM.5: each iteration below (fset build, applyMatches, the Apply*
	// stages, fw.Rules.Eval, and every emit's Apply) reads only shared
	// read-only inputs (baseIndex, matchesByFw, graphSoFar, fw itself —
	// fw.Rules is "built once by Load, shared read-only" per Framework's own
	// doc comment, and fw.Rules.Eval builds a brand-new *Engine per call, no
	// shared engine state). The only genuinely shared mutable state the old
	// serial loop touched per-iteration was `res`/`seenNode`, both updated
	// in fws order — so each framework computes into its own perFwResults
	// slot fully in parallel, and a second, cheap, strictly-ordered pass
	// merges slots back in original fws order, preserving the exact
	// dedup/error semantics (first framework in order wins a duplicate node
	// ID; first framework in order that errors is the error Run returns) a
	// caller observing determinism depends on.
	results := make([]perFwResult, len(fws))
	{
		var g errgroup.Group
		g.SetLimit(runtime.GOMAXPROCS(0))
		for i, fw := range fws {
			i, fw := i, fw
			g.Go(func() error {
				results[i] = runFramework(fw, matchesByFw[fw.Name], baseIndex, baseTuples, graphSoFar, st != nil)
				return nil // errors are captured per-slot (results[i].err) and surfaced in fws order below
			})
		}
		_ = g.Wait()
	}

	var res Result
	seenNode := map[string]bool{}
	for i, fw := range fws {
		r := results[i]
		if r.err != nil {
			return Result{}, fmt.Errorf("factpipe: framework %s: %w", fw.Name, r.err)
		}
		res.Edges = append(res.Edges, r.edges...)
		for _, n := range r.nodes {
			if !seenNode[n.ID] {
				seenNode[n.ID] = true
				res.Nodes = append(res.Nodes, n)
			}
		}
		res.Unresolved = append(res.Unresolved, stampService(r.unresolved, svc)...)
		res.Ledger = append(res.Ledger, stampService(r.ledger, svc)...)
		for old, newID := range r.replaced {
			if res.Replaced == nil {
				res.Replaced = map[string]string{}
			}
			res.Replaced[old] = newID
		}
		res.Deleted = append(res.Deleted, r.deleted...)
		res.Patches = append(res.Patches, r.patches...)
		res.Resolved = append(res.Resolved, r.resolved...)

		if st != nil {
			st.Extract += r.extractElapsed
			st.Derive += r.deriveElapsed
			st.Emit += r.emitElapsed
			if st.PerFramework == nil {
				st.PerFramework = make(map[string]time.Duration, len(fws))
			}
			st.PerFramework[fw.Name] += r.totalElapsed
		}
	}

	sort.SliceStable(res.Edges, func(i, j int) bool { return res.Edges[i].ID < res.Edges[j].ID })
	sort.SliceStable(res.Nodes, func(i, j int) bool { return res.Nodes[i].ID < res.Nodes[j].ID })
	sortUnresolved(res.Unresolved)
	sortUnresolved(res.Ledger)
	if len(res.Deleted) > 0 {
		sort.Strings(res.Deleted)
		deduped := res.Deleted[:1]
		for _, id := range res.Deleted[1:] {
			if id != deduped[len(deduped)-1] {
				deduped = append(deduped, id)
			}
		}
		res.Deleted = deduped
	}
	sort.SliceStable(res.Patches, func(i, j int) bool { return res.Patches[i].ID < res.Patches[j].ID })
	if len(res.Resolved) > 0 {
		sort.Strings(res.Resolved)
		deduped := res.Resolved[:1]
		for _, r := range res.Resolved[1:] {
			if r != deduped[len(deduped)-1] {
				deduped = append(deduped, r)
			}
		}
		res.Resolved = deduped
	}
	return res, nil
}

// perFwResult is one framework's contribution to Result, computed by
// runFramework in isolation (XM.5) so Run's per-framework loop can fan the
// work out across goroutines and merge the slots back in original fws
// order afterward — see Run's comment above where results is built.
type perFwResult struct {
	edges      []graph.Edge
	nodes      []graph.Node
	unresolved []graph.UnresolvedRef
	ledger     []graph.UnresolvedRef
	replaced   map[string]string
	deleted    []string
	patches    []factpipe.NodePatch
	resolved   []string
	err        error

	extractElapsed time.Duration
	deriveElapsed  time.Duration
	emitElapsed    time.Duration
	totalElapsed   time.Duration
}

// runFramework is Run's per-framework loop body (XM.0-era extract/derive/
// emit staging, unchanged), extracted so it can run concurrently across
// frameworks. It only reads shared state (baseIndex, matches, graphSoFar,
// fw itself) and only writes to its own local fset/perFwResult — nothing
// here mutates anything another concurrent call to runFramework observes.
// trackTime gates the time.Now() calls the same way Run's `st != nil` check
// always has (RunStats' "nil skips every clock read" discipline).
func runFramework(fw *Framework, matches []patterns.MatchResult, baseIndex map[string][]factpipe.Fact, baseTuples map[string][]datalog.Tuple, graphSoFar graph.Snapshot, trackTime bool) perFwResult {
	var r perFwResult
	var fwStart time.Time
	if trackTime {
		fwStart = time.Now()
	}
	fset := factpipe.NewFactSet()
	keep := frameworkKeepSet(fw)
	preds := make([]string, 0, len(keep))
	for p := range keep {
		preds = append(preds, p)
	}
	sort.Strings(preds)
	for _, p := range preds {
		for _, f := range baseIndex[p] {
			fset.Add(f)
		}
	}
	// XM.6: baseFactCount marks where the base-copy loop above ends and this
	// framework's own applyMatches/Apply* facts begin in fset.All() — the
	// copy itself still has to happen (ApplyResolves/ApplyConfig/ApplyTable/
	// ApplyHub/ApplyDerive read these as Facts), but factRelationsWithBase
	// uses it to skip re-converting that prefix into datalog.Tuple, since
	// baseTuples already holds that exact conversion, done once for every
	// framework by Run.
	baseFactCount := fset.Len()
	t0 := time.Now()
	if err := applyMatches(fw, matches, fset); err != nil {
		r.err = err
		return r
	}
	if trackTime {
		r.extractElapsed += time.Since(t0)
	}

	t1 := time.Now()
	factpipe.ApplyResolves(fw.Resolves, graphSoFar.Files, fset)
	factpipe.ApplyConfig(fw.Configs, graphSoFar.ServicePath, fset)
	factpipe.ApplyTable(fw.Tables, graphSoFar.ServicePath, fset)
	factpipe.ApplyHub(fw.Hubs, graphSoFar.Nodes, graphSoFar.Files, graphSoFar.ServicePath, graphSoFar.Links, graphSoFar.Schema, graphSoFar.Unresolved, fset)
	factpipe.ApplyDerive(fw.Derives, fset)

	fr := factRelationsWithBase(fw, fset, baseFactCount, preds, baseTuples)
	derived, prov, err := fw.Rules.Eval(fr)
	if err != nil {
		r.err = fmt.Errorf("eval: %w", err)
		return r
	}
	if trackTime {
		r.deriveElapsed += time.Since(t1)
	}

	t2 := time.Now()
	for _, e := range fw.Emits {
		er := e.Apply(derived[e.Relation()], prov)
		r.edges = append(r.edges, er.Edges...)
		r.nodes = append(r.nodes, er.Nodes...)
		r.unresolved = append(r.unresolved, er.Unresolved...)
		r.ledger = append(r.ledger, er.Ledger...)
		for old, newID := range er.Replaced {
			if r.replaced == nil {
				r.replaced = map[string]string{}
			}
			r.replaced[old] = newID
		}
		r.deleted = append(r.deleted, er.Deleted...)
		r.patches = append(r.patches, er.Patches...)
		r.resolved = append(r.resolved, er.Resolved...)
	}
	if trackTime {
		r.emitElapsed += time.Since(t2)
		r.totalElapsed += time.Since(fwStart)
	}
	return r
}

func stampService(u []graph.UnresolvedRef, svc string) []graph.UnresolvedRef {
	for i := range u {
		if u[i].Service == "" {
			u[i].Service = svc
		}
	}
	return u
}

// sharedMatchers holds one patterns.TreeSitterMatcher per distinct
// Framework.Language among one Run call's active frameworks (XM.1,
// docs/factpipe-cross-framework-matching-plan.md). Before this,
// extractFramework built one Registry+TreeSitterMatcher per framework and
// ran one full tree-sitter query execution per file per framework — for
// cedar's ~15-20 ruby frameworks, ~15-20 redundant walks of the same
// ~3,182 ruby files. matcher.go's getQuerySet already concatenates every
// pattern *registered in one Registry* into a single combined query (one
// cursor.Exec per file); this just feeds it every active framework's
// patterns for a language instead of one framework's.
type sharedMatchers struct {
	byLang map[string]*patterns.TreeSitterMatcher
}

// buildSharedMatchers groups fws by Language and builds one owner-namespaced
// Registry (patterns.RegisterFileOwned) per group, so a match coming back
// out of the combined query can still be routed to its owning Framework by
// name.
func buildSharedMatchers(fws []*Framework) *sharedMatchers {
	byLang := map[string][]*Framework{}
	for _, fw := range fws {
		byLang[fw.Language] = append(byLang[fw.Language], fw)
	}
	sm := &sharedMatchers{byLang: make(map[string]*patterns.TreeSitterMatcher, len(byLang))}
	for lang, group := range byLang {
		reg := patterns.NewRegistry()
		for _, fw := range group {
			reg.RegisterFileOwned(fw.Patterns, fw.Name)
		}
		sm.byLang[lang] = patterns.NewTreeSitterMatcher(reg)
	}
	return sm
}

// sharedMatcherCache memoizes buildSharedMatchers by the exact set of active
// framework names, process-wide — the same "compile once, share read-only"
// shape as LoadEmbedded and matcher.go's predicateRegexCache. Active's gate
// makes the same fws slice (by name, not by pointer) recur across many
// service Run calls with the same dependency profile; without this, every
// Run call would recompile every active language's combined tree-sitter
// query from scratch, undoing FX.8.PERF's per-framework matcher cache.
var sharedMatcherCache sync.Map // key: sorted "\x00"-joined framework names -> *sharedMatchers

func cachedSharedMatchers(fws []*Framework) *sharedMatchers {
	names := make([]string, len(fws))
	for i, fw := range fws {
		names[i] = fw.Name
	}
	sort.Strings(names)
	key := strings.Join(names, "\x00")
	if v, ok := sharedMatcherCache.Load(key); ok {
		return v.(*sharedMatchers)
	}
	actual, _ := sharedMatcherCache.LoadOrStore(key, buildSharedMatchers(fws))
	return actual.(*sharedMatchers)
}

// matchAll runs each language's shared matcher once per matching file and
// routes every match back to its owning framework by name (the owner
// namespace patterns.RegisterFileOwned encoded into PatternName).
func (sm *sharedMatchers) matchAll(files []ParsedFile, roots map[string]*sitter.Node) (map[string][]patterns.MatchResult, error) {
	out := map[string][]patterns.MatchResult{}
	for _, f := range files {
		m := sm.byLang[f.Language]
		if m == nil {
			continue
		}
		grammar := f.Grammar
		if grammar == "" {
			grammar = f.Language
		}
		matches, err := m.MatchWithGrammarRoot(f.Language, grammar, f.Path, f.Src, roots[f.Path])
		if err != nil {
			return nil, err
		}
		for _, mr := range matches {
			owner, name, ok := patterns.SplitOwner(mr.PatternName)
			if !ok {
				continue
			}
			mr.PatternName = name
			out[owner] = append(out[owner], mr)
		}
	}
	return out, nil
}

// parseFileRoots parses each file once (FX.8.PERF, docs/declarative-
// framework-pipeline-plan.md) so Run's per-framework loop below doesn't
// re-parse the same source bytes once per matching-language framework —
// tree-sitter parse cost, not compiled-query cost, was the remaining
// dominant cost after LoadEmbedded/matcher caching. A file whose grammar
// this build has no binding for, or that fails to parse, is simply absent
// from the map; extractFramework's MatchWithGrammarRoot call falls back to
// parsing it itself (and surfacing the same error it always did) when its
// root is missing.
//
// XM.27 (docs/factpipe-cross-framework-matching-plan.md): JS/TS and Ruby
// files route through internal/jsast.Parse / internal/rubyast.Parse instead
// of a private sitter.ParseCtx call. Both packages own a process-wide cache
// that internal/indexer enables for the whole link-pass phase
// (EnableJSTreeCache/EnableRubyTreeCache) and that every hand-written ruby_*/
// js_* link pass plus several factpipe hub providers (hub_schema_url_
// link.go, hub_pusher_producer.go, ...) already read and populate — this
// pass's parse of a given file is, in production, very likely either a
// cache hit (some other pass already parsed it this run) or the parse that
// makes it one for whoever asks next, instead of a fourth independent
// tree-sitter parse of the same bytes. Outside a link phase (tests calling
// Run directly, cache never enabled) both packages fall back to an
// uncached, per-call parse — identical cost to what this function always
// did. Go/ERB files have no such shared-cache package and keep the direct
// sitter.ParseCtx path. release must be deferred until every reader of
// roots' trees is done — including the *sitter.Node values matchAll's
// matches capture, read later by the concurrent per-framework loop in Run —
// not called right after this function returns.
func parseFileRoots(files []ParsedFile) (map[string]*sitter.Node, func()) {
	roots := make(map[string]*sitter.Node, len(files))
	var releases []func()
	for _, f := range files {
		grammar := f.Grammar
		if grammar == "" {
			grammar = f.Language
		}
		var root *sitter.Node
		switch {
		case f.Language == "javascript" && (grammar == "typescript" || grammar == "tsx"):
			// jsast.Parse always parses under its own tsLang/tsxLang choice
			// (GrammarLangForFile: tsx for .tsx/.jsx, typescript — a JS
			// superset — for everything else, including plain .js/.mjs/.cjs).
			// That only matches what this framework pipeline asks for
			// (grammar == "typescript" or "tsx") for .ts/.tsx/.jsx files;
			// plain .js/.mjs/.cjs files want the separate, non-superset
			// "javascript" grammar patternLangForFile assigns them, which
			// jsast never produces, so those keep the raw path below —
			// substituting jsast's tree there silently drops matches from
			// every framework pattern compiled against the real javascript
			// grammar (caught via a real-corpus node/edge count regression
			// on orion before this gate existed).
			if _, root2, _, ok := jsast.Parse(f.Path); ok {
				root = root2
			}
		case f.Language == "ruby":
			if _, root2, release, ok := rubyast.Parse(f.Path); ok {
				root = root2
				releases = append(releases, release)
			}
		}
		if root == nil {
			lang := patterns.GrammarFor(grammar)
			if lang == nil {
				continue
			}
			var err error
			root, err = sitter.ParseCtx(context.Background(), f.Src, lang)
			if err != nil {
				continue
			}
		}
		// XM.5: roots is read by every active framework's applyMatches (via
		// matchesByFw's captured nodes) once Run's per-framework loop below
		// runs frameworks concurrently — warm each tree here, still
		// single-threaded, so every later concurrent navigation is a pure
		// cache read (see factpipe.WarmParseTree's doc comment for why a
		// lazily-memoizing *sitter.Tree is otherwise unsafe to share).
		factpipe.WarmParseTree(root)
		roots[f.Path] = root
	}
	return roots, func() {
		for _, r := range releases {
			r()
		}
	}
}

// factSpecsByPattern maps a framework's own (unprefixed) pattern names to
// their facts: block, shared by extractFramework (EvalOnce's single-
// framework path, still runs its own matcher) and applyMatches (Run's XM.1
// shared-matcher path). A framework with neither a facts-bearing pattern nor
// a hub provider cannot produce anything — the same "this pass could never
// have worked" error either path surfaced before this was factored out.
func factSpecsByPattern(fw *Framework) (map[string][]patterns.FactSpec, error) {
	specsByPattern := map[string][]patterns.FactSpec{}
	for _, p := range fw.Patterns.Patterns {
		if len(p.Facts) > 0 {
			specsByPattern[p.Name] = p.Facts
		}
	}
	if len(specsByPattern) == 0 && len(fw.Hubs) == 0 {
		return nil, fmt.Errorf("no patterns carry a facts: block")
	}
	return specsByPattern, nil
}

// applyMatches lowers this framework's share of a sharedMatchers.matchAll
// result to facts in dst — extractFramework's per-file loop, minus the
// tree-sitter query execution itself (already done once, shared across
// every framework of this language, by matchAll).
func applyMatches(fw *Framework, matches []patterns.MatchResult, dst factpipe.FactSet) error {
	specsByPattern, err := factSpecsByPattern(fw)
	if err != nil {
		return err
	}
	for _, mr := range matches {
		specs := specsByPattern[mr.PatternName]
		if len(specs) == 0 {
			continue
		}
		ec := &patterns.ExtractContext{}
		for _, fact := range patterns.MatchToFacts(mr, specs, ec) {
			dst.Add(fact)
		}
	}
	return nil
}

// extractFramework runs stage 1: match this framework's fact-bearing patterns
// against every file of its language and lower each match to facts. roots is
// parseFileRoots' pre-parsed-per-file cache, keyed by file path; nil is
// valid (each match falls back to parsing its own file, as EvalOnce's single-
// framework introspection callers do — parse-once sharing only pays off
// across multiple frameworks).
func extractFramework(fw *Framework, files []ParsedFile, dst factpipe.FactSet, roots map[string]*sitter.Node) error {
	m := fw.matcher

	specsByPattern, err := factSpecsByPattern(fw)
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.Language != fw.Language {
			continue
		}
		grammar := f.Grammar
		if grammar == "" {
			grammar = fw.Language
		}
		matches, err := m.MatchWithGrammarRoot(fw.Language, grammar, f.Path, f.Src, roots[f.Path])
		if err != nil {
			return err
		}
		for _, mr := range matches {
			specs := specsByPattern[mr.PatternName]
			if len(specs) == 0 {
				continue
			}
			ec := &patterns.ExtractContext{}
			for _, fact := range patterns.MatchToFacts(mr, specs, ec) {
				dst.Add(fact)
			}
		}
	}
	return nil
}

// indexByPred groups a FactSet's facts by predicate, once per Run call
// (XM.2, docs/factpipe-cross-framework-matching-plan.md) — frameworkKeepSet's
// filter then costs O(keep-set size) per framework to apply instead of a
// fresh O(len(base)) linear scan each time, for the same base scanned
// ~25-33 times regardless.
func indexByPred(fs factpipe.FactSet) map[string][]factpipe.Fact {
	idx := map[string][]factpipe.Fact{}
	for _, f := range fs.All() {
		idx[f.Pred] = append(idx[f.Pred], f)
	}
	return idx
}

// buildBaseTuples converts every base predicate's facts (indexByPred's
// baseIndex) into datalog.Tuple exactly once per Run call (XM.6,
// docs/factpipe-cross-framework-matching-plan.md), instead of once per
// framework inside factRelations. Before this, every active framework that
// kept a given base predicate (frameworkKeepSet) redid the identical
// Fact.Args[i].Value() conversion over the identical baseIndex[pred] slice —
// with cedar's ~20 active ruby frameworks routinely sharing the same handful
// of GraphFacts-bridged predicates, that conversion ran ~20x for no reason.
// Read-only afterward: factRelationsWithBase only ever appends these slices
// into a fresh per-framework *datalog.FactRelations (datalog.Tuple's
// element type, string, makes an appended tuple immutable in practice), so
// sharing the same backing slice across every concurrent runFramework call
// is safe with no lock.
func buildBaseTuples(baseIndex map[string][]factpipe.Fact) map[string][]datalog.Tuple {
	out := make(map[string][]datalog.Tuple, len(baseIndex))
	for pred, facts := range baseIndex {
		tuples := make([]datalog.Tuple, len(facts))
		for i, f := range facts {
			t := make(datalog.Tuple, len(f.Args))
			for j, a := range f.Args {
				t[j] = a.Value()
			}
			tuples[i] = t
		}
		out[pred] = tuples
	}
	return out
}

// factRelationsWithBase is factRelations' XM.6 fast path for runFramework:
// baseFactCount is fset.Len() captured right after the base-predicate copy
// loop in runFramework and before applyMatches/Apply* ran, so
// fset.All()[:baseFactCount] is exactly the facts baseTuples already holds
// pre-converted (in the same order, since both walk preds/baseIndex[p] in
// the same order) and fset.All()[baseFactCount:] is this framework's own
// new facts, which still need today's per-fact conversion. preds is the
// same sorted keep-set runFramework copied from, reused here instead of
// recomputed.
func factRelationsWithBase(fw *Framework, fset factpipe.FactSet, baseFactCount int, preds []string, baseTuples map[string][]datalog.Tuple) *datalog.FactRelations {
	fr := datalog.NewFactRelations(fw.goals...)
	present := map[string]bool{}
	for _, p := range preds {
		if ts := baseTuples[p]; len(ts) > 0 {
			fr.Add(p, ts...)
			present[p] = true
		}
	}
	for _, f := range fset.All()[baseFactCount:] {
		t := make(datalog.Tuple, len(f.Args))
		for i, a := range f.Args {
			t[i] = a.Value()
		}
		fr.Add(f.Pred, t)
		present[f.Pred] = true
	}
	for _, rel := range fw.Rules.BaseRelations() {
		if !present[rel] {
			fr.Declare(rel)
		}
	}
	return fr
}

// frameworkKeepSet is the audited (XM.2) set of base predicates a framework
// can actually consume out of the copied base FactSet — NOT just
// fw.Rules.BaseRelations() (the rules' own input). ApplyResolves/ApplyConfig/
// ApplyTable each read pre-existing facts out of fset between the base copy
// and factRelations, via the predicate named in the framework's own
// resolve:/config:/table: block (CompiledResolve.From/CompiledConfig.From/
// CompiledTable.Against — the latter only for an artifact-backed block;
// Against() is "" for a declarative rows: block, which reads nothing).
// ApplyDerive always reads exactly "node_meta" — CompileDeriveSpecs enforces
// every derive: block's `from` is literally "node_meta", so no per-spec
// accessor is needed. ApplyHub reads no fset facts at all (only
// nodes/files/svcPath/links/schema, passed to it directly by Run, never
// through fset) so it contributes nothing here — audited by reading
// internal/factpipe/hub.go's ApplyHub, which never calls dst.All().
//
// A predicate missing here starves that Apply* stage silently (no error, no
// panic — a relation the stage would have populated stays empty), the exact
// failure mode this plan's own Risks section warns about; the regression
// guard is TestFrameworkKeepSetCoversApplyStageReads plus cedar/orion 0/0
// diff, not code review.
func frameworkKeepSet(fw *Framework) map[string]bool {
	keep := make(map[string]bool)
	for _, rel := range fw.Rules.BaseRelations() {
		keep[rel] = true
	}
	for _, r := range fw.Resolves {
		keep[r.From()] = true
	}
	for _, c := range fw.Configs {
		keep[c.From()] = true
	}
	for _, t := range fw.Tables {
		if against := t.Against(); against != "" {
			keep[against] = true
		}
	}
	if len(fw.Derives) > 0 {
		keep["node_meta"] = true
	}
	return keep
}

// factRelations groups a FactSet into the datalog base, one relation per
// predicate, and declares every base relation the rules read that produced no
// facts ("this service really has no X").
func factRelations(fw *Framework, src factpipe.FactSet) *datalog.FactRelations {
	fr := datalog.NewFactRelations(fw.goals...)
	present := map[string]bool{}
	for _, f := range src.All() {
		t := make(datalog.Tuple, len(f.Args))
		for i, a := range f.Args {
			t[i] = a.Value()
		}
		fr.Add(f.Pred, t)
		present[f.Pred] = true
	}
	for _, rel := range fw.Rules.BaseRelations() {
		if !present[rel] {
			fr.Declare(rel)
		}
	}
	return fr
}

// EvalOnce runs stages 1–3 (extract → bridge → derive) for one framework over
// one service and returns the tuples of every relation named in goals (the
// emit relations plus anything passed in extraGoals). It is the introspection
// point for the FX.8 differential harness and for debugging a rule set; stage 4
// (emit) is Run's job.
func (fw *Framework) EvalOnce(files []ParsedFile, graphSoFar graph.Snapshot, extraGoals ...string) (map[string][]datalog.Tuple, error) {
	fset := factpipe.NewFactSet()
	factpipe.GraphFacts(graphSoFar, fset)
	if err := extractFramework(fw, files, fset, nil); err != nil {
		return nil, err
	}
	factpipe.ApplyResolves(fw.Resolves, graphSoFar.Files, fset)
	factpipe.ApplyConfig(fw.Configs, graphSoFar.ServicePath, fset)
	factpipe.ApplyTable(fw.Tables, graphSoFar.ServicePath, fset)
	factpipe.ApplyHub(fw.Hubs, graphSoFar.Nodes, graphSoFar.Files, graphSoFar.ServicePath, graphSoFar.Links, graphSoFar.Schema, graphSoFar.Unresolved, fset)
	factpipe.ApplyDerive(fw.Derives, fset)
	fr := factRelations(fw, fset)
	fr.Goals = append(append([]string(nil), fr.Goals...), extraGoals...)
	derived, _, err := fw.Rules.Eval(fr)
	return derived, err
}

func sortUnresolved(u []graph.UnresolvedRef) {
	sort.SliceStable(u, func(i, j int) bool {
		a, b := u[i], u[j]
		switch {
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.Name != b.Name:
			return a.Name < b.Name
		case a.Service != b.Service:
			return a.Service < b.Service
		case a.File != b.File:
			return a.File < b.File
		default:
			return a.Line < b.Line
		}
	})
}
