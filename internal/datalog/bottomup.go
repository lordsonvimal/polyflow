package datalog

import (
	"fmt"
	"sort"
)

// Tier DL.3 / P.5 — stratum-ordered evaluation with semi-naive deltas.
//
// The emitter's goals are whole relations with every argument free
// (`Query("action_filter")`), and top-down-with-tabling has nothing to exploit
// there: there is no demand to prune with, so the tabled evaluator pays the
// bookkeeping of demand-driven evaluation and gets none of the benefit. Worse,
// a negated literal under that evaluator has to materialize its relation with a
// nested fixpoint, because nothing in the top-down control flow establishes
// that a relation is finished (docs/datalog-engine-performance-plan.md §1.1).
//
// Bottom-up over strata establishes it structurally. A stratum's negated
// relations all sit strictly below it and are therefore complete before it
// begins, so `not p(x)` is an index probe with no fixpoint anywhere in the
// path. Within a stratum, semi-naive evaluation joins a recursive rule against
// the previous round's delta rather than the whole relation, so a round costs
// what is new rather than what is known.
//
// Bound queries (QueryPattern with any argument bound, and Provenance, which
// binds all of them) keep the top-down entry point, which is where
// demand-driven evaluation genuinely pays. Two evaluation modes over one rule
// set is the standard arrangement; both go through the same evalRule/join, and
// differ only in the bodySource below.

// bodySource supplies the tuples satisfying one positive body literal and
// decides one negated literal. It is the *only* thing the two evaluation modes
// differ in: everything about how a rule binds its variables, builds its head
// and records its derivation is shared, because a second copy of that logic is
// exactly where the two modes would silently drift apart.
type bodySource interface {
	// eachLit iterates the tuples satisfying body literal at position pos of the
	// rule being evaluated, stopping early when fn returns false.
	eachLit(pos int, rel string, binds []*string, fn func(Tuple) bool) error
	// negated decides `not rel(binds)`.
	negated(rel string, binds []*string) (bool, error)
}

// topDown is the tabled evaluator: a body literal is another subgoal, and a
// negated one is materialized unbound and probed.
type topDown struct {
	e  *Engine
	rs *roundState
}

func (s topDown) eachLit(_ int, rel string, binds []*string, fn func(Tuple) bool) error {
	return s.e.eachSolved(s.rs, rel, binds, fn)
}

func (s topDown) negated(rel string, binds []*string) (bool, error) {
	return s.e.solveNegated(rel, binds)
}

// bottomUp evaluates against materialized relations. Exactly one body position
// may be redirected to the previous round's delta — that is semi-naive
// evaluation, and `pos` is which position.
type bottomUp struct {
	e *Engine
	// full holds every derived relation materialized so far, including the ones
	// in the stratum currently being produced.
	full map[string]*relation
	// complete records which of those have finished their stratum. Only a
	// complete relation may be read under negation.
	complete map[string]bool
	// delta holds the previous round's new tuples for the current stratum's
	// relations, and pos is the body position that must read them (-1 => every
	// literal reads the full relation, which is what round 0 and every
	// non-recursive rule want).
	delta map[string]*relation
	pos   int
}

// source is the relation a positive body literal reads when it is not the delta
// position: the materialized derived relation if there is one (it already
// includes the base facts of the same name, seeded at stratum start), otherwise
// the asserted facts.
func (s *bottomUp) source(rel string) *relation {
	if r, ok := s.full[rel]; ok {
		return r
	}
	return s.e.base[rel]
}

func (s *bottomUp) eachLit(pos int, rel string, binds []*string, fn func(Tuple) bool) error {
	src := s.source(rel)
	if src == nil {
		return fmt.Errorf("datalog: unknown relation %q", rel)
	}
	if pos == s.pos {
		// Semi-naive: this occurrence reads only what the previous round added.
		src = s.delta[rel]
		if src == nil {
			return nil
		}
	}
	src.each(binds, fn)
	return nil
}

func (s *bottomUp) negated(rel string, binds []*string) (bool, error) {
	if len(s.e.rules[rel]) == 0 {
		// Base relation: complete from the moment it was asserted.
		br := s.e.base[rel]
		if br == nil {
			return false, fmt.Errorf("datalog: unknown relation %q", rel)
		}
		return br.exists(binds), nil
	}
	if !s.complete[rel] {
		// Stratification puts a negated relation strictly below the stratum that
		// reads it, so it has been materialized and finished by now. Reaching
		// here means the stratum order is wrong, and the symptom would be
		// `not p(x)` succeeding merely because p was unfinished — missing tuples,
		// silently. Fail loudly instead (plan §4).
		return false, fmt.Errorf("datalog: internal: negated relation %q is read before its stratum completed", rel)
	}
	return s.full[rel].exists(binds), nil
}

// useBottomUp: an unbound whole-relation goal over a loaded rule set. A bound
// pattern, or a goal that is nothing but asserted facts, does not take this
// path.
func (e *Engine) useBottomUp(goal string, binds []*string) bool {
	if !e.loaded || e.strata == nil || len(e.rules[goal]) == 0 {
		return false
	}
	for _, b := range binds {
		if b != nil {
			return false
		}
	}
	return true
}

// freeTable returns the memo table for a relation's all-free subgoal, creating
// it if needed. It is the same table the top-down evaluator would build for
// that subgoal, and marking it complete is what lets a later bound query — or
// solveNegated — reuse this work instead of re-deriving it.
func (e *Engine) freeTable(rel string, arity int) *table {
	key := e.subgoalKey(rel, make([]*string, arity))
	t := e.tables[key]
	if t == nil {
		t = &table{rel: newRelation(rel, arity, e.syms)}
		e.tables[key] = t
	}
	return t
}

// solveBottomUp materializes goal by evaluating its dependency cone one
// strongly connected component at a time, dependencies first. It returns the
// goal's tuples in derivation order; the caller sorts.
func (e *Engine) solveBottomUp(goal string) ([]Tuple, error) {
	bu := &bottomUp{
		e:        e,
		full:     map[string]*relation{},
		complete: map[string]bool{},
		pos:      -1,
	}
	for _, comp := range e.cone(goal) {
		if err := e.evalComponent(bu, comp); err != nil {
			return nil, err
		}
	}
	arity, err := e.arityOf(goal)
	if err != nil {
		return nil, err
	}
	return e.freeTable(goal, arity).rel.tuples, nil
}

// cone returns every derived relation goal depends on, itself included, grouped
// into strongly connected components and ordered so that a component appears
// after every component it reads.
//
// This is the stratum order the plan asks for, refined. Relations inside a
// recursive cycle all carry the same stratum (they depend on each other
// positively, since a negative edge in a cycle is rejected at load), so every
// component lies wholly within one stratum and component order is a refinement
// of stratum order — negated reads still land strictly earlier. The refinement
// is what makes the common case cheap: `class_filter :- effective_filter, ...`
// shares a stratum with effective_filter but is not recursive with it, so by
// component it fires exactly once, against a relation that is already final,
// instead of being dragged through effective_filter's fixpoint rounds.
//
// Only the cone is evaluated: a rule file may define relations this goal never
// reads, and materializing those would be work nobody asked for.
func (e *Engine) cone(goal string) [][]string {
	// Dependency graph over derived relations, edges pointing at what a relation
	// reads. Sorted, so the component order cannot inherit rule-file order.
	adj := map[string][]string{}
	var nodes []string
	seen := map[string]bool{}
	var collect func(rel string)
	collect = func(rel string) {
		if seen[rel] || len(e.rules[rel]) == 0 {
			return
		}
		seen[rel] = true
		nodes = append(nodes, rel)
		for _, r := range e.rules[rel] {
			for _, l := range r.Body {
				if len(e.rules[l.Rel]) == 0 {
					continue
				}
				adj[rel] = append(adj[rel], l.Rel)
				collect(l.Rel)
			}
		}
	}
	collect(goal)
	sort.Strings(nodes)
	for _, n := range nodes {
		sort.Strings(adj[n])
		adj[n] = dedupe(adj[n])
	}

	// Tarjan: a component is emitted only once everything reachable from it —
	// which, with edges pointing at dependencies, means everything it reads —
	// has been emitted.
	var (
		index   = map[string]int{}
		low     = map[string]int{}
		onStack = map[string]bool{}
		stack   []string
		next    int
		out     [][]string
		strong  func(v string)
	)
	strong = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			if _, done := index[w]; !done {
				strong(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if onStack[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		sort.Strings(comp)
		out = append(out, comp)
	}
	for _, n := range nodes {
		if _, done := index[n]; !done {
			strong(n)
		}
	}
	return out
}

// evalComponent runs one strongly connected component to fixpoint. Everything
// it reads under negation is in an earlier component and is already complete,
// so no fixpoint is entered anywhere below this loop.
func (e *Engine) evalComponent(bu *bottomUp, rels []string) error {
	inComp := make(map[string]bool, len(rels))
	tables := make(map[string]*table, len(rels))
	for _, rel := range rels {
		inComp[rel] = true
	}

	// A component already completed by an earlier query is reused wholesale; the
	// second Query the emitter runs shares most of its cone with the first.
	pending := make([]string, 0, len(rels))
	for _, rel := range rels {
		arity, err := e.arityOf(rel)
		if err != nil {
			return err
		}
		t := e.freeTable(rel, arity)
		bu.full[rel] = t.rel
		if t.state == tableComplete {
			bu.complete[rel] = true
			continue
		}
		tables[rel] = t
		pending = append(pending, rel)
	}
	if len(pending) == 0 {
		return nil
	}

	// A relation can be both asserted and derived; the answer is the union, so
	// seed the base facts before the first round (solve does the same at the top
	// of a subgoal).
	for _, rel := range pending {
		t := tables[rel]
		t.state = tableProducing
		if br := e.base[rel]; br != nil {
			for _, tup := range br.tuples {
				if err := e.record(t, tup, nil); err != nil {
					return err
				}
			}
		}
	}

	// Rules producing this component, in file order so derivation output is
	// deterministic. A body literal inside the component is the recursive one:
	// it is the only kind that can still grow while these rules run, and it is
	// the position semi-naive evaluation redirects to a delta.
	type ruleWork struct {
		rule *Rule
		rec  []int
	}
	var work []ruleWork
	recursive := false
	for _, r := range e.ruleOrder {
		if tables[r.Head.Rel] == nil {
			continue
		}
		w := ruleWork{rule: r}
		for i, l := range r.Body {
			if !l.Neg && inComp[l.Rel] {
				w.rec = append(w.rec, i)
			}
		}
		if len(w.rec) > 0 {
			recursive = true
		}
		work = append(work, w)
	}

	// Round 0: every literal reads the full relation.
	mark := make(map[string]int, len(pending))
	bu.pos = -1
	for _, w := range work {
		if err := e.evalRule(bu, w.rule, make([]*string, w.rule.Head.arity()), tables[w.rule.Head.Rel]); err != nil {
			return err
		}
	}

	// A component with no rule reading it back is not recursive: every relation
	// it reads was final before round 0, so round 0 derived everything and a
	// second round could only re-derive it. Skipping that is most of what the
	// SCC refinement buys — it is the difference between firing the big
	// cross-product rules once and firing them twice.
	if !recursive {
		return e.completeComponent(bu, tables, pending)
	}

	// Semi-naive rounds. A tuple new in round k+1 needs at least one body tuple
	// new in round k, so redirecting one recursive occurrence at a time to the
	// delta — with the others reading the full relation — derives everything
	// naive evaluation would, at a cost proportional to what is new.
	for round := 1; ; round++ {
		deltas := make(map[string]*relation, len(pending))
		grew := false
		for _, rel := range pending {
			r := bu.full[rel]
			if d := e.deltaRelation(r, mark[rel]); d != nil {
				deltas[rel] = d
				grew = true
			}
			mark[rel] = len(r.tuples)
		}
		if !grew {
			break
		}
		if round > e.opts.MaxRounds {
			return fmt.Errorf("datalog: %s did not reach a fixpoint in %d rounds", pending[0], e.opts.MaxRounds)
		}
		bu.delta = deltas
		for _, w := range work {
			for _, pos := range w.rec {
				bu.pos = pos
				if err := e.evalRule(bu, w.rule, make([]*string, w.rule.Head.arity()), tables[w.rule.Head.Rel]); err != nil {
					return err
				}
			}
		}
		bu.pos = -1
		bu.delta = nil
	}
	return e.completeComponent(bu, tables, pending)
}

// completeComponent marks a finished component's tables complete, which is what
// makes them legal to read under negation and reusable by a later query.
func (e *Engine) completeComponent(bu *bottomUp, tables map[string]*table, pending []string) error {
	for _, rel := range pending {
		tables[rel].state = tableComplete
		bu.complete[rel] = true
	}
	return nil
}

// deltaRelation wraps the tuples a relation gained since index `from` — the
// previous round's output — as a probeable relation. Nil when nothing was
// gained. The tuples are shared, not copied; only the index is new.
func (e *Engine) deltaRelation(r *relation, from int) *relation {
	if from >= len(r.tuples) {
		return nil
	}
	d := newRelation(r.name, r.arity, e.syms)
	d.tuples = r.tuples[from:len(r.tuples):len(r.tuples)]
	return d
}
