package datalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

const closureProgram = `
	ancestor(C, P) :- parent(C, P).
	ancestor(C, A) :- parent(C, P), ancestor(P, A).
`

// TestCompileOnceEvalMany — one compiled Program, two independent fact bases,
// no cross-talk.
func TestCompileOnceEvalMany(t *testing.T) {
	t.Parallel()
	p, err := datalog.Compile([]byte(closureProgram), "closure.dl")
	require.NoError(t, err)

	a := datalog.NewFactRelations("ancestor")
	a.Add("parent", datalog.Tuple{"c", "b"}, datalog.Tuple{"b", "a"})
	ra, _, err := p.Eval(a)
	require.NoError(t, err)
	assert.Equal(t, []string{"b/a", "c/a", "c/b"}, flat(ra["ancestor"]))

	b := datalog.NewFactRelations("ancestor")
	b.Add("parent", datalog.Tuple{"z", "y"})
	rb, _, err := p.Eval(b)
	require.NoError(t, err)
	assert.Equal(t, []string{"z/y"}, flat(rb["ancestor"]))

	// The first result is unchanged by the second Eval.
	ra2, _, err := p.Eval(a)
	require.NoError(t, err)
	assert.Equal(t, flat(ra["ancestor"]), flat(ra2["ancestor"]))
}

// TestCompileRejectsUnstratified — the failure surfaces at Compile, not Eval.
func TestCompileRejectsUnstratified(t *testing.T) {
	t.Parallel()
	_, err := datalog.Compile([]byte(`win(X) :- edge(X, Y), not win(Y).`), "bad.dl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not stratified")
}

// TestEvalUnknownBaseRelationIsAnError — a rule reading a relation the fact base
// never mentions is a typo, not an empty set; Declare makes the intent explicit.
func TestEvalUnknownBaseRelation(t *testing.T) {
	t.Parallel()
	p, err := datalog.Compile([]byte(`eff(C, X) :- reg(C, X), not skip(C, X).`), "t.dl")
	require.NoError(t, err)

	fr := datalog.NewFactRelations("eff")
	fr.Add("reg", datalog.Tuple{"A", "auth"})
	_, _, err = p.Eval(fr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip")

	fr.Declare("skip")
	out, _, err := p.Eval(fr)
	require.NoError(t, err)
	assert.Equal(t, []string{"A/auth"}, flat(out["eff"]))
}

// TestEvalProvenanceIsLazy — Eval records nothing; the returned handle
// recomputes one tuple's derivation on demand.
func TestEvalProvenanceIsLazy(t *testing.T) {
	t.Parallel()
	p, err := datalog.Compile([]byte(closureProgram), "rails_filters.dl")
	require.NoError(t, err)
	fr := datalog.NewFactRelations("ancestor")
	fr.Add("parent", datalog.Tuple{"c", "b"}, datalog.Tuple{"b", "a"})

	_, prov, err := p.Eval(fr)
	require.NoError(t, err)
	ds, err := prov.Of("ancestor", datalog.Tuple{"c", "a"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, "rails_filters/ancestor", ds[0].Rule)
	assert.Equal(t, "parent", ds[0].Body[0].Relation)
}

// TestEvalWithBuiltinsAndAggregation — the FX.3 additions run through the
// Program surface end to end.
func TestEvalWithBuiltinsAndAggregation(t *testing.T) {
	t.Parallel()
	p, err := datalog.Compile([]byte(`
		shallowest(Site, D) :- resolved(Site, _, _, Depth), min(D, Depth).
		certain(Site)       :- shallowest(Site, D), le(D, 1).
	`), "t.dl")
	require.NoError(t, err)

	fr := datalog.NewFactRelations("shallowest", "certain")
	fr.Add("resolved",
		datalog.Tuple{"s1", "auth", "t_a", "3"},
		datalog.Tuple{"s1", "auth", "t_b", "1"},
		datalog.Tuple{"s2", "audit", "t_c", "4"},
	)
	out, _, err := p.Eval(fr)
	require.NoError(t, err)
	assert.Equal(t, []string{"s1/1", "s2/4"}, flat(out["shallowest"]))
	assert.Equal(t, []string{"s1"}, flat(out["certain"]))
}

// TestEvalIsDeterministic — same input, byte-identical output, ten runs.
func TestEvalIsDeterministic(t *testing.T) {
	t.Parallel()
	p, err := datalog.Compile([]byte(closureProgram), "t.dl")
	require.NoError(t, err)

	var first []string
	for i := 0; i < 10; i++ {
		fr := datalog.NewFactRelations("ancestor")
		fr.Add("parent", datalog.Tuple{"d", "c"}, datalog.Tuple{"c", "b"}, datalog.Tuple{"b", "a"})
		out, _, err := p.Eval(fr)
		require.NoError(t, err)
		if i == 0 {
			first = flat(out["ancestor"])
			continue
		}
		assert.Equal(t, first, flat(out["ancestor"]))
	}
}
