package datalog_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/rules"
)

// Tier DL.3 / P.0 — a benchmark that reproduces the cedar shape without cedar.
//
// docs/static-architecture-dl1-report.md measured the derivation layer against a
// private corpus: ~450ms wall and ~2GB allocated to produce ~16,300 facts, where
// the hand-written walk does the same in 1.4ms. None of that is reproducible in
// CI, so every future regression in the engine is invisible. This fixture builds
// a synthetic fact base with the same shape — class count, ancestor-closure size,
// registration and action population, only:/skip mix — and drives the real
// rules/ruby/rails_filters.dl end to end through the two queries the emitter runs
// (class_filter, action_filter).
//
// Shape targets (docs/datalog-engine-performance-plan.md §P.0):
//
//	1,090 class declarations
//	  644 ancestor edges (transitive closure of subclass_of)
//	  338 filter registrations
//	  827 (class, action) pairs
//	  125 registrations carrying only:
//	   40 skips
//
// The one parameter not fixed by the plan is action-name diversity: cedar's 827
// (class, action) pairs span a few hundred distinct method names, and the size of
// that set is what drives the `reg_blocked` / `skip_applies` cross-products and,
// through them, the global-round-counter memo thrashing described in §1.1.
// benchActionNames is tuned so the whole thing lands on the DL.1 wall/alloc
// numbers.
//
// Acceptance: BenchmarkRailsFilters reproduces the DL.1 numbers within 25% on the
// same machine. A benchmark that does not reproduce the defect cannot demonstrate
// the fix, and P.1/P.2 are measured against this. Measured on an Apple M4:
// ~452ms/op, ~1.9GB/op, ~7.0M allocs/op (DL.1 report: ~450ms, ~2GB; the alloc
// profile matches too — (*relation).match is 71% of alloc_space, cf. §1.2's 61%).

const (
	benchClasses      = 1090
	benchDirectSub    = 500 // classes subclassing the base controller directly
	benchDepth2Sub    = 72  // classes one level below those (each => 2 ancestor edges)
	benchControllers  = benchDirectSub + benchDepth2Sub
	benchRegs         = 338
	benchRegsOnC0     = 6 // registrations on the base controller: inherited everywhere
	benchActionPairs  = 827
	benchActionNames  = 380
	benchRegsWithOnly = 125
	benchRegsExcept   = 15
	benchSkips        = 40
	benchSkipsExcept  = 10 // skips carrying except: (the rest are skip_total)
	benchIncludes     = 50
)

func classKey(i int) string   { return fmt.Sprintf("c%04d", i) }
func regKey(i int) string     { return fmt.Sprintf("r%04d", i) }
func skipKey(i int) string    { return fmt.Sprintf("s%04d", i) }
func moduleKey(i int) string  { return fmt.Sprintf("m%04d", i) }
func actionName(i int) string { return fmt.Sprintf("action%03d", i%benchActionNames) }

var benchCallbacks = []string{
	"authenticate_user", "set_locale", "authorize_access", "load_resource",
	"set_paper_trail", "track_activity", "require_login", "set_current_tenant",
}

var benchFamilies = []string{
	"before_action", "before_action", "before_action", "before_action",
	"before_action", "before_action", "before_action", "before_action",
	"after_action", "around_action",
}

// buildRailsFixture returns an engine loaded with rules/ruby/rails_filters.dl and
// a synthetic fact base of the shape described above.
func buildRailsFixture(tb testing.TB) *datalog.Engine {
	tb.Helper()
	e := datalog.New(datalog.Options{})

	var (
		classDecl  []datalog.Tuple
		subclassOf []datalog.Tuple
		includes   []datalog.Tuple
		actions    []datalog.Tuple
		filterRegs []datalog.Tuple
		skipReg    []datalog.Tuple
		regFamily  []datalog.Tuple
		regCB      []datalog.Tuple
		skipTotal  []datalog.Tuple
		regHasOnly []datalog.Tuple
		regOnly    []datalog.Tuple
		regExcept  []datalog.Tuple
	)

	// class_decl: one per class or module body.
	for i := 0; i < benchClasses; i++ {
		classDecl = append(classDecl, datalog.Tuple{classKey(i)})
	}
	// modules are named in their own space so filter_owner recursion has
	// something to chase; declare them too.
	for i := 0; i < benchIncludes; i++ {
		classDecl = append(classDecl, datalog.Tuple{moduleKey(i)})
	}

	// subclass_of: c0 is ApplicationController. c1..c500 subclass it directly
	// (500 ancestor edges); c501..c572 subclass one of those (144 more) => 644.
	for i := 1; i <= benchDirectSub; i++ {
		subclassOf = append(subclassOf, datalog.Tuple{classKey(i), classKey(0)})
	}
	for i := 0; i < benchDepth2Sub; i++ {
		child := benchDirectSub + 1 + i
		subclassOf = append(subclassOf, datalog.Tuple{classKey(child), classKey(1 + i)})
	}

	// includes_module: some controllers include a concern; a few concerns
	// include another concern (filter_owner is transitive through modules).
	for i := 0; i < benchIncludes; i++ {
		includes = append(includes, datalog.Tuple{classKey(1 + i*7), moduleKey(i)})
		if i%5 == 0 && i+1 < benchIncludes {
			includes = append(includes, datalog.Tuple{moduleKey(i), moduleKey(i + 1)})
		}
	}

	// action(C, A): 827 pairs across the controller classes, drawn from a pool
	// of ~380 distinct names (benchActionNames). Pre-D.1 this sized the
	// reg_blocked / skip_applies cross-product; post-D.1 it sizes the per-class
	// bounded only:/except: probes instead.
	{
		emitted := 0
		cls := 1
		k := 0
		for emitted < benchActionPairs {
			actions = append(actions, datalog.Tuple{classKey(cls), actionName(cls*7 + k)})
			emitted++
			k++
			// first ~255 classes get 2 actions, the rest 1, summing to 827.
			if (cls <= 255 && k == 2) || (cls > 255 && k == 1) {
				cls++
				k = 0
			}
		}
	}

	// filter_reg(C, R): benchRegsOnC0 on the base controller (inherited by every
	// controller), the rest spread across the controller classes.
	only := 0
	except := 0
	for i := 0; i < benchRegs; i++ {
		r := regKey(i)
		var owner string
		if i < benchRegsOnC0 {
			owner = classKey(0)
		} else {
			owner = classKey(1 + (i % benchControllers))
		}
		filterRegs = append(filterRegs, datalog.Tuple{owner, r})
		regFamily = append(regFamily, datalog.Tuple{r, benchFamilies[i%len(benchFamilies)]})
		cb := benchCallbacks[i%len(benchCallbacks)]
		if i < benchRegsOnC0 {
			cb = benchCallbacks[i%2] // authenticate_user / set_locale: high fan-out
		}
		regCB = append(regCB, datalog.Tuple{r, cb})

		switch {
		case only < benchRegsWithOnly && i%2 == 0:
			regHasOnly = append(regHasOnly, datalog.Tuple{r})
			regOnly = append(regOnly, datalog.Tuple{r, actionName(i)})
			regOnly = append(regOnly, datalog.Tuple{r, actionName(i + 1)})
			only++
		case except < benchRegsExcept && i%3 == 0:
			regExcept = append(regExcept, datalog.Tuple{r, actionName(i)})
			except++
		}
	}

	// skip_reg(C, S): 40 skips retracting the base controller's before_action
	// callbacks from classes lower in the chain. skip_total for most; a few
	// carry except: and are decided per action.
	for i := 0; i < benchSkips; i++ {
		s := skipKey(i)
		owner := classKey(1 + (i*13)%benchControllers)
		skipReg = append(skipReg, datalog.Tuple{owner, s})
		regFamily = append(regFamily, datalog.Tuple{s, "before_action"})
		regCB = append(regCB, datalog.Tuple{s, benchCallbacks[i%2]})
		if i < benchSkipsExcept {
			regExcept = append(regExcept, datalog.Tuple{s, actionName(i * 5)})
		} else {
			skipTotal = append(skipTotal, datalog.Tuple{s})
		}
	}

	assert := func(rel string, ts []datalog.Tuple) {
		require.NoError(tb, e.Assert(rel, ts), "assert %s", rel)
	}
	assert("class_decl", classDecl)
	assert("subclass_of", subclassOf)
	assert("includes_module", includes)
	assert("action", actions)
	assert("filter_reg", filterRegs)
	assert("skip_reg", skipReg)
	assert("reg_family", regFamily)
	assert("reg_callback", regCB)
	assert("skip_total", skipTotal)
	assert("reg_has_only", regHasOnly)
	assert("reg_only", regOnly)
	assert("reg_except", regExcept)

	require.NoError(tb, e.LoadRules(rules.MustLoad("ruby/rails_filters.dl"), "rails_filters.dl"))
	return e
}

// TestRailsFixtureShape pins the fixture's derived-relation sizes so it cannot
// silently drift away from the cedar shape it is meant to reproduce.
func TestRailsFixtureShape(t *testing.T) {
	t.Parallel()
	e := buildRailsFixture(t)

	for _, tc := range []struct {
		rel      string
		min, max int
	}{
		{"ancestor", 600, 700},
		{"registered", 2000, 60000},
		{"effective_filter", 1500, 60000},
		{"class_filter", 1000, 60000},
		{"action_filter", 1000, 200000},
	} {
		got, err := e.Query(tc.rel)
		require.NoError(t, err, tc.rel)
		n := len(got)
		if n < tc.min || n > tc.max {
			t.Errorf("%s: got %d tuples, want within [%d, %d]", tc.rel, n, tc.min, tc.max)
		} else {
			t.Logf("%s: %d tuples", tc.rel, n)
		}
	}
}

// TestRailsFiltersD1DroppedTheCrossProduct pins DL.3 D.1: the only:/except:
// restriction is case-split into bounded index probes, so the manufactured
// service-wide relations reg_blocked / skip_applies / action_name (and their
// helpers) must have left the rule set entirely — a query for one is now an
// unknown-relation error, not a large cross-product.
func TestRailsFiltersD1DroppedTheCrossProduct(t *testing.T) {
	t.Parallel()
	e := buildRailsFixture(t)
	for _, rel := range []string{"reg_blocked", "skip_applies", "action_name", "skip_reg_any"} {
		_, err := e.Query(rel)
		require.Error(t, err, rel)
		assert.Contains(t, err.Error(), "unknown relation", rel)
	}
	// The two edge relations still derive, and action_filter still refines
	// class_filter per action rather than dropping or inflating rows.
	cf, err := e.Query("class_filter")
	require.NoError(t, err)
	af, err := e.Query("action_filter")
	require.NoError(t, err)
	assert.NotEmpty(t, cf)
	assert.NotEmpty(t, af)
	cfKeys := map[string]bool{}
	for _, tup := range cf {
		cfKeys[strings.Join(tup[:3], "/")] = true
	}
	for _, tup := range af {
		require.True(t, cfKeys[strings.Join(tup[:3], "/")],
			"action_filter row %v has no class_filter (class, reg, cb)", tup)
	}
}

// BenchmarkRailsFilters drives rules/ruby/rails_filters.dl end to end over the
// synthetic cedar-shaped fact base, running the two queries the emitter runs.
// Reports ns/op, allocs/op and B/op — the DL.3 phases are measured against it.
func BenchmarkRailsFilters(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := buildRailsFixture(b)
		b.StartTimer()

		if _, err := e.Query("class_filter"); err != nil {
			b.Fatal(err)
		}
		if _, err := e.Query("action_filter"); err != nil {
			b.Fatal(err)
		}
	}
}

// chainEngine loads the two-rule transitive closure over a parent chain
// a0 <- a1 <- ... <- a(depth). The closure has depth*(depth+1)/2 tuples, so
// doubling the depth quadruples the *answer*: the thing P.5 is measured on is
// cost per derived tuple, which naive evaluation cannot hold flat because it
// re-derives the whole relation once per round, and there are depth rounds.
func chainEngine(tb testing.TB, depth int) *datalog.Engine {
	tb.Helper()
	e := datalog.New(datalog.Options{MaxTuples: 10_000_000, MaxRounds: depth + 10})
	parent := make([]datalog.Tuple, 0, depth)
	for i := 1; i <= depth; i++ {
		parent = append(parent, datalog.Tuple{fmt.Sprintf("a%05d", i), fmt.Sprintf("a%05d", i-1)})
	}
	require.NoError(tb, e.Assert("parent", parent))
	require.NoError(tb, e.LoadRules([]byte(`
		ancestor(C, P) :- parent(C, P).
		ancestor(C, A) :- parent(C, P), ancestor(P, A).
	`), "chain.dl"))
	return e
}

// BenchmarkAncestorChain is P.5's acceptance measurement: an ancestor-style
// closure must scale linearly when chain depth is doubled. "Linearly" is per
// derived tuple — the closure itself is quadratic in depth — so the number to
// read is ns/tuple, which must stay flat as depth doubles rather than doubling
// with it.
func BenchmarkAncestorChain(b *testing.B) {
	for _, depth := range []int{125, 250, 500, 1000} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			b.ReportAllocs()
			tuples := 0
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				e := chainEngine(b, depth)
				b.StartTimer()

				got, err := e.Query("ancestor")
				if err != nil {
					b.Fatal(err)
				}
				tuples = len(got)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*tuples), "ns/tuple")
		})
	}
}

// TestDeepChainClosureIsComplete pins semi-naive evaluation against the one
// failure it can have that a small fixture would not show: a delta round that
// stops early derives a closure that is merely incomplete, not wrong-looking.
func TestDeepChainClosureIsComplete(t *testing.T) {
	t.Parallel()
	const depth = 200
	got, err := chainEngine(t, depth).Query("ancestor")
	require.NoError(t, err)
	require.Len(t, got, depth*(depth+1)/2, "every (descendant, ancestor) pair in the chain")
	assert.Equal(t, datalog.Tuple{"a00001", "a00000"}, got[0])
	assert.Equal(t, datalog.Tuple{fmt.Sprintf("a%05d", depth), fmt.Sprintf("a%05d", depth-1)}, got[len(got)-1])
}
