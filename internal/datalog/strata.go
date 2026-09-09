package datalog

import (
	"fmt"
	"sort"
	"strings"
)

// stratify assigns each derived relation a stratum such that a relation is
// always evaluated after everything it reads, and strictly after anything it
// reads under `not`.
//
// The reason this is load-time and not a runtime concern: negation through a
// recursive cycle has no least fixpoint, so a program containing one has no
// single answer — the engine would return whichever of two equally valid
// models the evaluation order happened to reach. That is the failure mode that
// makes a declarative layer untrustworthy, and it is cheap to reject outright.
func stratify(rules []*Rule) (map[string]int, error) {
	heads := map[string]bool{}
	for _, r := range rules {
		heads[r.Head.Rel] = true
	}

	// deps[to] = relations `to` reads; neg records whether the read is negated.
	type dep struct{ rel string; neg bool }
	deps := map[string][]dep{}
	for _, r := range rules {
		for _, l := range r.Body {
			if !heads[l.Rel] {
				continue // base relations are stratum 0 by construction
			}
			deps[r.Head.Rel] = append(deps[r.Head.Rel], dep{l.Rel, l.Neg})
		}
	}

	// Iterative stratum assignment: stratum(H) >= stratum(B) for a positive
	// read, and > for a negated one. Convergence within len(heads) rounds is
	// what proves no negative edge sits inside a cycle.
	strata := map[string]int{}
	for h := range heads {
		strata[h] = 0
	}
	names := make([]string, 0, len(heads))
	for h := range heads {
		names = append(names, h)
	}
	sort.Strings(names)

	for round := 0; round <= len(heads); round++ {
		changed := false
		for _, h := range names {
			for _, d := range deps[h] {
				want := strata[d.rel]
				if d.neg {
					want++
				}
				if want > strata[h] {
					strata[h] = want
					changed = true
				}
			}
		}
		if !changed {
			return strata, nil
		}
	}

	// Did not converge: some negative edge is inside a recursive cycle. Name it,
	// because "your program is not stratified" without the cycle is useless.
	cycle := findNegativeCycle(rules, heads)
	return nil, fmt.Errorf("program is not stratified: negation inside the recursive cycle %s", strings.Join(cycle, " -> "))
}

// findNegativeCycle returns a relation cycle containing at least one negated
// edge, for the error message. Best-effort: any such cycle is a valid answer.
func findNegativeCycle(rules []*Rule, heads map[string]bool) []string {
	type edge struct{ to string; neg bool }
	adj := map[string][]edge{}
	var froms []string
	for _, r := range rules {
		if _, seen := adj[r.Head.Rel]; !seen {
			froms = append(froms, r.Head.Rel)
		}
		for _, l := range r.Body {
			if heads[l.Rel] {
				adj[r.Head.Rel] = append(adj[r.Head.Rel], edge{l.Rel, l.Neg})
			}
		}
		if adj[r.Head.Rel] == nil {
			adj[r.Head.Rel] = []edge{}
		}
	}
	sort.Strings(froms)

	var path []string
	onPath := map[string]bool{}
	var found []string
	var walk func(rel string, sawNeg bool)
	walk = func(rel string, sawNeg bool) {
		if found != nil {
			return
		}
		if onPath[rel] {
			if sawNeg {
				for i, p := range path {
					if p == rel {
						found = append(append([]string{}, path[i:]...), rel)
						return
					}
				}
			}
			return
		}
		onPath[rel] = true
		path = append(path, rel)
		for _, e := range adj[rel] {
			walk(e.to, sawNeg || e.neg)
		}
		path = path[:len(path)-1]
		onPath[rel] = false
	}
	for _, f := range froms {
		walk(f, false)
		if found != nil {
			return found
		}
	}
	return froms
}
