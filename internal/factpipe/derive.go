package factpipe

import "fmt"

// derive.go is FX.8.24's unblocking primitive — approved design,
// docs/declarative-framework-pipeline-plan.md § FX.8.24/FX.8.P6. A `derive:`
// block joins several already-materialized `node_meta` columns on the SAME
// graph node into one base fact, via a literal separator:
//
//	derive:
//	  - relation: controller_key      # output relation: (Id, Value), 2-arity
//	    from: node_meta                # always node_meta; no other source
//	    columns: [controller_module, resource]
//	    separator: "/"
//
// This is deliberately narrower than resolve_path or path_transform: no
// candidate enumeration, no regex, no substrings, no case transforms, no
// conditional branching, no source other than node_meta. It exists because a
// `.dl` rule cannot build a string — datalog's builtins (lt/le/ne/contains/
// prefix) are boolean tests over already-bound atoms, never generators — so
// "assemble one fact from two node_meta columns already sitting on the same
// node" has no home anywhere else in the vocabulary. A node missing any one
// of the named columns emits nothing for that relation: silent join-miss,
// the same discipline resolve_path already uses for "no candidate hit" — no
// error, no panic, just an absent fact a `.dl` rule's `not` can test.
type DeriveSpec struct {
	Relation  string   `yaml:"relation"`
	From      string   `yaml:"from"`
	Columns   []string `yaml:"columns"`
	Separator *string  `yaml:"separator"`
}

// CompiledDerive is a validated DeriveSpec.
type CompiledDerive struct{ spec DeriveSpec }

// Relation is the derived relation this derive produces.
func (d CompiledDerive) Relation() string { return d.spec.Relation }

// CompileDeriveSpecs validates already-decoded specs (the pipeline path).
func CompileDeriveSpecs(specs []DeriveSpec) ([]CompiledDerive, error) {
	out := make([]CompiledDerive, 0, len(specs))
	for _, s := range specs {
		if s.Relation == "" {
			return nil, fmt.Errorf("derive: missing relation")
		}
		if s.From != "node_meta" {
			return nil, fmt.Errorf("derive %q: from must be \"node_meta\"", s.Relation)
		}
		if len(s.Columns) < 2 {
			return nil, fmt.Errorf("derive %q: columns needs at least 2 entries", s.Relation)
		}
		for _, c := range s.Columns {
			if c == "" {
				return nil, fmt.Errorf("derive %q: columns entries must be non-empty", s.Relation)
			}
		}
		if s.Separator == nil {
			return nil, fmt.Errorf("derive %q: missing separator (an empty string is a valid value, but the key must be present)", s.Relation)
		}
		out = append(out, CompiledDerive{spec: s})
	}
	return out, nil
}

// ApplyDerive runs every `derive:` block against the `node_meta` facts already
// in fs (from the FX.1 bridge, or any Stage-1 extraction that happens to
// assert the same predicate), adding one joined fact per node that carries
// every named column. It is a no-op when the framework has no derive block,
// the same inert-by-default shape as ApplyResolves/ApplyConfig/ApplyTable/
// ApplyHub.
func ApplyDerive(ds []CompiledDerive, fs FactSet) {
	if len(ds) == 0 {
		return
	}

	// Group node_meta(Id, Key, Value) facts by node, preserving first-seen node
	// order for determinism (the bridge emits node_meta in Snapshot.Nodes order
	// with keys sorted, so this is stable given a deterministic input).
	type nodeMeta struct {
		order []string
		vals  map[string]map[string]string
		orig  map[string]map[string]Origin
	}
	nm := nodeMeta{vals: map[string]map[string]string{}, orig: map[string]map[string]Origin{}}
	for _, f := range fs.All() {
		if f.Pred != "node_meta" || len(f.Args) < 3 {
			continue
		}
		id, key, val := f.Args[0].Value(), f.Args[1].Str, f.Args[2].Str
		if nm.vals[id] == nil {
			nm.order = append(nm.order, id)
			nm.vals[id] = map[string]string{}
			nm.orig[id] = map[string]Origin{}
		}
		if _, dup := nm.vals[id][key]; !dup {
			nm.vals[id][key] = val
			nm.orig[id][key] = f.Origin
		}
	}

	for _, d := range ds {
		for _, id := range nm.order {
			meta := nm.vals[id]
			parts := make([]string, len(d.spec.Columns))
			ok := true
			for i, c := range d.spec.Columns {
				v, present := meta[c]
				if !present {
					ok = false
					break
				}
				parts[i] = v
			}
			if !ok {
				continue
			}
			joined := parts[0]
			for _, p := range parts[1:] {
				joined += *d.spec.Separator + p
			}
			origin := nm.orig[id][d.spec.Columns[0]]
			origin.Pattern = d.Relation()
			fs.Add(Fact{
				Pred:   d.Relation(),
				Args:   []Atom{Node(id), Str(joined)},
				Origin: origin,
			})
		}
	}
}
