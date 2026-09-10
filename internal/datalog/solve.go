package datalog

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Query evaluates demand-driven from the named goal, with every argument free.
// Bottom-up materialization of every derived relation is NOT the default: see
// Options and QueryPattern.
func (e *Engine) Query(goal string) ([]Tuple, error) {
	a, err := e.arityOf(goal)
	if err != nil {
		return nil, err
	}
	return e.QueryPattern(goal, make([]string, a))
}

// QueryPattern is Query with a call pattern: a non-empty entry binds that
// argument, an empty string leaves it free. This is what makes DemandDriven
// worth having — a bound pattern evaluates only the subgoals that pattern
// demands, instead of the whole relation.
//
// The returned tuples are sorted, because a caller that turns them into graph
// edges must not inherit map iteration order (bug-class #2).
func (e *Engine) QueryPattern(goal string, pattern []string) ([]Tuple, error) {
	if err := e.ready(goal, len(pattern)); err != nil {
		return nil, err
	}
	binds := bindsOf(pattern)
	// An unbound whole-relation goal has no demand to exploit, so it takes the
	// stratified bottom-up path (P.5); a bound one keeps the tabled evaluator,
	// which is where demand-driven evaluation genuinely pays.
	solve := e.solveComplete
	if e.useBottomUp(goal, binds) {
		solve = func(goal string, _ []*string) ([]Tuple, error) { return e.solveBottomUp(goal) }
	}
	out, err := solve(goal, binds)
	if err != nil {
		return nil, err
	}
	res := make([]Tuple, len(out))
	copy(res, out)
	sortTuples(res)
	return res, nil
}

// Provenance returns, for one derived tuple, the rule name and the base tuples
// that derived it. Required by SA.1; it is built in from the start, because
// retrofitting derivation tracking means re-running evaluation. Every
// derivation is returned when a tuple is derivable more than one way — they are
// not collapsed to the first, because "which rule produced this false edge" is
// the question this method exists to answer.
func (e *Engine) Provenance(goal string, t Tuple) ([]Derivation, error) {
	if err := e.ready(goal, len(t)); err != nil {
		return nil, err
	}
	pattern := make([]string, len(t))
	copy(pattern, t)
	binds := bindsOf(pattern)

	// Bulk evaluation runs with recording off (P.6). Recompute this one ground
	// goal with recording on: it is a fully bound query, so it touches only the
	// derivations of the tuple asked about and costs microseconds. Re-derive
	// even if a previous bound query already completed this subgoal — that run
	// may have had recording off too.
	prev := e.recordProv
	e.recordProv = true
	defer func() { e.recordProv = prev }()
	if tb := e.tables[e.subgoalKey(goal, binds)]; tb != nil && tb.state == tableComplete && len(e.derivs[e.groupKey(goal, t)]) == 0 {
		tb.state = tableDirty
	}

	if _, err := e.solveComplete(goal, binds); err != nil {
		return nil, err
	}
	ds := e.derivs[e.groupKey(goal, t)]
	if len(ds) == 0 {
		if r, ok := e.base[goal]; ok && r.has(t) {
			// A base fact has no derivation and that is the answer, not a
			// failure: the walk back stops here.
			return nil, nil
		}
		return nil, fmt.Errorf("datalog: %s%v is not derivable", goal, []string(t))
	}
	out := append([]Derivation(nil), ds...)
	sort.SliceStable(out, func(i, j int) bool { return derivKey(out[i]) < derivKey(out[j]) })
	return out, nil
}

func (e *Engine) ready(goal string, arity int) error {
	if !e.loaded && len(e.rules) == 0 {
		if _, ok := e.base[goal]; !ok {
			return fmt.Errorf("datalog: no rules loaded and %s was never asserted", goal)
		}
	}
	want, err := e.arityOf(goal)
	if err != nil {
		return err
	}
	if want >= 0 && arity != want {
		return fmt.Errorf("datalog: %s has arity %d, got a pattern of %d terms", goal, want, arity)
	}
	return nil
}

func (e *Engine) arityOf(goal string) (int, error) {
	if rs := e.rules[goal]; len(rs) > 0 {
		return rs[0].Head.arity(), nil
	}
	if r, ok := e.base[goal]; ok {
		return r.arity, nil
	}
	return 0, fmt.Errorf("datalog: unknown relation %q — it is neither asserted nor the head of a rule", goal)
}

func bindsOf(pattern []string) []*string {
	binds := make([]*string, len(pattern))
	for i := range pattern {
		if pattern[i] != "" {
			v := pattern[i]
			binds[i] = &v
		}
	}
	return binds
}

// roundState is the per-round memo of one fixpoint: the set of subgoals already
// evaluated this round, so a subgoal reached twice in one round is not
// recomputed. It is owned by the solveComplete invocation that created it — a
// nested fixpoint (from solveNegated) gets its own, so nothing a negated
// relation's evaluation does can invalidate the outer fixpoint's memo. That
// cross-fixpoint interference, via a shared global counter, was the defect in
// docs/datalog-engine-performance-plan.md §1.1.
type roundState struct {
	seen map[sgKey]bool
}

// solveComplete drives the fixpoint for one subgoal: re-evaluate until a whole
// round adds no tuple anywhere. Datalog is monotone and the domain is finite,
// so this terminates; MaxRounds only catches a bug in the engine itself.
func (e *Engine) solveComplete(rel string, binds []*string) ([]Tuple, error) {
	topLevel := e.evalDepth == 0
	e.evalDepth++
	defer func() { e.evalDepth-- }()

	var out []Tuple
	for i := 0; i < e.opts.MaxRounds; i++ {
		before := e.grown
		rs := &roundState{seen: map[sgKey]bool{}}
		var err error
		out, err = e.solve(rs, rel, binds)
		if err != nil {
			return nil, err
		}
		if e.grown == before {
			if topLevel {
				// A global fixpoint was reached with nothing in flight, so
				// every table populated along the way is final. Marking them
				// lets a second Query reuse the work instead of re-deriving it.
				for _, t := range e.tables {
					t.state = tableComplete
				}
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("datalog: %s did not reach a fixpoint in %d rounds", rel, e.opts.MaxRounds)
}

// solve answers one subgoal, returning whatever is known now. A subgoal already
// in progress returns its partial answers rather than recursing — the caller's
// fixpoint loop is what turns that into the complete set. This is the tabling
// half of "top-down with tabling"; the call-pattern key is the demand half.
func (e *Engine) solve(rs *roundState, rel string, binds []*string) ([]Tuple, error) {
	rules := e.rules[rel]
	baseRel, hasBase := e.base[rel]
	if len(rules) == 0 {
		if !hasBase {
			return nil, fmt.Errorf("datalog: unknown relation %q", rel)
		}
		return baseRel.match(binds), nil
	}

	key := e.subgoalKey(rel, binds)
	t := e.tables[key]
	if t == nil {
		t = &table{rel: newRelation(rel, len(binds), e.syms)}
		e.tables[key] = t
	}
	if t.state == tableComplete || t.state == tableProducing || rs.seen[key] {
		return t.rel.tuples, nil
	}
	rs.seen[key] = true
	t.state = tableProducing
	defer func() {
		if t.state == tableProducing {
			t.state = tableDirty
		}
	}()

	// A relation can be both asserted and derived; the answer is the union.
	if hasBase {
		var rerr error
		baseRel.each(binds, func(tup Tuple) bool {
			if err := e.record(t, tup, nil); err != nil {
				rerr = err
				return false
			}
			return true
		})
		if rerr != nil {
			return nil, rerr
		}
	}
	if !e.opts.DemandDriven {
		// Debugging mode: ignore the call pattern and materialize the whole
		// relation, then filter. Never the default — this is what makes a
		// closure over a large ancestor set explode.
		free := make([]*string, len(binds))
		if e.subgoalKey(rel, free) != key {
			all, err := e.solve(rs, rel, free)
			if err != nil {
				return nil, err
			}
			for _, tup := range all {
				if matches(tup, binds) {
					if err := e.record(t, tup, nil); err != nil {
						return nil, err
					}
				}
			}
			return t.rel.tuples, nil
		}
	}
	src := topDown{e: e, rs: rs}
	for _, r := range rules {
		if err := e.evalRule(src, r, binds, t); err != nil {
			return nil, err
		}
	}
	return t.rel.tuples, nil
}

// solveNegated decides `not rel(...)`. It is the one place the engine gives up
// demand-driven evaluation on purpose, and the reason is worth stating.
//
// A negation must read a *complete* relation. Reading partial answers would let
// `not p(x)` succeed merely because p had not finished — the one wrong answer a
// negation must never give. Completing a *bound* subgoal per candidate row
// would mean one fixpoint per row, which is quadratic on exactly the joins this
// tier exists to speed up. So the negated relation is materialized once,
// unbound, and every later check probes that table.
//
// This is safe because the program is stratified. Every relation on the active
// production stack sits at a stratum >= the head being proved, and a negated
// goal sits strictly below it, so the negated goal's dependency cone cannot
// contain anything currently in flight — nothing partial can leak in, and the
// table is genuinely final once its own fixpoint settles.
func (e *Engine) solveNegated(rel string, binds []*string) (bool, error) {
	// A relation with no rules is complete the moment it was asserted — there is
	// no fixpoint that could add to it. reg_only, reg_except, action and skip_total
	// are all base relations negated in hot rules, and running solveComplete for
	// each check does two expensive things for nothing: it walks a full (empty)
	// fixpoint loop, and it bumps the global round counter, invalidating the
	// per-round memo for every table in the engine (docs/datalog-engine-performance-plan.md
	// §1.1). Probe the facts directly instead.
	if len(e.rules[rel]) == 0 {
		br := e.base[rel]
		if br == nil {
			return false, fmt.Errorf("datalog: unknown relation %q", rel)
		}
		return br.exists(binds), nil
	}
	free := make([]*string, len(binds))
	key := e.subgoalKey(rel, free)
	if t := e.tables[key]; t != nil && t.state == tableComplete {
		return t.rel.exists(binds), nil
	}
	if _, err := e.solveComplete(rel, free); err != nil {
		return false, err
	}
	if t := e.tables[key]; t != nil {
		t.state = tableComplete
		return t.rel.exists(binds), nil
	}
	// A purely base relation has no table; probe the facts directly.
	return e.base[rel].exists(binds), nil
}

// rulePatterns is the reusable set of sub-call patterns for one rule body, one
// per body literal. join mutates the variable positions in place as bindings
// change instead of allocating a fresh []*string per literal per candidate row
// (docs/datalog-engine-performance-plan.md §1.2, P.2). Each recursion depth owns
// its own entry, and a deeper literal is fully evaluated before the loop at a
// shallower depth rewrites its pattern, so in-place mutation is safe.
type rulePatterns struct {
	ptr  [][]*string // ptr[i][k] is nil (free) or points into vals[i]
	vals [][]string  // stable backing storage the pointers alias
	head []string    // scratch head tuple, rebuilt per firing, cloned only on insert (D.3)
}

func newRulePatterns(r *Rule) *rulePatterns {
	rp := &rulePatterns{
		ptr:  make([][]*string, len(r.Body)),
		vals: make([][]string, len(r.Body)),
		head: make([]string, len(r.Head.Args)),
	}
	for bi, lit := range r.Body {
		rp.vals[bi] = make([]string, len(lit.Args))
		rp.ptr[bi] = make([]*string, len(lit.Args))
		for k, term := range lit.Args {
			if !term.IsVar {
				rp.vals[bi][k] = term.Name
				rp.ptr[bi][k] = &rp.vals[bi][k]
			}
		}
	}
	return rp
}

// litPlan is one body literal's relation resolved once for the life of the rule
// (docs/datalog-engine-performance-plan.md D.9). `join`, `topDown.litTuples` and
// `bottomUp.litTuples` all used to classify a literal by string every firing —
// `e.base[rel]`, `len(e.rules[rel])`, `s.full[rel]` — and `mapaccess_faststr`
// was ~23% of a bulk pass on keys that never change for a rule.
//
// derived (has rules) is stratification-stable; base never moves once Assert
// creates it. buSrc is the one handle that is mode-specific and mutable: the
// bottom-up materialized relation to read at every non-delta position. It is
// primed by evalComponent before its rounds and read only inside that
// solveBottomUp; the top-down path leaves it nil and never consults it.
type litPlan struct {
	rel     string
	neg     bool
	derived bool
	base    *relation // e.base[rel], nil when the relation has no asserted facts
	buSrc   *relation // bottom-up only: the full materialized relation for this literal
}

// planBody builds and memoizes r.plan. Idempotent; reset clears it when the
// classification could have changed.
func (e *Engine) planBody(r *Rule) []litPlan {
	if r.plan != nil {
		return r.plan
	}
	p := make([]litPlan, len(r.Body))
	for i, l := range r.Body {
		p[i] = litPlan{
			rel:     l.Rel,
			neg:     l.Neg,
			derived: len(e.rules[l.Rel]) > 0,
			base:    e.base[l.Rel],
		}
	}
	r.plan = p
	return p
}

// frame is a rule's binding environment: a variable's value is frame.val[Var]
// and set records which slots are bound (Var < 64, checked at load). It
// replaces the `env map[string]string` that evalRule allocated per call and the
// `map...faststr` hashing on every body-literal binding
// (docs/datalog-engine-performance-plan.md D.2). One []string alloc per
// evalRule, and the set/unwind is bit ops.
type frame struct {
	val []string
	set uint64
}

func (f *frame) get(id int) (string, bool) { return f.val[id], f.set&(1<<uint(id)) != 0 }
func (f *frame) put(id int, v string)      { f.val[id] = v; f.set |= 1 << uint(id) }

// evalRule proves one rule against a call pattern, left to right. src decides
// where a body literal's tuples come from — the tabled evaluator or a
// materialized stratum — and is the only thing the two evaluation modes differ
// in (see bottomup.go).
func (e *Engine) evalRule(src bodySource, r *Rule, binds []*string, t *table) error {
	fr := frame{val: make([]string, r.NVars)}
	for i, term := range r.Head.Args {
		b := binds[i]
		if b == nil {
			continue
		}
		if !term.IsVar {
			if term.Name != *b {
				return nil
			}
			continue
		}
		if v, seen := fr.get(term.Var); seen {
			if v != *b {
				return nil
			}
			continue
		}
		fr.put(term.Var, *b)
	}
	var steps []DerivationStep
	if e.recordProv {
		steps = make([]DerivationStep, 0, len(r.Body))
	}

	// Reuse the rule's cached patterns; hand a re-entrant call (a recursive rule
	// solved top-down) a fresh set so it can't corrupt the outer join's vals (D.4).
	var rp *rulePatterns
	if r.patBusy {
		rp = newRulePatterns(r)
	} else {
		if r.pat == nil {
			r.pat = newRulePatterns(r)
		}
		rp = r.pat
		r.patBusy = true
		defer func() { r.patBusy = false }()
	}
	return e.join(src, r, rp, e.planBody(r), 0, &fr, binds, t, steps)
}

func (e *Engine) join(src bodySource, r *Rule, rp *rulePatterns, plan []litPlan, i int, fr *frame, binds []*string, t *table, steps []DerivationStep) error {
	if i == len(r.Body) {
		// Reuse the per-rule scratch head instead of make(Tuple, …) per firing;
		// record clones it only when the tuple is genuinely new (D.3). The
		// terminal case does not recurse, so nothing overwrites rp.head between
		// here and record.
		head := Tuple(rp.head)
		for k, term := range r.Head.Args {
			if term.IsVar {
				v, ok := fr.get(term.Var)
				if !ok {
					// checkSafe rules this out at load; a hit here is an engine bug.
					return fmt.Errorf("datalog: rule %s (%s:%d) left head variable %s unbound",
						r.Name, r.File, r.Line, term.Name)
				}
				head[k] = v
			} else {
				head[k] = term.Name
			}
		}
		if !matches(head, binds) {
			return nil
		}
		var d *Derivation
		if e.recordProv {
			// The Derivation retains its Head, so it gets a stable copy — the
			// scratch buffer is about to be reused. Off the hot path by default.
			hc := append(Tuple(nil), head...)
			d = &Derivation{Rule: r.Name, Head: hc, Body: append([]DerivationStep(nil), steps...)}
			return e.record(t, hc, d)
		}
		return e.record(t, head, d)
	}

	lit := r.Body[i]
	lp := &plan[i]
	sub := rp.ptr[i]
	for k, term := range lit.Args {
		if !term.IsVar {
			continue
		}
		if v, ok := fr.get(term.Var); ok {
			rp.vals[i][k] = v
			sub[k] = &rp.vals[i][k]
		} else {
			sub[k] = nil
		}
	}

	if lit.Neg {
		found, err := src.negated(lp, sub)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		return e.join(src, r, rp, plan, i+1, fr, binds, t, steps)
	}

	isBase := !lp.derived && lp.base != nil
	rec := e.recordProv

	// Iterate the candidate slice directly instead of handing eachLit a callback.
	// The callback was a fresh func literal per body literal per candidate scan,
	// and because it crossed the bodySource interface it escaped to the heap
	// along with the `joinErr` it closed over — together 58% of this benchmark's
	// allocation (docs/datalog-engine-performance-plan.md §6.5/§6.6). Pulling the
	// tuples lets the recursion return its error straight up the stack.
	tuples, err := src.litTuples(i, lp, sub)
	if err != nil {
		return err
	}
	for _, tup := range tuples {
		var added uint64
		ok := true
		for k, term := range lit.Args {
			if !term.IsVar {
				// litTuples pre-filters at most one bound position; a constant
				// here is checked as the binding happens, exactly as matches did
				// inside the old each.
				if term.Name != tup[k] {
					ok = false
					break
				}
				continue
			}
			if v, seen := fr.get(term.Var); seen {
				if v != tup[k] {
					ok = false
					break
				}
				continue
			}
			fr.put(term.Var, tup[k])
			added |= 1 << uint(term.Var)
		}
		if ok {
			next := steps
			if rec {
				next = append(steps, DerivationStep{Relation: lit.Rel, Tuple: tup, Base: isBase})
			}
			if e2 := e.join(src, r, rp, plan, i+1, fr, binds, t, next); e2 != nil {
				fr.set &^= added
				return e2
			}
		}
		fr.set &^= added
	}
	return nil
}

func (e *Engine) record(t *table, tup Tuple, d *Derivation) error {
	if t.rel.add(tup) {
		e.grown++
		if len(t.rel.tuples) > e.opts.MaxTuples {
			return fmt.Errorf("datalog: relation %s exceeded MaxTuples (%d)", t.rel.name, e.opts.MaxTuples)
		}
	}
	if d != nil && e.recordProv {
		k := e.derivKeyFast(*d)
		if !e.derivSet[k] {
			e.derivSet[k] = true
			gk := e.groupKey(t.rel.name, tup)
			e.derivs[gk] = append(e.derivs[gk], *d)
		}
	}
	return nil
}

// dgKey groups derivations by the tuple they explain. Interned, so it costs no
// string concatenation on the hot path (docs/datalog-engine-performance-plan.md P.3).
type dgKey struct {
	rel string
	k   tkey
}

func (e *Engine) groupKey(rel string, t Tuple) dgKey { return dgKey{rel: rel, k: e.syms.key(t)} }

// derivKeyFast is the dedup key for a derivation on the hot path: interned
// symbols packed four bytes each, one allocation for the final string instead
// of a strings.Join per body tuple. Order is not meaningful here — dedup only —
// so leaking interning order into it is harmless; the Provenance sort still
// uses the string-ordered derivKey.
func (e *Engine) derivKeyFast(d Derivation) string {
	var sb strings.Builder
	sb.Grow(4 + 4*len(d.Head) + len(d.Body)*(4+16))
	e.writeSym(&sb, d.Rule)
	e.writeSyms(&sb, d.Head)
	for _, b := range d.Body {
		e.writeSym(&sb, b.Relation)
		e.writeSyms(&sb, b.Tuple)
	}
	return sb.String()
}

func (e *Engine) writeSym(sb *strings.Builder, s string) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], e.syms.id(s))
	sb.Write(b[:])
}

func (e *Engine) writeSyms(sb *strings.Builder, t Tuple) {
	for _, s := range t {
		e.writeSym(sb, s)
	}
}

func derivKey(d Derivation) string {
	var sb strings.Builder
	sb.WriteString(d.Rule)
	sb.WriteByte('\x01')
	sb.WriteString(tupleKey(d.Head))
	for _, b := range d.Body {
		sb.WriteByte('\x02')
		sb.WriteString(b.Relation)
		sb.WriteByte('\x03')
		sb.WriteString(tupleKey(b.Tuple))
	}
	return sb.String()
}

func matches(t Tuple, binds []*string) bool {
	for i, b := range binds {
		if b != nil && t[i] != *b {
			return false
		}
	}
	return true
}

// sortTuples orders a Query result. It compares tuples element by element
// instead of joining each into a "\x00"-separated key: the old
// sort.SliceStable(tupleKey(i) < tupleKey(j)) allocated two []byte per
// comparison — 42% of BenchmarkRailsFilters' allocation objects for a step
// nobody profiles as hot (docs/datalog-engine-performance-plan.md §6 Phase A).
// Every tuple in one relation's result has the same arity and they are distinct
// (deduped by relation.seen), so an unstable element-wise sort is both
// allocation-free and deterministic.
func sortTuples(ts []Tuple) {
	slices.SortFunc(ts, compareTuples)
}

func compareTuples(a, b Tuple) int {
	n := min(len(a), len(b))
	for k := 0; k < n; k++ {
		if a[k] != b[k] {
			if a[k] < b[k] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}
