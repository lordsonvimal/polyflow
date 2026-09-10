package datalog

import (
	"fmt"
	"sort"
)

// FX.3 — bounded aggregation: min / max / count.
//
// "First hit" / "nearest" / "shallowest" / "how many disagreed" are the shapes
// the framework rules need that plain Datalog cannot state. Aggregation supplies
// them without arithmetic: `head(G, Out) :- body, min(Out, In)` groups the
// satisfying substitutions by the head variables other than Out and binds Out to
// min / max / count of In per group.
//
// It is bounded the way negation is: the aggregated relation must be fully
// derived before the aggregating rule runs. The stratifier forces every positive
// input of an aggregate rule a stratum higher (strata.go) and rejects an
// aggregate over its own or a higher component. Evaluation therefore only
// happens on the stratified bottom-up path; a top-down reach for an aggregate
// relation is redirected there by solve.

type aggRun struct {
	accs map[tkey]*aggAcc
}

// aggAcc is one group's running aggregate. group is the head tuple with the
// aggregate slot left zero; flushAgg fills that slot and records the row.
type aggAcc struct {
	group    itup
	aggPos   int
	ival     int64
	have     bool
	distinct map[uint32]bool
	steps    []DerivationStep
}

// aggCollect folds one satisfying substitution into its group. Called from the
// join's terminal case in place of head construction when r.Agg is set.
func (e *Engine) aggCollect(r *Rule, fr *frame, steps []DerivationStep) error {
	ar := e.agg
	head := make(itup, len(r.Head.Args))
	aggPos := -1
	for k, term := range r.Head.Args {
		if !term.IsVar {
			head[k] = term.Sym
			continue
		}
		if term.Var == r.Agg.outSlot {
			aggPos = k
			continue // filled at flush
		}
		v, ok := fr.get(term.Var)
		if !ok {
			return fmt.Errorf("datalog: rule %s (%s:%d): head variable %s unbound in aggregate",
				r.Name, r.File, r.Line, term.Name)
		}
		head[k] = v
	}
	inv, ok := fr.get(r.Agg.inSlot)
	if !ok {
		return fmt.Errorf("datalog: rule %s (%s:%d): aggregate input %s unbound", r.Name, r.File, r.Line, r.Agg.InVar)
	}

	// The aggregate slot is zero in every row of a group, so the key separates
	// groups by their other head columns and nothing else.
	key := e.syms.key(head)
	acc := ar.accs[key]
	if acc == nil {
		acc = &aggAcc{group: append(itup(nil), head...), aggPos: aggPos, distinct: map[uint32]bool{}}
		ar.accs[key] = acc
	}

	switch r.Agg.Fn {
	case "count":
		acc.distinct[inv] = true
	default: // min, max
		n, isInt := decodeIntSym(inv)
		if !isInt {
			return fmt.Errorf("datalog: rule %s: %s(%s, %s) aggregates a non-integer value %q",
				r.Name, r.Agg.Fn, r.Agg.OutVar, r.Agg.InVar, e.syms.sym(inv))
		}
		if !acc.have || (r.Agg.Fn == "min" && n < acc.ival) || (r.Agg.Fn == "max" && n > acc.ival) {
			acc.have = true
			acc.ival = n
			if e.recordProv {
				acc.steps = append([]DerivationStep(nil), steps...)
			}
		}
	}
	return nil
}

// flushAgg emits one head tuple per group once the join has visited every
// substitution. Groups are recorded in sorted order so derivation output is
// deterministic (the Query result is sorted regardless).
func (e *Engine) flushAgg(r *Rule, t *table) error {
	ar := e.agg
	keys := make([]tkey, 0, len(ar.accs))
	for k := range ar.accs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareTuples(e.syms.reveal(ar.accs[keys[i]].group), e.syms.reveal(ar.accs[keys[j]].group)) < 0
	})

	for _, k := range keys {
		acc := ar.accs[k]
		var result int64
		switch r.Agg.Fn {
		case "count":
			result = int64(len(acc.distinct))
		default:
			if !acc.have {
				continue
			}
			result = acc.ival
		}
		head := append(itup(nil), acc.group...)
		head[acc.aggPos] = intSymFlag | uint32(result)

		var d *Derivation
		if e.recordProv {
			d = &Derivation{Rule: r.Name, Head: e.syms.reveal(head), Body: append([]DerivationStep(nil), acc.steps...)}
		}
		if err := e.record(t, head, d); err != nil {
			return err
		}
	}
	return nil
}
