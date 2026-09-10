package datalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

func railsResolved() []datalog.Tuple {
	return []datalog.Tuple{
		{"s1", "auth", "t_a", "3"},
		{"s1", "auth", "t_b", "1"},
		{"s1", "auth", "t_c", "2"},
		{"s2", "audit", "t_d", "4"},
		{"s2", "audit", "t_e", "6"},
	}
}

// TestMinPicksShallowestDepth — "resolve to the nearest frame" as a rule.
func TestMinPicksShallowestDepth(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("resolved", railsResolved()))
	require.NoError(t, e.LoadRules([]byte(`
		shallowest(Site, D) :- resolved(Site, _, _, Depth), min(D, Depth).
		deepest(Site, D)    :- resolved(Site, _, _, Depth), max(D, Depth).
	`), "t.dl"))

	got, err := e.Query("shallowest")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1/1", "s2/4"}, flat(got))

	got, err = e.Query("deepest")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1/3", "s2/6"}, flat(got))
}

// TestCountAggregatesDistinctValues — "how many distinct targets resolved here".
func TestCountDistinct(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("resolved", append(railsResolved(),
		datalog.Tuple{"s1", "auth", "t_a", "9"}, // duplicate target t_a
	)))
	require.NoError(t, e.LoadRules([]byte(`
		fanout(Site, N) :- resolved(Site, _, Target, _), count(N, Target).
	`), "t.dl"))
	got, err := e.Query("fanout")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1/3", "s2/2"}, flat(got))
}

// TestAggregateOverOwnStratumRejected — an aggregate over a relation in its own
// component has no least fixpoint, same as negation through recursion.
func TestAggregateOverOwnStratumRejected(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("seed", []datalog.Tuple{{"a", "1"}}))
	err := e.LoadRules([]byte(`
		loop(A, M) :- seed(A, _), loop(A, B), min(M, B).
		loop(A, V) :- seed(A, V).
	`), "t.dl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not stratified")
}

// TestAggregateComposesWithDownstreamRules — the aggregate result feeds an
// ordinary rule, and a bound query on the aggregate relation still works.
func TestAggregateComposesAndBinds(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("resolved", railsResolved()))
	require.NoError(t, e.LoadRules([]byte(`
		shallowest(Site, D) :- resolved(Site, _, _, Depth), min(D, Depth).
		certain(Site)       :- shallowest(Site, D), le(D, 1).
	`), "t.dl"))

	got, err := e.Query("certain")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1"}, flat(got))

	bound, err := e.QueryPattern("shallowest", []string{"s2", ""})
	require.NoError(t, err)
	assert.Equal(t, []string{"s2/4"}, flat(bound))
}

// TestAggregateProvenance — the derivation names the rule and carries the
// winning input row.
func TestAggregateProvenance(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("resolved", railsResolved()))
	require.NoError(t, e.LoadRules([]byte(`
		shallowest(Site, D) :- resolved(Site, _, _, Depth), min(D, Depth).
	`), "rails_filters.dl"))

	ds, err := e.Provenance("shallowest", datalog.Tuple{"s1", "1"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, "rails_filters/shallowest", ds[0].Rule)
	require.Len(t, ds[0].Body, 1)
	assert.Equal(t, "resolved", ds[0].Body[0].Relation)
	assert.Equal(t, datalog.Tuple{"s1", "auth", "t_b", "1"}, ds[0].Body[0].Tuple)
}

// TestAggregateDeterministic — no map order in the emitted groups.
func TestAggregateDeterministic(t *testing.T) {
	t.Parallel()
	var first []string
	for i := 0; i < 10; i++ {
		e := datalog.New(datalog.Options{})
		require.NoError(t, e.Assert("resolved", railsResolved()))
		require.NoError(t, e.LoadRules([]byte(`
			shallowest(Site, D) :- resolved(Site, _, _, Depth), min(D, Depth).
		`), "t.dl"))
		got, err := e.Query("shallowest")
		require.NoError(t, err)
		if i == 0 {
			first = flat(got)
			continue
		}
		assert.Equal(t, first, flat(got))
	}
}
