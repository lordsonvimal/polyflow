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
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	patterndata "github.com/lordsonvimal/polyflow/patterns"
	"github.com/lordsonvimal/polyflow/rules"
)

// LoadEmbedded pairs the rule + pattern trees compiled into the binary. This is
// the production entry point; Load takes explicit filesystems for tests.
func LoadEmbedded() (*Registry, error) {
	return Load(rules.FS, patterndata.FS)
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

	goals []string // the emit relations, materialized by Eval
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
	return &Framework{
		Name:     name,
		Language: lang,
		Gate:     Gate{Package: pf.Package, VersionRange: pf.VersionRange},
		Patterns: &pf,
		Rules:    prog,
		Emits:    emits,
		goals:    goals,
	}, nil
}

// All returns every registered framework, gate ignored — for introspection.
func (r *Registry) All() []*Framework {
	return append([]*Framework(nil), r.frameworks...)
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
	Unresolved []graph.UnresolvedRef
	Ledger     []graph.UnresolvedRef
}

// Run executes stages 1–4 for one service. graphSoFar is the language-semantic
// output; stage 2 (FX.1's GraphFacts) bridges it into base relations every
// framework's rules share. Output is deterministic given deterministic input.
func Run(fws []*Framework, files []ParsedFile, graphSoFar graph.Snapshot) (Result, error) {
	base := factpipe.NewFactSet()
	factpipe.GraphFacts(graphSoFar, base)

	svc := ""
	if len(graphSoFar.Nodes) > 0 {
		svc = graphSoFar.Nodes[0].Service
	}

	var res Result
	for _, fw := range fws {
		fset := factpipe.NewFactSet()
		for _, f := range base.All() {
			fset.Add(f)
		}
		if err := extractFramework(fw, files, fset); err != nil {
			return Result{}, fmt.Errorf("factpipe: framework %s: %w", fw.Name, err)
		}

		fr := factRelations(fw, fset)
		derived, prov, err := fw.Rules.Eval(fr)
		if err != nil {
			return Result{}, fmt.Errorf("factpipe: framework %s eval: %w", fw.Name, err)
		}

		for _, e := range fw.Emits {
			er := e.Apply(derived[e.Relation()], prov)
			res.Edges = append(res.Edges, er.Edges...)
			res.Unresolved = append(res.Unresolved, stampService(er.Unresolved, svc)...)
			res.Ledger = append(res.Ledger, stampService(er.Ledger, svc)...)
		}
	}

	sort.SliceStable(res.Edges, func(i, j int) bool { return res.Edges[i].ID < res.Edges[j].ID })
	sortUnresolved(res.Unresolved)
	sortUnresolved(res.Ledger)
	return res, nil
}

func stampService(u []graph.UnresolvedRef, svc string) []graph.UnresolvedRef {
	for i := range u {
		if u[i].Service == "" {
			u[i].Service = svc
		}
	}
	return u
}

// extractFramework runs stage 1: match this framework's fact-bearing patterns
// against every file of its language and lower each match to facts.
func extractFramework(fw *Framework, files []ParsedFile, dst factpipe.FactSet) error {
	reg := patterns.NewRegistry()
	reg.RegisterFile(fw.Patterns)
	m := patterns.NewTreeSitterMatcher(reg)

	specsByPattern := map[string][]patterns.FactSpec{}
	for _, p := range fw.Patterns.Patterns {
		if len(p.Facts) > 0 {
			specsByPattern[p.Name] = p.Facts
		}
	}
	if len(specsByPattern) == 0 {
		return fmt.Errorf("no patterns carry a facts: block")
	}

	for _, f := range files {
		if f.Language != fw.Language {
			continue
		}
		grammar := f.Grammar
		if grammar == "" {
			grammar = fw.Language
		}
		matches, err := m.MatchWithGrammar(fw.Language, grammar, f.Path, f.Src)
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
