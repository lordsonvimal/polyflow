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
	return &Program{src: append([]byte(nil), src...), name: name}, nil
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
