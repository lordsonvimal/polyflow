package factpipe

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lordsonvimal/polyflow/internal/artifact"
)

// table.go is FX.8.P4 — the table_facts primitive (plan § FX.0). Two
// frameworks need to assert a static lookup table as base facts, not extract
// one from source: rails_devise (module name -> URL scope, currently a
// hardcoded Go map) and schema_url_link (a checked-in JSON/YAML asset, Tier
// AR's internal/artifact reader+gate, SA.3). Both are "here is a table; turn
// every row into a fact" — no tree-matching, no dataflow — so, like
// resolve_path / config_value, a `.dl` rule cannot express admitting the
// table; a primitive does.
//
// A `table:` block in the framework YAML is one of two mutually exclusive
// shapes:
//
//	table:
//	  - relation: devise_scope   # declarative: literal rows, unconditional
//	    rows:
//	      - [confirmable, confirmations]
//	      - [recoverable, passwords]
//	  - relation: schema_url_entity   # artifact-backed: Tier AR mapping
//	    artifact: endpoint_table      # kind name in artifacts/*.yaml
//	    against: handler_path         # relation already in the fact set...
//	    against_arg: 0                # ...whose Nth arg corroborates a leaf
//
// The declarative shape needs no disk access and no gate: every row becomes
// one fact on every run, same as a fixed extraction. The artifact shape reuses
// Tier AR's Reader (format dispatch) and Gate (corroboration) untouched —
// SA.3 built both but nothing ever called them (git grep confirms no
// non-artifact package imports internal/artifact); this primitive, and
// artifact.Mapping.Rows (the Verdict -> facts conversion SA.3 left undone),
// are that first caller.
type TableSpec struct {
	Relation   string     `yaml:"relation"`
	Rows       [][]string `yaml:"rows"`
	Artifact   string     `yaml:"artifact"`
	Against    string     `yaml:"against"`
	AgainstArg int        `yaml:"against_arg"`
}

// CompiledTable is a validated TableSpec.
type CompiledTable struct{ spec TableSpec }

// Relation is the derived relation this table block produces.
func (t CompiledTable) Relation() string { return t.spec.Relation }

// CompileTableSpecs validates already-decoded specs (the pipeline path).
func CompileTableSpecs(specs []TableSpec) ([]CompiledTable, error) {
	out := make([]CompiledTable, 0, len(specs))
	for _, s := range specs {
		if s.Relation == "" {
			return nil, fmt.Errorf("table: missing relation")
		}
		hasRows := len(s.Rows) > 0
		hasArtifact := s.Artifact != ""
		if hasRows == hasArtifact {
			return nil, fmt.Errorf("table %q: exactly one of rows or artifact", s.Relation)
		}
		if hasArtifact {
			if s.Against == "" {
				return nil, fmt.Errorf("table %q: artifact needs against", s.Relation)
			}
			if _, ok := artifact.MappingFor(s.Artifact); !ok {
				return nil, fmt.Errorf("table %q: unknown artifact mapping %q", s.Relation, s.Artifact)
			}
		}
		out = append(out, CompiledTable{spec: s})
	}
	return out, nil
}

// ApplyTable runs every `table:` block against dst. Declarative blocks (rows
// set) are unconditional and ignore svcPath entirely. Artifact-backed blocks
// need svcPath (to find files on disk) and dst's already-asserted `against`
// facts (to corroborate them) — like ApplyConfig, an artifact block is a
// no-op when svcPath is "", so it is inert for every corpus-only (no real
// filesystem) test until one opts in.
func ApplyTable(ts []CompiledTable, svcPath string, dst FactSet) {
	for _, t := range ts {
		if len(t.spec.Rows) > 0 {
			applyDeclarativeTable(t, dst)
			continue
		}
		if svcPath == "" {
			continue
		}
		applyArtifactTable(t, svcPath, dst)
	}
}

func applyDeclarativeTable(t CompiledTable, dst FactSet) {
	for _, row := range t.spec.Rows {
		args := make([]Atom, len(row))
		for i, v := range row {
			args[i] = Str(v)
		}
		dst.Add(Fact{
			Pred: t.Relation(),
			Args: args,
			Origin: Origin{
				Kind:    OriginPrimitive,
				Pattern: t.Relation(),
			},
		})
	}
}

// applyArtifactTable walks svcPath for files a registered artifact.Reader
// claims, keeps the ones the mapping's Gate corroborates against the
// `against` relation's `against_arg`'th argument (already in dst — extracted
// by this framework or bridged from the graph), and asserts one
// (Entity, Key, Value) fact per matched leaf.
func applyArtifactTable(t CompiledTable, svcPath string, dst FactSet) {
	m, ok := artifact.MappingFor(t.spec.Artifact)
	if !ok {
		return
	}
	gate := m.GateSpec()
	known := collectKnown(t.spec.Against, t.spec.AgainstArg, gate, dst)
	knownByService := map[string]map[string]bool{"": known}

	formats := make(map[string]bool, len(m.Formats))
	for _, f := range m.Formats {
		formats[f] = true
	}

	_ = filepath.WalkDir(svcPath, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(svcPath, p)
		a, ok := artifact.ReadFile(rel, raw)
		if !ok || !formats[a.Format] {
			return nil
		}
		v := gate.Evaluate(a, knownByService, false)
		for _, row := range m.Rows(v) {
			dst.Add(Fact{
				Pred: t.Relation(),
				Args: []Atom{Str(row.Entity), Str(row.Key), Str(row.Value)},
				Origin: Origin{
					Kind:    OriginPrimitive,
					File:    rel,
					Pattern: t.Relation(),
				},
			})
		}
		return nil
	})
}

// collectKnown normalises the against relation's against_arg'th argument
// through the gate's chain for every fact already in src — the corroboration
// set Gate.Evaluate compares an artifact's leaves against.
func collectKnown(against string, argIdx int, gate artifact.Gate, src FactSet) map[string]bool {
	out := map[string]bool{}
	for _, f := range src.All() {
		if f.Pred != against || argIdx >= len(f.Args) {
			continue
		}
		a := f.Args[argIdx]
		if a.Kind != AtomStr {
			continue
		}
		if norm, ok := gate.Norm(a.Str); ok {
			out[norm] = true
		}
	}
	return out
}
