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
//   - **Demand-driven where there is demand.** A *bound* query is evaluated
//     top-down with tabling, so asking for one class's filters does not
//     materialize the closure for every class. An unbound whole-relation goal
//     has no demand to prune with, and takes stratified bottom-up evaluation
//     with semi-naive deltas instead (bottomup.go, Tier DL.3 / P.5). Both modes
//     share one rule set and one join.
//   - **Derivation tracking from the start.** Provenance answers "which rule
//     produced this tuple, from which base facts" (SA.1). Retrofitting it means
//     re-running evaluation, so it is recorded during evaluation, not after.
//
// The engine is deliberately graph-agnostic — the layer contract in
// internal/arch forbids it importing internal/graph. All terms are strings; the
// emitter that asserts the facts is the only thing that knows what a key means.
package datalog

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

// interner maps a term string to a small dense integer and back. Tuple keys,
// subgoal keys and the fixpoint `seen` set all key on the integers instead of
// on joined strings — string keys were 36% of allocation after P.2
// (docs/datalog-engine-performance-plan.md §1.2, P.3). The public API stays
// []string; interning happens at the relation boundary, which is a dozen
// relations asserted once, not the join.
type interner struct {
	ids  map[string]uint32
	strs []string
}

func newInterner() *interner { return &interner{ids: make(map[string]uint32)} }

func (in *interner) id(s string) uint32 {
	if v, ok := in.ids[s]; ok {
		return v
	}
	v := uint32(len(in.strs))
	in.strs = append(in.strs, s)
	in.ids[s] = v
	return v
}

// tkey is a comparable, allocation-free key for a tuple: the interned symbols
// packed two-per-uint64. Arity <=4 (every relation this engine sees) packs
// exactly and cannot collide; a wider tuple spills its tail into ext.
type tkey struct {
	lo, hi uint64
	ext    string
}

func (in *interner) key(t Tuple) tkey {
	var k tkey
	for i, s := range t {
		id := uint64(in.id(s))
		switch i {
		case 0:
			k.lo |= id << 32
		case 1:
			k.lo |= id
		case 2:
			k.hi |= id << 32
		case 3:
			k.hi |= id
		default:
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], uint32(id))
			k.ext += string(b[:])
		}
	}
	return k
}

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
	// DemandDriven selects top-down evaluation with tabling for a *bound* query,
	// so it computes only the subgoals that pattern needs. Default true (the zero
	// value is promoted by New). With it off, a bound query ignores its call
	// pattern and materializes the whole relation before filtering — which is
	// what makes `registered(C, CB, Kind)` explode across a large ancestor
	// closure, and is available only for debugging.
	//
	// It does not affect an unbound whole-relation goal: there is no demand to
	// exploit there, so that goal always takes the stratified bottom-up path.
	DemandDriven bool

	// MaxTuples caps answers per relation; exceeded => error naming it.
	MaxTuples int
	// MaxRounds caps fixpoint iteration.
	MaxRounds int

	// Provenance records a Derivation for every derived tuple during bulk
	// (whole-relation) evaluation. It defaults off (Tier DL.3 / P.6): derivation
	// recording is ~40% of a bulk pass, it is criterion 4 rather than something
	// any caller reads inline, and Provenance(goal, t) recomputes it on demand
	// for one ground tuple in microseconds. Turn it on only to inspect the
	// derivations of a whole relation at once.
	Provenance bool
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
	in     *interner
	seen   map[tkey]bool
	idx    []map[string][]Tuple // by argument position, nil until first use
}

func newRelation(name string, arity int, in *interner) *relation {
	return &relation{name: name, arity: arity, in: in, seen: map[tkey]bool{}}
}

func tupleKey(t Tuple) string { return strings.Join(t, "\x00") }

// add returns true when the tuple was new.
func (r *relation) add(t Tuple) bool {
	k := r.in.key(t)
	if r.seen[k] {
		return false
	}
	r.seen[k] = true
	// Copy only on insert (docs/datalog-engine-performance-plan.md D.3). The
	// caller may hand us a scratch buffer it reuses per rule firing — join builds
	// the head tuple into rulePatterns.head — so a tuple that is genuinely new is
	// cloned here, at the one point it starts being retained, and a re-derived
	// one costs nothing.
	stored := append(Tuple(nil), t...)
	r.tuples = append(r.tuples, stored)
	if r.idx != nil {
		// The index is only ever added to — relations are monotone within a run —
		// so maintain it in place instead of dropping it and rebuilding every
		// per-argument bucket over every tuple on the next lookup. During a
		// fixpoint that rebuild was per round per relation
		// (docs/datalog-engine-performance-plan.md P.4).
		r.indexTuple(stored)
	}
	return true
}

// indexTuple files one tuple into each per-argument bucket. buildIndex does the
// same over the whole relation; add does it incrementally.
func (r *relation) indexTuple(t Tuple) {
	for i := 0; i < r.arity && i < len(t); i++ {
		r.idx[i][t[i]] = append(r.idx[i][t[i]], t)
	}
}

// has reports whether an exact tuple is present.
func (r *relation) has(t Tuple) bool { return r.seen[r.in.key(t)] }

// selectCands returns the candidate bucket to scan for a call pattern: the
// smallest per-argument index bucket among the bound positions, or every tuple
// when the pattern is fully free. binds[i] == nil means argument i is free.
func (r *relation) selectCands(binds []*string) []Tuple {
	var best []Tuple
	bestN := -1
	for i, b := range binds {
		if b == nil {
			continue
		}
		if r.idx == nil {
			r.buildIndex()
		}
		// Hold the bucket, not its position: the old code fetched r.idx[i][*b]
		// once for len() and then r.idx[best][*binds[best]] again to return it —
		// two string-keyed map probes where one does (§6 Phase A).
		bucket := r.idx[i][*b]
		if bestN < 0 || len(bucket) < bestN {
			best, bestN = bucket, len(bucket)
		}
	}
	if bestN < 0 {
		return r.tuples
	}
	return best
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
		r.indexTuple(t)
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

// sgKey identifies a subgoal — a relation name plus its call pattern — as a
// comparable, allocation-free value. The bound argument symbols are packed
// like tkey; mask records which of the first four positions are bound (so a
// bound symbol 0 is not confused with a free slot), and a wider pattern spills
// its tail into ext. Replaces the strings.Builder key of P.2.
type sgKey struct {
	rel    string
	lo, hi uint64
	mask   uint8
	ext    string
}

func (e *Engine) subgoalKey(rel string, binds []*string) sgKey {
	k := sgKey{rel: rel}
	for i, b := range binds {
		var id uint64
		bound := b != nil
		if bound {
			id = uint64(e.syms.id(*b))
		}
		switch i {
		case 0:
			k.lo |= id << 32
			if bound {
				k.mask |= 1
			}
		case 1:
			k.lo |= id
			if bound {
				k.mask |= 2
			}
		case 2:
			k.hi |= id << 32
			if bound {
				k.mask |= 4
			}
		case 3:
			k.hi |= id
			if bound {
				k.mask |= 8
			}
		default:
			var b [5]byte
			if bound {
				b[0] = 1
				binary.LittleEndian.PutUint32(b[1:], uint32(id))
			}
			k.ext += string(b[:])
		}
	}
	return k
}

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
	// strata is the load-time proof that the program is stratified. Bottom-up
	// evaluation orders by strongly connected component, which refines stratum
	// order (see Engine.cone), so this is not consulted per query — but a nil
	// value means no stratification has been established and no bottom-up
	// evaluation may run.
	strata map[string]int

	syms      *interner
	tables    map[sgKey]*table
	evalDepth int   // nested solveComplete calls in flight; 0 => a top-level fixpoint
	grown     int64 // incremented on every new tuple; the fixpoint signal

	// derivs records every way a tuple was derived, keyed relation+tuple.
	// Deduped, because a rule re-fires on every fixpoint round.
	derivs   map[dgKey][]Derivation
	derivSet map[string]bool
	// recordProv gates derivation recording on the hot path. It tracks
	// opts.Provenance, except Provenance() flips it on for the duration of one
	// bound recompute (P.6).
	recordProv bool

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
		opts:       opts.withDefaults(),
		recordProv: opts.Provenance,
		syms:       newInterner(),
		base:       map[string]*relation{},
		rules:      map[string][]*Rule{},
		tables:     map[sgKey]*table{},
		derivs:     map[dgKey][]Derivation{},
		derivSet:   map[string]bool{},
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
		r = newRelation(rel, arity, e.syms)
		e.base[rel] = r
	}
	for _, t := range tuples {
		if r.arity < 0 {
			r.arity = len(t)
		}
		if len(t) != r.arity {
			return fmt.Errorf("datalog: relation %s has arity %d, got a tuple of %d terms %v", rel, r.arity, len(t), t)
		}
		if r.add(t) && len(r.tuples) > e.opts.MaxTuples {
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
		// The join binding frame tracks which slots are set with a uint64 mask
		// (D.2). No rule this engine sees comes close, but a bad one must fail at
		// load rather than corrupt a binding.
		if r.NVars > 64 {
			return fmt.Errorf("%s:%d: rule %s has %d distinct variables; the engine supports at most 64", r.File, r.Line, r.Name, r.NVars)
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
	e.tables = map[sgKey]*table{}
	e.evalDepth = 0
	e.grown = 0
	// New facts or rules can change a literal's base/derived classification, so
	// the cached body plans (D.9) are no longer trustworthy.
	for _, r := range e.ruleOrder {
		r.plan = nil
	}
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
