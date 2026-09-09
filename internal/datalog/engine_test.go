package datalog_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

func flat(ts []datalog.Tuple) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, strings.Join(t, "/"))
	}
	return out
}

// TestTransitiveClosure is the shape the whole tier is for: an ancestor chain
// written once instead of a hand-rolled recursive walk per caller.
func TestTransitiveClosure(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("parent", []datalog.Tuple{{"c", "b"}, {"b", "a"}, {"d", "b"}}))
	require.NoError(t, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
	`), "t.dl"))

	got, err := e.Query("ancestor")
	require.NoError(t, err)
	assert.Equal(t, []string{"b/a", "c/a", "c/b", "d/a", "d/b"}, flat(got))
}

// TestCycleTerminates: a class hierarchy reconstructed from constant names can
// close a loop. The engine must answer, not spin — the hand-written walks it
// replaces each carry their own depth cap for this.
func TestCycleTerminates(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("parent", []datalog.Tuple{{"a", "b"}, {"b", "a"}}))
	require.NoError(t, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
	`), "t.dl"))

	got, err := e.Query("ancestor")
	require.NoError(t, err)
	assert.Equal(t, []string{"a/a", "a/b", "b/a", "b/b"}, flat(got))
}

// TestStratifiedNegation: the `not skipped(...)` half of the filter chain.
func TestStratifiedNegation(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("reg", []datalog.Tuple{{"A", "auth"}, {"A", "audit"}, {"B", "auth"}}))
	require.NoError(t, e.Assert("parent", []datalog.Tuple{{"B", "A"}}))
	require.NoError(t, e.Assert("skip", []datalog.Tuple{{"B", "audit"}}))
	require.NoError(t, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
		inherited(C, CB) :- ancestor(C, A), reg(A, CB).
		registered(C, CB) :- reg(C, CB).
		registered(C, CB) :- inherited(C, CB).
		effective(C, CB) :- registered(C, CB), not skip(C, CB).
	`), "t.dl"))

	got, err := e.Query("effective")
	require.NoError(t, err)
	assert.Equal(t, []string{"A/audit", "A/auth", "B/auth"}, flat(got),
		"B inherits audit from A and skips it; auth survives")
}

// TestNegationThroughRecursionIsRejected. A program with no least fixpoint has
// no single answer, and the engine would otherwise return whichever model the
// evaluation order happened to reach.
func TestNegationThroughRecursionIsRejected(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("edge", []datalog.Tuple{{"a", "b"}}))
	err := e.LoadRules([]byte(`
		win(X) :- edge(X, Y), not win(Y).
	`), "t.dl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not stratified")
	assert.Contains(t, err.Error(), "win", "the cycle must be named")
}

// TestUnknownRelationIsAnError is the single most common failure mode of a
// declarative layer: a typo'd relation silently yielding nothing.
func TestUnknownRelationIsAnError(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("parent", nil))
	err := e.LoadRules([]byte(`ancestor(C, P) :- parnet(C, P).`), "t.dl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parnet")

	e2 := datalog.New(datalog.Options{})
	require.NoError(t, e2.Assert("parent", nil))
	require.NoError(t, e2.LoadRules([]byte(`ancestor(C, P) :- parent(C, P).`), "t.dl"))
	_, err = e2.Query("ancesotr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown relation")
}

// TestAssertedButEmptyIsNotUnknown: an emitter saying "this service really has
// no skips" must not look like a typo.
func TestAssertedButEmptyIsNotUnknown(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("reg", []datalog.Tuple{{"A", "auth"}}))
	require.NoError(t, e.Assert("skip", nil))
	require.NoError(t, e.LoadRules([]byte(`eff(C, CB) :- reg(C, CB), not skip(C, CB).`), "t.dl"))
	got, err := e.Query("eff")
	require.NoError(t, err)
	assert.Equal(t, []string{"A/auth"}, flat(got))
}

// TestUnsafeRulesRejected: an unbound head variable is an infinite relation and
// an unbound variable under `not` asks a different question than it looks like.
func TestUnsafeRulesRejected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, want string }{
		{"unbound head", `p(X, Y) :- q(X).`, "head variable Y"},
		{"unbound under not", `p(X) :- q(X), not r(Y).`, "variable Y in `not r(...)`"},
		{"anonymous head", `p(_) :- q(X).`, "anonymous variable in its head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := datalog.New(datalog.Options{})
			require.NoError(t, e.Assert("q", nil))
			require.NoError(t, e.Assert("r", nil))
			err := e.LoadRules([]byte(tc.src), "t.dl")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestArityMismatchRejected — `filter_reg(C)` against an asserted
// `filter_reg(C, R)` evaluates to nothing at all, silently, without this.
func TestArityMismatchRejected(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("reg", []datalog.Tuple{{"A", "auth"}}))
	err := e.LoadRules([]byte(`p(C) :- reg(C).`), "t.dl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "arity")
}

// TestAnonymousVariablesAreDistinct: two `_` in one literal must not join.
func TestAnonymousVariablesAreDistinct(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("pair", []datalog.Tuple{{"a", "b"}, {"c", "c"}}))
	require.NoError(t, e.LoadRules([]byte(`any(X) :- pair(_, X), pair(X, _).`), "t.dl"))
	got, err := e.Query("any")
	require.NoError(t, err)
	assert.Equal(t, []string{"c"}, flat(got))
}

// TestProvenanceWalksBackToBaseFacts is the SA.1 requirement: "which rule
// produced this tuple, from which facts", walkable to the bottom.
func TestProvenanceWalksBackToBaseFacts(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("parent", []datalog.Tuple{{"c", "b"}, {"b", "a"}}))
	require.NoError(t, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
	`), "rails_filters.dl"))

	ds, err := e.Provenance("ancestor", datalog.Tuple{"c", "a"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, "rails_filters/ancestor", ds[0].Rule)
	require.Len(t, ds[0].Body, 2)
	assert.Equal(t, "parent", ds[0].Body[0].Relation)
	assert.True(t, ds[0].Body[0].Base)
	assert.Equal(t, datalog.Tuple{"c", "b"}, ds[0].Body[0].Tuple)
	assert.Equal(t, "ancestor", ds[0].Body[1].Relation)
	assert.False(t, ds[0].Body[1].Base, "a derived body entry is walkable, not a base fact")

	// and the walk-back terminates at a base fact
	inner, err := e.Provenance("ancestor", ds[0].Body[1].Tuple)
	require.NoError(t, err)
	require.Len(t, inner, 1)
	assert.Equal(t, []datalog.DerivationStep{{Relation: "parent", Tuple: datalog.Tuple{"b", "a"}, Base: true}}, inner[0].Body)
}

// TestProvenanceReturnsEveryDerivation — collapsing to the first would defeat
// the question the method exists to answer.
func TestProvenanceReturnsEveryDerivation(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("a", []datalog.Tuple{{"x"}}))
	require.NoError(t, e.Assert("b", []datalog.Tuple{{"x"}}))
	require.NoError(t, e.LoadRules([]byte(`
		p(X) :- a(X).
		p(X) :- b(X).
	`), "t.dl"))
	ds, err := e.Provenance("p", datalog.Tuple{"x"})
	require.NoError(t, err)
	require.Len(t, ds, 2)
	assert.Equal(t, "a", ds[0].Body[0].Relation)
	assert.Equal(t, "b", ds[1].Body[0].Relation)
}

// TestBoundQueryAgreesWithUnbound — demand-driven evaluation must not be a
// different program from bottom-up. Both modes, both patterns.
func TestBoundQueryAgreesWithUnbound(t *testing.T) {
	t.Parallel()
	src := []byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
		guarded(C, CB) :- ancestor(C, A), reg(A, CB), not skip(C, CB).
		guarded(C, CB) :- reg(C, CB), not skip(C, CB).
	`)
	facts := map[string][]datalog.Tuple{
		"parent": {{"d", "c"}, {"c", "b"}, {"b", "a"}},
		"reg":    {{"a", "auth"}, {"b", "audit"}, {"d", "own"}},
		"skip":   {{"d", "audit"}},
	}
	build := func(demand bool) *datalog.Engine {
		e := datalog.New(datalog.Options{DemandDriven: demand, MaxTuples: 1000, MaxRounds: 50})
		for rel, ts := range facts {
			require.NoError(t, e.Assert(rel, ts))
		}
		require.NoError(t, e.LoadRules(src, "t.dl"))
		return e
	}

	want := []string{"a/auth", "b/audit", "b/auth", "c/audit", "c/auth", "d/auth", "d/own"}
	for _, demand := range []bool{true, false} {
		all, err := build(demand).Query("guarded")
		require.NoError(t, err)
		assert.Equal(t, want, flat(all), "demandDriven=%v", demand)

		bound, err := build(demand).QueryPattern("guarded", []string{"d", ""})
		require.NoError(t, err)
		assert.Equal(t, []string{"d/auth", "d/own"}, flat(bound), "demandDriven=%v", demand)
	}
}

// TestMaxTuplesNamesTheRelation: a cap that fails without saying what blew up
// is a cap nobody can act on.
func TestMaxTuplesNamesTheRelation(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{DemandDriven: true, MaxTuples: 6})
	require.NoError(t, e.Assert("parent", []datalog.Tuple{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "e"}}))
	require.NoError(t, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
	`), "t.dl"))
	_, err := e.Query("ancestor")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ancestor")
	assert.Contains(t, err.Error(), "MaxTuples")
}

// TestQueryIsDeterministic — output order must never come from map iteration.
func TestQueryIsDeterministic(t *testing.T) {
	t.Parallel()
	var first []string
	for i := 0; i < 8; i++ {
		e := datalog.New(datalog.Options{})
		require.NoError(t, e.Assert("parent", []datalog.Tuple{{"c", "b"}, {"b", "a"}, {"d", "b"}, {"e", "d"}}))
		require.NoError(t, e.LoadRules([]byte(`
			ancestor(C, P) :- parent(C, P).
			ancestor(C, A) :- parent(C, P), ancestor(P, A).
		`), "t.dl"))
		got, err := e.Query("ancestor")
		require.NoError(t, err)
		if i == 0 {
			first = flat(got)
			continue
		}
		assert.Equal(t, first, flat(got))
	}
}
