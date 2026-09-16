package datalog

import "sort"

// FX.3 — the per-batch evaluation surface.
//
// The framework pipeline compiles a rule set once and evaluates it against many
// per-service fact sets. Program is that compiled unit; Eval runs it bottom-up
// over a supplied fact base with no surviving mutation, so a batch cannot see
// another batch's state.
//
// The compiled join plan the pinned interface anticipates is FX.4 (the columnar
// engine). Until then Eval re-parses the rule text per call — parsing is
// microseconds, and it keeps each evaluation on its own Engine with its own
// interner and its own mutable rule state, which is the isolation guarantee that
// matters. Compile still parses and fully validates up front so a bad rule set
// fails at compile time, not on the first batch.

// Program is a parsed, safety-checked, stratified rule set.
type Program struct {
	src  []byte
	name string
	base []string // body relations that are neither a rule head nor a builtin
}

// BaseRelations returns the sorted names of every relation a rule body reads
// that no rule derives — i.e. the relations the caller must supply (Add or
// Declare) on the FactRelations before Eval. LoadRules rejects the program if
// one of these is absent, so the pipeline uses this list to Declare the ones a
// given service produced no facts for ("this service really has no X").
func (p *Program) BaseRelations() []string {
	return append([]string(nil), p.base...)
}

// Compile validates src. It fails here — not at Eval — on a syntax error, an
// unsafe rule, or an unstratifiable program (negation or aggregation through
// recursion).
func Compile(src []byte, name string) (*Program, error) {
	rules, err := parseRules(src, name)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if err := checkSafe(r); err != nil {
			return nil, err
		}
	}
	if _, err := stratify(rules); err != nil {
		return nil, err
	}

	heads := map[string]bool{}
	for _, r := range rules {
		heads[r.Head.Rel] = true
	}
	baseSet := map[string]bool{}
	for _, r := range rules {
		for _, l := range r.Body {
			if l.Rel == "" || heads[l.Rel] || isBuiltin(l.Rel) {
				continue
			}
			baseSet[l.Rel] = true
		}
	}
	base := make([]string, 0, len(baseSet))
	for rel := range baseSet {
		base = append(base, rel)
	}
	sort.Strings(base)

	return &Program{src: append([]byte(nil), src...), name: name, base: base}, nil
}

// FactRelations is the base-fact input to Eval: relation name to tuples, plus
// the goals to materialize. A relation a rule reads must be present — Add a
// tuple or Declare it empty — or LoadRules rejects the program rather than
// silently yielding nothing.
type FactRelations struct {
	rels  map[string][]Tuple
	Goals []string
}

// NewFactRelations returns an empty fact base.
func NewFactRelations(goals ...string) *FactRelations {
	return &FactRelations{rels: map[string][]Tuple{}, Goals: goals}
}

// Declare records a relation as present but empty ("this service really has no
// X"), which Eval distinguishes from an unknown relation.
func (f *FactRelations) Declare(rel string) {
	if _, ok := f.rels[rel]; !ok {
		f.rels[rel] = nil
	}
}

// Add appends tuples to a relation.
func (f *FactRelations) Add(rel string, tuples ...Tuple) {
	f.rels[rel] = append(f.rels[rel], tuples...)
}

// Eval runs the program bottom-up over base and returns every goal's tuples,
// sorted. Derivation recording is off on this path (~40% of derivation cost,
// datalog-engine-performance-plan.md P.6); the returned *Provenance recomputes
// one tuple's derivation on demand.
func (p *Program) Eval(base *FactRelations) (map[string][]Tuple, *Provenance, error) {
	e := New(Options{})

	names := make([]string, 0, len(base.rels))
	for rel := range base.rels {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		if err := e.Assert(rel, base.rels[rel]); err != nil {
			return nil, nil, err
		}
	}
	if err := e.LoadRules(p.src, p.name); err != nil {
		return nil, nil, err
	}

	out := make(map[string][]Tuple, len(base.Goals))
	for _, g := range base.Goals {
		ts, err := e.Query(g)
		if err != nil {
			return nil, nil, err
		}
		out[g] = ts
	}
	return out, &Provenance{e: e}, nil
}

// Provenance answers "which rule produced this tuple, from which base facts" for
// one goal tuple, recomputing with derivation recording on.
type Provenance struct{ e *Engine }

// Of returns every derivation of t under goal.
func (p *Provenance) Of(goal string, t Tuple) ([]Derivation, error) {
	return p.e.Provenance(goal, t)
}

// RuleOf answers "which single rule produced t" without Of's full recording-
// on re-derivation, when the bulk pass already recorded an unambiguous
// answer (XM.11, docs/factpipe-cross-framework-matching-plan.md). ok is
// false when the caller must fall back to Of — no fast answer recorded, or
// t is genuinely derivable more than one way.
func (p *Provenance) RuleOf(goal string, t Tuple) (string, bool) {
	return p.e.RuleOf(goal, t)
}
