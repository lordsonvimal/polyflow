package linker

// Tier DL.1's own claims, asserted against the fact base rather than the edges.
// The external rails_filters_test.go already runs every behavioural case under
// both decision procedures; what is left is what only the rule path can say.

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

var dlFixtureFiles = []string{
	"testdata/rails_filters/app/controllers/application_controller.rb",
	"testdata/rails_filters/app/controllers/categories_controller.rb",
	"testdata/rails_filters/app/controllers/client_api/v1/agents_controller.rb",
	"testdata/rails_filters/app/controllers/client_api/v1/api_base_controller.rb",
	"testdata/rails_filters/app/controllers/client_api/v1/repository_controller.rb",
	"testdata/rails_filters/app/controllers/documents_controller.rb",
	"testdata/rails_filters/app/controllers/errors_controller.rb",
	"testdata/rails_filters/app/controllers/repository_controller.rb",
	"testdata/rails_filters/app/controllers/concerns/auditable.rb",
	"testdata/rails_filters/app/controllers/concerns/security_checks.rb",
	"testdata/rails_filters/app/controllers/concerns/task_security_checks.rb",
	"testdata/rails_filters/app/controllers/concerns/token_authenticatable.rb",
	"testdata/rails_filters/app/controllers/public_pages_controller.rb",
	"testdata/rails_filters/app/controllers/reports_controller.rb",
}

func dlProgramFixture(t *testing.T) (*filterIndex, *dlProgram) {
	t.Helper()
	// nil nodes: the fact base comes entirely from re-parsing the files. The
	// node set only supplies method IDs, which this level never reaches.
	ix := newFilterIndex(nil, "orion", dlFixtureFiles)
	p, err := ix.datalogProgram()
	require.NoError(t, err)
	return ix, p
}

// shortKey trims a class key back to the constant path, so a failure message
// reads like Ruby rather than like a file path.
func shortKey(k string) string {
	if i := strings.Index(k, "@"); i >= 0 {
		return k[:i]
	}
	return k
}

func pairs(t *testing.T, e *datalog.Engine, rel string) []string {
	t.Helper()
	ts, err := e.Query(rel)
	require.NoError(t, err)
	out := make([]string, 0, len(ts))
	for _, tup := range ts {
		out = append(out, shortKey(tup[0])+" -> "+shortKey(tup[1]))
	}
	sort.Strings(out)
	return out
}

// TestDatalogSeparatesMixinsFromSuperclasses is the defect the tier fixes for
// free. The `inherits` edge conflates superclasses with mixins (1595 vs 670 on
// the audit corpus), so every caller has to filter meta.via != "mixin" before
// treating it as inheritance — and the ones that forget are wrong silently.
//
// As two relations there is nothing to conflate: `ancestor` is the superclass
// chain and only that, `includes_module` is `include`/`extend` and only that,
// and no rule can read one for the other.
func TestDatalogSeparatesMixinsFromSuperclasses(t *testing.T) {
	_, p := dlProgramFixture(t)

	anc := pairs(t, p.engine, "ancestor")
	inc := pairs(t, p.engine, "includes_module")

	assert.Contains(t, anc, "CategoriesController -> ApplicationController")
	assert.Contains(t, inc, "ApplicationController -> SecurityChecks")
	assert.Contains(t, inc, "SecurityChecks -> TaskSecurityChecks",
		"includes_module is transitive through a concern that includes a concern")

	// The separation itself: not one mixin appears in the superclass chain,
	// and not one superclass appears as a mixin.
	for _, a := range anc {
		assert.NotContains(t, inc, a, "%q is in both relations", a)
	}

	// And a module is never an ancestor of anything.
	modules := map[string]bool{}
	for _, c := range _classesOf(p) {
		if c.isModule {
			modules[c.qualified()] = true
		}
	}
	require.NotEmpty(t, modules, "the fixture has concerns; if not, this test proves nothing")
	for _, a := range anc {
		_, sup, _ := strings.Cut(a, " -> ")
		assert.False(t, modules[sup], "module %s reached through `ancestor`, which is `< Super` only", sup)
	}
}

func _classesOf(p *dlProgram) []*ctrlClass {
	out := make([]*ctrlClass, 0, len(p.classKeys))
	for c := range p.classKeys {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return p.classKeys[out[i]] < p.classKeys[out[j]] })
	return out
}

// TestDatalogProvenanceReachesTheRegistration is the SA.1 half of the DL.1
// acceptance: for a derived edge, the rule that fired and the base facts it
// consumed, walkable to the line of Ruby that caused it.
func TestDatalogProvenanceReachesTheRegistration(t *testing.T) {
	ix, p := dlProgramFixture(t)

	require.NotEmpty(t, ix.classes)

	rows, err := p.engine.Query("action_filter")
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	// The acceptance criterion samples ten derived edges. Evenly spaced rather
	// than the first ten, so the sample is not all one controller's chain.
	var sampled int
	step := len(rows) / 10
	if step < 1 {
		step = 1
	}
	for i := 0; i < len(rows); i += step {
		row := rows[i]
		ds, err := p.engine.Provenance("action_filter", row)
		require.NoError(t, err)
		require.NotEmpty(t, ds, "no derivation for %v", row)
		assert.Equal(t, "rails_filters/action_filter", ds[0].Rule)

		// Walk one step back: the effective_filter this rested on, and from
		// there the registration that a class body actually wrote.
		var eff *datalog.Tuple
		for i := range ds[0].Body {
			if ds[0].Body[i].Relation == "effective_filter" {
				tup := ds[0].Body[i].Tuple
				eff = &tup
			}
		}
		require.NotNil(t, eff, "action_filter must rest on effective_filter")
		effDs, err := p.engine.Provenance("effective_filter", *eff)
		require.NoError(t, err)
		require.NotEmpty(t, effDs)
		assert.Equal(t, "rails_filters/effective_filter", effDs[0].Rule)

		// The registration key names the declaration and the line it is on,
		// which is the point: "why is this edge here" ends at a file:line.
		reg, ok := p.regs[(*eff)[1]]
		require.True(t, ok, "registration key %q is not one the emitter minted", (*eff)[1])
		assert.NotEmpty(t, reg.owner.file)
		assert.Positive(t, reg.reg.line)
		sampled++
	}
	require.GreaterOrEqual(t, sampled, 10,
		"the acceptance criterion samples 10 edges; the fixture must offer at least that many")
}

// TestDatalogInheritedFilterIsNegationNotOrdering: `inherited_filter` is
// `registered` minus `own_filter`, which is the pass's one real use of
// stratified negation. A class that declares a filter itself must never appear
// in it, however far up the chain the same callback is also registered.
func TestDatalogInheritedFilterIsNegationNotOrdering(t *testing.T) {
	ix, p := dlProgramFixture(t)

	inherited, err := p.engine.Query("inherited_filter")
	require.NoError(t, err)
	require.NotEmpty(t, ix.classes)
	for _, row := range inherited {
		reg, ok := p.regs[row[1]]
		require.True(t, ok)
		assert.NotEqual(t, p.classKeys[reg.owner], row[0],
			"a class's own registration is not inherited by it")
	}
	require.NotEmpty(t, inherited, "the fixture's whole point is that filters inherit")
}
