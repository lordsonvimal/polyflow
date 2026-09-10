// Package datalog is Tier DL: a small stratified-datalog engine for expressing
// derivation rules — ancestor closures, filter chains, containment — as rules
// instead of as hand-written recursive walks.
//
// It exists because the walks it replaces all have the same shape and all get
// the same things wrong. `internal/linker/rails_filters.go` computes an
// ancestor closure by hand in seven mutually-recursive functions, recomputing
// the chain per class; `import_edges`, `containment` and `routegroup` each
// carry their own copy of the same idea. A rule file states the closure once,
// and the engine is the only place that has to get cycle-safety, ordering and
// fixpoint right.
//
// Two properties are non-negotiable and are why this is not a thirty-line
// bottom-up loop:
//
//   - **Demand-driven by default.** Evaluation is top-down with tabling, so a
//     query for one class's filters does not materialize the closure for every
//     class. Full bottom-up materialization is available under
//     Options.DemandDriven=false, for debugging only.
//   - **Derivation tracking from the start.** Provenance answers "which rule
//     produced this tuple, from which base facts" (SA.1). Retrofitting it means
//     re-running evaluation, so it is recorded during evaluation, not after.
//
// The engine is deliberately graph-agnostic — the layer contract in
// internal/arch forbids it importing internal/graph. All terms are strings; the
// emitter that asserts the facts is the only thing that knows what a key means.
package datalog

import (
	"fmt"
	"sort"
	"strings"
)

// Tuple is one fact. All terms are strings; the emitter types them.
type Tuple []string

// Relation names a relation and its arity.
type Relation struct {
	Name  string
	Arity int
}

// Derivation is one step of "why does this tuple exist": the rule that fired
// and the tuples it consumed. Body entries are themselves queryable, so a
// caller can walk back to base facts; Rule is the string that goes into
// SourceRef.Rule (SA.1), formatted "<rulefile>/<head relation>".
type Derivation struct {
	Rule string // e.g. "rails_filters/effective_filter"
	Head Tuple  // the derived tuple
	Body []DerivationStep
}

// DerivationStep is one positive body literal as it was satisfied. Negated
// literals contribute no step: there is no tuple to name, only the absence of
// one, and inventing a placeholder row would make a derivation unreadable.
type DerivationStep struct {
	Relation string
	Tuple    Tuple
	Base     bool // true when this came from Assert, not from another rule
}

// Options configures evaluation.
type Options struct {
	// DemandDriven selects top-down evaluation with tabling, so a bound query
	// computes only the subgoals it needs. Default true (the zero value is
	// promoted by New). Full bottom-up materialization is what makes
	// `registered(C, CB, Kind)` explode across a large ancestor closure, and is
	// available only for debugging.
	DemandDriven bool

	// MaxTuples caps answers per relation; exceeded => error naming it.
	MaxTuples int
	// MaxRounds caps fixpoint iteration.
	MaxRounds int
}

func (o Options) withDefaults() Options {
	if o.MaxTuples == 0 {
		o.MaxTuples = 5_000_000
	}
	if o.MaxRounds == 0 {
		o.MaxRounds = 200
	}
	return o
}

// relation is a set of tuples with per-argument indexes, built lazily. The
// index matters: without it every join degrades to a full scan, and the pass
// this tier replaces is a closure over thousands of classes.
type relation struct {
	name   string
	arity  int
	tuples []Tuple
	seen   map[string]bool
	idx    []map[string][]Tuple // by argument position, nil until first use
}

func newRelation(name string, arity int) *relation {
	return &relation{name: name, arity: arity, seen: map[string]bool{}}
}

func tupleKey(t Tuple) string { return strings.Join(t, "\x00") }

// add returns true when the tuple was new.
func (r *relation) add(t Tuple) bool {
	k := tupleKey(t)
	if r.seen[k] {
		return false
	}
	r.seen[k] = true
	r.tuples = append(r.tuples, t)
	r.idx = nil // invalidated; rebuilt on next indexed lookup
	return true
}

// selectCands returns the candidate bucket to scan for a call pattern: the
// smallest per-argument index bucket among the bound positions, or every tuple
// when the pattern is fully free. binds[i] == nil means argument i is free.
func (r *relation) selectCands(binds []*string) []Tuple {
	best, bestN := -1, -1
	for i, b := range binds {
		if b == nil {
			continue
		}
		r.buildIndex()
		n := len(r.idx[i][*b])
		if best < 0 || n < bestN {
			best, bestN = i, n
		}
	}
	if best >= 0 {
		return r.idx[best][*binds[best]]
	}
	return r.tuples
}

// exists reports whether any tuple is consistent with the call pattern,
// allocating nothing. Every negated literal and every base-union probe wants
// this and not a slice — it was 61% of allocation before the split
// (docs/datalog-engine-performance-plan.md §1.2, P.2).
func (r *relation) exists(binds []*string) bool {
	if r == nil {
		return false
	}
	for _, t := range r.selectCands(binds) {
		if matches(t, binds) {
			return true
		}
	}
	return false
}

// each calls fn for every tuple consistent with the call pattern, stopping
// early when fn returns false. No result slice is built; the join consumes
// tuples one at a time.
func (r *relation) each(binds []*string, fn func(Tuple) bool) {
	if r == nil {
		return
	}
	for _, t := range r.selectCands(binds) {
		if matches(t, binds) {
			if !fn(t) {
				return
			}
		}
	}
}

// match returns the tuples consistent with a call pattern. Kept for Query's
// final answer, where the slice is the product rather than an intermediate.
func (r *relation) match(binds []*string) []Tuple {
	if r == nil {
		return nil
	}
	var out []Tuple
	for _, t := range r.selectCands(binds) {
		if matches(t, binds) {
			out = append(out, t)
		}
	}
	return out
}

func (r *relation) buildIndex() {
	if r.idx != nil {
		return
	}
	r.idx = make([]map[string][]Tuple, r.arity)
	for i := range r.idx {
		r.idx[i] = map[string][]Tuple{}
	}
	for _, t := range r.tuples {
		for i := 0; i < r.arity && i < len(t); i++ {
			r.idx[i][t[i]] = append(r.idx[i][t[i]], t)
		}
	}
}

// tableState is where a memo table sits in its lifecycle. It replaces the old
// global round counter: a subgoal reached twice is recognised by its own
// state, not by comparing a mutable integer that any nested fixpoint could bump
// (docs/datalog-engine-performance-plan.md §1.1).
type tableState int

const (
	// tableDirty: never produced, or produced in an earlier fixpoint round and
	// due to be re-derived this round.
	tableDirty tableState = iota
	// tableProducing: on the current production stack. A recursive reach
	// returns the partial tuples rather than recursing — this is the tabling
	// half of top-down-with-tabling, and the recursion guard.
	tableProducing
	// tableComplete: a full fixpoint produced nothing new, so the tuple set is
	// final. Returned as-is, which is what makes a negated subgoal — always in
	// a strictly lower stratum — safe to read.
	tableComplete
)

// table is the memo for one subgoal: a relation plus its lifecycle state.
type table struct {
	rel   *relation
	state tableState
}

// Engine holds base facts, rules and the memo tables from evaluation.
type Engine struct {
	opts Options

	base  map[string]*relation
	rules map[string][]*Rule
	// ruleOrder keeps rules in file order so evaluation, and therefore
	// derivation output, is deterministic.
	ruleOrder []*Rule
	strata    map[string]int

	tables    map[string]*table
	evalDepth int   // nested solveComplete calls in flight; 0 => a top-level fixpoint
	grown     int64 // incremented on every new tuple; the fixpoint signal

	// derivs records every way a tuple was derived, keyed relation+tuple.
	// Deduped, because a rule re-fires on every fixpoint round.
	derivs   map[string][]Derivation
	derivSet map[string]bool

	loaded bool // rules stratified; set on first Query
	err    error
}

// New builds an engine. The zero Options value means demand-driven evaluation
// with the default caps.
func New(opts Options) *Engine {
	if !opts.DemandDriven && opts.MaxTuples == 0 && opts.MaxRounds == 0 {
		// Zero value: promote to the documented default rather than silently
		// selecting the debugging mode.
		opts.DemandDriven = true
	}
	return &Engine{
		opts:     opts.withDefaults(),
		base:     map[string]*relation{},
		rules:    map[string][]*Rule{},
		tables:   map[string]*table{},
		derivs:   map[string][]Derivation{},
		derivSet: map[string]bool{},
	}
}

// Assert loads base facts. Must be called before Run for every relation a rule
// references; an unknown relation is an error, never an empty set — a typo'd
// relation name that silently yields nothing is the single most common failure
// mode of a declarative layer. Assert with no tuples declares the relation
// empty, which is how an emitter says "this really is empty here".
//
// Arity is taken from the first tuple; a later tuple of a different length is
// an error. A relation declared with no tuples takes its arity from the rules.
func (e *Engine) Assert(rel string, tuples []Tuple) error {
	if rel == "" {
		return fmt.Errorf("datalog: Assert with an empty relation name")
	}
	r := e.base[rel]
	if r == nil {
		arity := -1
		if len(tuples) > 0 {
			arity = len(tuples[0])
		}
		r = newRelation(rel, arity)
		e.base[rel] = r
	}
	for _, t := range tuples {
		if r.arity < 0 {
			r.arity = len(t)
		}
		if len(t) != r.arity {
			return fmt.Errorf("datalog: relation %s has arity %d, got a tuple of %d terms %v", rel, r.arity, len(t), t)
		}
		if r.add(append(Tuple(nil), t...)) && len(r.tuples) > e.opts.MaxTuples {
			return fmt.Errorf("datalog: relation %s exceeded MaxTuples (%d)", rel, e.opts.MaxTuples)
		}
	}
	// New facts invalidate everything derived from them.
	e.reset()
	return nil
}

// LoadRules parses a rule file. Rules are stratified on load; a program with
// negation through a recursive cycle is rejected with the cycle named.
func (e *Engine) LoadRules(src []byte, name string) error {
	rules, err := parseRules(src, name)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if err := checkSafe(r); err != nil {
			return err
		}
	}
	all := append(append([]*Rule{}, e.ruleOrder...), rules...)

	// Arity agreement across every mention, head or body. A rule referring to
	// `filter_reg(C)` when the emitter asserts `filter_reg(C, R)` is a typo
	// that would otherwise evaluate to nothing at all.
	arity := map[string]int{}
	for rel, r := range e.base {
		if r.arity >= 0 {
			arity[rel] = r.arity
		}
	}
	for _, r := range all {
		for _, l := range append([]Literal{r.Head}, r.Body...) {
			if a, seen := arity[l.Rel]; seen && a != l.arity() {
				return fmt.Errorf("%s:%d: %s is used with arity %d here and %d elsewhere", r.File, r.Line, l.Rel, l.arity(), a)
			}
			arity[l.Rel] = l.arity()
		}
	}

	strata, err := stratify(all)
	if err != nil {
		return err
	}

	// Every body relation must be a rule head or an asserted relation.
	heads := map[string]bool{}
	for _, r := range all {
		heads[r.Head.Rel] = true
	}
	var missing []string
	for _, r := range all {
		for _, l := range r.Body {
			if heads[l.Rel] {
				continue
			}
			if _, ok := e.base[l.Rel]; !ok {
				missing = append(missing, fmt.Sprintf("%s (in %s at %s:%d)", l.Rel, r.Name, r.File, r.Line))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("datalog: %s references relations that are neither asserted nor derived: %s",
			name, strings.Join(dedupe(missing), ", "))
	}

	// An emitter that asserts an empty relation ("this service really has no
	// skips") gives no arity; the rules do. Backfill it, or a Query on that
	// relation builds a pattern of -1 terms.
	for rel, r := range e.base {
		if r.arity < 0 {
			if a, ok := arity[rel]; ok {
				r.arity = a
			}
		}
	}

	e.ruleOrder = all
	e.rules = map[string][]*Rule{}
	for _, r := range all {
		e.rules[r.Head.Rel] = append(e.rules[r.Head.Rel], r)
	}
	e.strata = strata
	e.loaded = true
	e.reset()
	return nil
}

// Relations reports every relation the engine knows, base and derived, sorted.
// Used by the emitter's own tests and by `polyflow explain` to check that a
// rule file and the facts an emitter asserts still agree.
func (e *Engine) Relations() []Relation {
	seen := map[string]int{}
	for name, r := range e.base {
		seen[name] = r.arity
	}
	for _, r := range e.ruleOrder {
		seen[r.Head.Rel] = r.Head.arity()
	}
	out := make([]Relation, 0, len(seen))
	for name, a := range seen {
		out = append(out, Relation{Name: name, Arity: a})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *Engine) reset() {
	e.tables = map[string]*table{}
	e.evalDepth = 0
	e.grown = 0
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	var last string
	for i, s := range in {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}
