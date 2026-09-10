package datalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

// TestComparisonBuiltinFiltersOnDepth is the shape FX.3 exists for: "the
// callback resolved within two frames" as a rule, not a Go branch.
func TestComparisonBuiltinFiltersOnDepth(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("resolved", []datalog.Tuple{
		{"s1", "auth", "t1", "0"},
		{"s2", "auth", "t2", "2"},
		{"s3", "auth", "t3", "5"},
	}))
	require.NoError(t, e.LoadRules([]byte(`
		near(Site, Depth) :- resolved(Site, _, _, Depth), le(Depth, 2).
		far(Site)         :- resolved(Site, _, _, Depth), lt(2, Depth).
	`), "t.dl"))

	near, err := e.Query("near")
	require.NoError(t, err)
	assert.Equal(t, []string{"s1/0", "s2/2"}, flat(near))

	far, err := e.Query("far")
	require.NoError(t, err)
	assert.Equal(t, []string{"s3"}, flat(far))
}

// TestNeIsDefinedOnAnyAtoms — ne guards "these two node ids differ", which is
// not an integer comparison.
func TestNeIsDefinedOnAnyAtoms(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("pair", []datalog.Tuple{{"a", "b"}, {"c", "c"}, {"d", "e"}}))
	require.NoError(t, e.LoadRules([]byte(`diff(X, Y) :- pair(X, Y), ne(X, Y).`), "t.dl"))
	got, err := e.Query("diff")
	require.NoError(t, err)
	assert.Equal(t, []string{"a/b", "d/e"}, flat(got))
}

// TestBuiltinArgMustBeBoundEarlier — an unbound builtin operand asks a
// different question than it looks like, so it is rejected at load, in order.
func TestBuiltinArgMustBeBoundEarlier(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, src, want string }{
		{"never bound", `p(X) :- q(X), lt(X, Y).`, "argument Y is not bound"},
		{"bound later", `p(X) :- lt(X, Y), q(X, Y).`, "argument X is not bound by an earlier positive literal"},
		{"builtin as head", `lt(X, Y) :- q(X, Y).`, "is a builtin, not a relation"},
		{"negated builtin", `p(X) :- q(X, Y), not lt(X, Y).`, "cannot be negated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := datalog.New(datalog.Options{})
			require.NoError(t, e.Assert("q", nil))
			err := e.LoadRules([]byte(tc.src), "t.dl")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestLtRejectsNonIntegerOperand — lt/le are the integer domain only; a string
// operand is a rule bug and names the builtin.
func TestLtRejectsNonIntegerOperand(t *testing.T) {
	t.Parallel()
	e := datalog.New(datalog.Options{})
	require.NoError(t, e.Assert("name", []datalog.Tuple{{"foo"}}))
	require.NoError(t, e.LoadRules([]byte(`bad(X) :- name(X), lt(X, 3).`), "t.dl"))
	_, err := e.Query("bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lt expects integer operands")
}

// TestIntegerAtomsRoundTrip — a numeric term interns to the reserved range and
// de-interns to the same decimal, deterministically, in both eval modes.
func TestIntegerAtomsRoundTrip(t *testing.T) {
	t.Parallel()
	build := func() *datalog.Engine {
		e := datalog.New(datalog.Options{})
		require.NoError(t, e.Assert("line", []datalog.Tuple{{"a", "7"}, {"b", "42"}, {"c", "7"}}))
		require.NoError(t, e.LoadRules([]byte(`
			at(File, L) :- line(File, L), le(7, L).
		`), "t.dl"))
		return e
	}
	want := []string{"a/7", "b/42", "c/7"}
	all, err := build().Query("at")
	require.NoError(t, err)
	assert.Equal(t, want, flat(all))

	bound, err := build().QueryPattern("at", []string{"", "7"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a/7", "c/7"}, flat(bound))
}
