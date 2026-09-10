package datalog

import (
	"fmt"
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
	out, err := e.solveComplete(goal, binds)
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
	if _, err := e.solveComplete(goal, bindsOf(pattern)); err != nil {
		return nil, err
	}
	ds := e.derivs[goal+"\x01"+tupleKey(t)]
	if len(ds) == 0 {
		if _, ok := e.base[goal]; ok && e.base[goal].seen[tupleKey(t)] {
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

func subgoalKey(rel string, binds []*string) string {
	var sb strings.Builder
	sb.WriteString(rel)
	for _, b := range binds {
		sb.WriteByte('\x01')
		if b != nil {
			sb.WriteString(*b)
		} else {
			sb.WriteByte('*')
		}
	}
	return sb.String()
}

// roundState is the per-round memo of one fixpoint: the set of subgoals already
// evaluated this round, so a subgoal reached twice in one round is not
// recomputed. It is owned by the solveComplete invocation that created it — a
// nested fixpoint (from solveNegated) gets its own, so nothing a negated
// relation's evaluation does can invalidate the outer fixpoint's memo. That
// cross-fixpoint interference, via a shared global counter, was the defect in
// docs/datalog-engine-performance-plan.md §1.1.
type roundState struct {
	seen map[string]bool
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
		rs := &roundState{seen: map[string]bool{}}
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

	key := subgoalKey(rel, binds)
	t := e.tables[key]
	if t == nil {
		t = &table{rel: newRelation(rel, len(binds))}
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
		for _, tup := range baseRel.match(binds) {
			if err := e.record(t, tup, nil); err != nil {
				return nil, err
			}
		}
	}
	if !e.opts.DemandDriven {
		// Debugging mode: ignore the call pattern and materialize the whole
		// relation, then filter. Never the default — this is what makes a
		// closure over a large ancestor set explode.
		free := make([]*string, len(binds))
		if subgoalKey(rel, free) != key {
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
	for _, r := range rules {
		if err := e.evalRule(rs, r, binds, t); err != nil {
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
		return len(br.match(binds)) > 0, nil
	}
	free := make([]*string, len(binds))
	key := subgoalKey(rel, free)
	if t := e.tables[key]; t != nil && t.state == tableComplete {
		return len(t.rel.match(binds)) > 0, nil
	}
	if _, err := e.solveComplete(rel, free); err != nil {
		return false, err
	}
	if t := e.tables[key]; t != nil {
		t.state = tableComplete
		return len(t.rel.match(binds)) > 0, nil
	}
	// A purely base relation has no table; probe the facts directly.
	return len(e.base[rel].match(binds)) > 0, nil
}

// evalRule proves one rule against a call pattern, left to right.
func (e *Engine) evalRule(rs *roundState, r *Rule, binds []*string, t *table) error {
	env := map[string]string{}
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
		if v, seen := env[term.Name]; seen {
			if v != *b {
				return nil
			}
			continue
		}
		env[term.Name] = *b
	}
	steps := make([]DerivationStep, 0, len(r.Body))
	return e.join(rs, r, 0, env, binds, t, steps)
}

func (e *Engine) join(rs *roundState, r *Rule, i int, env map[string]string, binds []*string, t *table, steps []DerivationStep) error {
	if i == len(r.Body) {
		head := make(Tuple, len(r.Head.Args))
		for k, term := range r.Head.Args {
			if term.IsVar {
				v, ok := env[term.Name]
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
		return e.record(t, head, &Derivation{Rule: r.Name, Head: head, Body: append([]DerivationStep(nil), steps...)})
	}

	lit := r.Body[i]
	sub := make([]*string, len(lit.Args))
	for k, term := range lit.Args {
		if !term.IsVar {
			v := term.Name
			sub[k] = &v
			continue
		}
		if v, ok := env[term.Name]; ok {
			vv := v
			sub[k] = &vv
		}
	}

	if lit.Neg {
		found, err := e.solveNegated(lit.Rel, sub)
		if err != nil {
			return err
		}
		if found {
			return nil
		}
		return e.join(rs, r, i+1, env, binds, t, steps)
	}

	tuples, err := e.solve(rs, lit.Rel, sub)
	if err != nil {
		return err
	}
	_, isBase := e.base[lit.Rel]
	isBase = isBase && len(e.rules[lit.Rel]) == 0
	for _, tup := range tuples {
		var added []string
		ok := true
		for k, term := range lit.Args {
			if !term.IsVar {
				continue
			}
			if v, seen := env[term.Name]; seen {
				if v != tup[k] {
					ok = false
					break
				}
				continue
			}
			env[term.Name] = tup[k]
			added = append(added, term.Name)
		}
		if ok {
			step := DerivationStep{Relation: lit.Rel, Tuple: tup, Base: isBase}
			if err := e.join(rs, r, i+1, env, binds, t, append(steps, step)); err != nil {
				return err
			}
		}
		for _, name := range added {
			delete(env, name)
		}
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
	if d != nil {
		k := derivKey(*d)
		if !e.derivSet[k] {
			e.derivSet[k] = true
			gk := t.rel.name + "\x01" + tupleKey(tup)
			e.derivs[gk] = append(e.derivs[gk], *d)
		}
	}
	return nil
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

func sortTuples(ts []Tuple) {
	sort.SliceStable(ts, func(i, j int) bool { return tupleKey(ts[i]) < tupleKey(ts[j]) })
}
