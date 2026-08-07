package linker_test

// Rails filter chain, through REAL parses (bug-class #6): the pass resolves a
// callback symbol against method nodes the Ruby extractor mints, so hand-built
// nodes would test a different program.
//
// The claim: `before_action :ensure_valid_token` becomes a call an agent can
// walk, from the class that registers it and from each action it guards.

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

const filterSvc = "nextGen"

var filterFixtureFiles = []string{
	"testdata/rails_filters/app/controllers/application_controller.rb",
	"testdata/rails_filters/app/controllers/categories_controller.rb",
	"testdata/rails_filters/app/controllers/client_api/v1/agents_controller.rb",
	"testdata/rails_filters/app/controllers/concerns/auditable.rb",
	"testdata/rails_filters/app/controllers/concerns/security_checks.rb",
	"testdata/rails_filters/app/controllers/concerns/task_security_checks.rb",
	"testdata/rails_filters/app/controllers/concerns/token_authenticatable.rb",
	"testdata/rails_filters/app/controllers/reports_controller.rb",
}

func filterFixture(t *testing.T) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	for _, f := range filterFixtureFiles {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, _, _, err := p.Parse(f, filterSvc, m)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
	}
	edges, unresolved := linker.LinkRailsFilters(nodes, map[string][]string{filterSvc: filterFixtureFiles})
	return nodes, edges, unresolved
}

// nodeIDFor returns the single node of a type with a label, failing otherwise.
func nodeIDFor(t *testing.T, nodes []graph.Node, typ graph.NodeType, label string) string {
	t.Helper()
	var ids []string
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			ids = append(ids, nodes[i].ID)
		}
	}
	require.Len(t, ids, 1, "expected one %s node labelled %q, got %v", typ, label, ids)
	return ids[0]
}

// methodID resolves a Ruby method by its Class#method qualified name.
func methodID(t *testing.T, nodes []graph.Node, qn string) string {
	t.Helper()
	for i := range nodes {
		if nodes[i].Meta["qualified_name"] == qn {
			return nodes[i].ID
		}
	}
	t.Fatalf("no method node for %q", qn)
	return ""
}

// filterTargets lists the labels a node calls through a filter registration,
// with the scope meta appended so class and action edges stay distinguishable.
func filterTargets(nodes []graph.Node, edges []graph.Edge, fromID string) []string {
	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	var out []string
	for _, e := range edges {
		if e.From != fromID || e.Meta["via"] != "rails_filter" {
			continue
		}
		label := e.To
		if n := byID[e.To]; n != nil {
			label = n.Label
		}
		out = append(out, label+"/"+e.Meta["scope"])
	}
	sort.Strings(out)
	return out
}

// TestLinkRailsFilters_ActionReachesCallback is the worked example: the token
// check that guards every request now sits on the path out of each action, and
// on the class that registers it.
func TestLinkRailsFilters_ActionReachesCallback(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	classID := nodeIDFor(t, nodes, graph.NodeTypeClass, "AgentsController")
	assert.Contains(t, filterTargets(nodes, edges, classID), "ensure_valid_token/class",
		"the registration itself is not walkable from the class that declares it")

	// Every action, because the registration carries no only:/except:.
	for _, action := range []string{"index", "show", "update", "register"} {
		id := methodID(t, nodes, "AgentsController#"+action)
		assert.Contains(t, filterTargets(nodes, edges, id), "ensure_valid_token/action",
			"%s does not reach the filter that runs before it", action)
	}
}

// TestLinkRailsFilters_OnlyAndExcept: the options are the reason action-scope
// edges exist at all -- the chain genuinely differs per action.
func TestLinkRailsFilters_OnlyAndExcept(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	// only: %i[show update]
	for _, action := range []string{"show", "update"} {
		id := methodID(t, nodes, "AgentsController#"+action)
		assert.Contains(t, filterTargets(nodes, edges, id), "load_agent/action")
	}
	for _, action := range []string{"index", "register"} {
		id := methodID(t, nodes, "AgentsController#"+action)
		assert.NotContains(t, filterTargets(nodes, edges, id), "load_agent/action",
			"%s is outside only: %%i[show update]", action)
	}

	// except: [:index]
	assert.NotContains(t, filterTargets(nodes, edges, methodID(t, nodes, "AgentsController#index")),
		"audit/action", "index is excluded from the after_action")
	assert.Contains(t, filterTargets(nodes, edges, methodID(t, nodes, "AgentsController#register")),
		"audit/action")
}

// TestLinkRailsFilters_MetaRecordsTheRegistration: an agent reading the edge has
// to be able to tell an after_action from a before_action, and a conditional
// filter from one that always runs.
func TestLinkRailsFilters_MetaRecordsTheRegistration(t *testing.T) {
	nodes, edges, _ := filterFixture(t)
	target := methodID(t, nodes, "AgentsController#with_timing")

	var seen bool
	for _, e := range edges {
		if e.To != target || e.Meta["scope"] != "class" {
			continue
		}
		seen = true
		assert.Equal(t, "around_action", e.Meta["filter"])
		assert.Equal(t, "true", e.Meta["conditional"], "if: :slow? means it may not run")
		assert.Equal(t, graph.EdgeTypeCalls, e.Type)
	}
	assert.True(t, seen, "no edge to the around_action callback")

	// A callback the class defines itself is certain; one reached through an
	// include is a reconstructed ancestor chain, and says so.
	own := methodID(t, nodes, "AgentsController#load_agent")
	mixed := methodID(t, nodes, "TokenAuthenticatable#ensure_valid_token")
	for _, e := range edges {
		switch e.To {
		case own:
			assert.Equal(t, graph.ConfidenceStatic, e.Confidence)
		case mixed:
			assert.Equal(t, graph.ConfidenceInferred, e.Confidence)
		}
	}
}

// TestLinkRailsFilters_PrivateMethodsAreNotActions: `private` is what separates
// an action from a helper. Without it every callback would appear to call every
// other callback.
func TestLinkRailsFilters_PrivateMethodsAreNotActions(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	for _, helper := range []string{"load_agent", "audit", "with_timing"} {
		id := methodID(t, nodes, "AgentsController#"+helper)
		assert.Empty(t, filterTargets(nodes, edges, id),
			"%s is private -- the router never dispatches to it, so no filter runs before it", helper)
	}
}

// TestLinkRailsFilters_BaseControllerStillProducesAnEdge: ApplicationController
// declares a filter and defines no actions. The class-scope edge is what keeps
// that registration from vanishing.
func TestLinkRailsFilters_BaseControllerStillProducesAnEdge(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	classID := nodeIDFor(t, nodes, graph.NodeTypeClass, "ApplicationController")
	assert.Equal(t, []string{"authenticate_user!/class"}, filterTargets(nodes, edges, classID))
}

// TestLinkRailsFilters_NotPropagatedToSubclasses pins the documented scope. The
// filter really does run for ReportsController#index at runtime, but minting it
// here means one line in a base controller producing an edge per action in the
// app -- a decision to make with a measurement, not by accident.
func TestLinkRailsFilters_NotPropagatedToSubclasses(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	id := methodID(t, nodes, "ReportsController#index")
	for _, tgt := range filterTargets(nodes, edges, id) {
		assert.NotContains(t, tgt, "authenticate_user!")
	}
}

// TestLinkRailsFilters_SkipIsNotARegistration: skip_before_action removes an
// inherited filter. Reading it as a registration would invent a call that never
// happens.
func TestLinkRailsFilters_SkipIsNotARegistration(t *testing.T) {
	nodes, edges, unresolved := filterFixture(t)

	target := methodID(t, nodes, "ApplicationController#authenticate_user!")
	classID := nodeIDFor(t, nodes, graph.NodeTypeClass, "ReportsController")
	for _, e := range edges {
		if e.To == target {
			assert.NotEqual(t, classID, e.From, "skip_before_action minted a call")
		}
	}
	for _, u := range unresolved {
		assert.NotEqual(t, "authenticate_user!", u.Name,
			"a skip is not an unresolved registration either")
	}
}

// TestLinkRailsFilters_UnresolvableIsLedgered (bug-class #12): a callback with
// no method anywhere in the ancestor chain, and a registration inside a
// concern's `included do` block, are both recorded rather than dropped or
// guessed at.
func TestLinkRailsFilters_UnresolvableIsLedgered(t *testing.T) {
	_, _, unresolved := filterFixture(t)

	kinds := map[string][]string{}
	for _, u := range unresolved {
		kinds[u.Kind] = append(kinds[u.Kind], u.Name)
	}
	assert.Equal(t, []string{"nonexistent_filter"}, kinds["rails_filter_unresolved"])

	// Two registrations named nothing this pass can bind: the `included do`
	// block in the concern, whose owner is whatever includes the module, and the
	// block whose only call has a receiver.
	var files []string
	for _, u := range unresolved {
		if u.Kind == "rails_filter_unattributed" {
			assert.Greater(t, u.Line, 0)
			files = append(files, u.File[strings.LastIndex(u.File, "/")+1:])
		}
	}
	sort.Strings(files)
	require.Equal(t, []string{"auditable.rb", "categories_controller.rb"}, files)
}

// TestLinkRailsFilters_InlineBlockForm: `before_action -> { require_access(x) }`
// names no symbol, and reading it as "unresolvable" would have written off more
// than a third of nextGen's registrations. The lambda body names the method.
func TestLinkRailsFilters_InlineBlockForm(t *testing.T) {
	nodes, edges, _ := filterFixture(t)

	index := methodID(t, nodes, "CategoriesController#index")
	show := methodID(t, nodes, "CategoriesController#show")

	assert.Contains(t, filterTargets(nodes, edges, index), "require_study_access/action")
	assert.Contains(t, filterTargets(nodes, edges, show), "require_study_access/action")

	// Options and a block on the same registration: both still read.
	assert.Contains(t, filterTargets(nodes, edges, index), "ensure_study/action")
	assert.NotContains(t, filterTargets(nodes, edges, show), "ensure_study/action",
		"only: %i[index] applies to the block form too")

	// The indirection is recorded, so a reader is not told the line says
	// `before_action :require_study_access` when it does not.
	target := methodID(t, nodes, "CategoriesController#require_study_access")
	for _, e := range edges {
		if e.To == target {
			assert.Equal(t, "block", e.Meta["form"])
		}
	}
	// A symbol registration in the same class is not mislabelled.
	plain := methodID(t, nodes, "CategoriesController#set_study")
	for _, e := range edges {
		if e.To == plain {
			assert.Empty(t, e.Meta["form"])
		}
	}
}

// TestLinkRailsFilters_ResolvesThroughAModuleChain: the callback is three hops
// up — controller → superclass → `include SecurityChecks` → `include
// TaskSecurityChecks`. The middle link is a module, and treating only classes as
// ancestors left six nextGen controllers reporting a callback the repo plainly
// defines.
func TestLinkRailsFilters_ResolvesThroughAModuleChain(t *testing.T) {
	nodes, edges, unresolved := filterFixture(t)

	assert.Contains(t, filterTargets(nodes, edges, methodID(t, nodes, "CategoriesController#index")),
		"can_manage_task_for_study?/action")
	for _, u := range unresolved {
		assert.NotEqual(t, "can_manage_task_for_study?", u.Name,
			"the ancestor walk stopped at the first module")
	}
}

// TestLinkRailsFilters_BlockWithReceiverContributesNothing: `Rails.logger.info`
// says nothing about the controller, and guessing `info` is a callback would be
// a fabricated edge (bug-class #12).
func TestLinkRailsFilters_BlockWithReceiverContributesNothing(t *testing.T) {
	nodes, edges, unresolved := filterFixture(t)

	classID := nodeIDFor(t, nodes, graph.NodeTypeClass, "CategoriesController")
	for _, tgt := range filterTargets(nodes, edges, classID) {
		assert.NotContains(t, tgt, "info")
		assert.NotContains(t, tgt, "logger")
	}
	for _, u := range unresolved {
		assert.NotEqual(t, "info", u.Name)
	}
}

// TestLinkRailsFilters_NoDanglingEdges (bug-class #10): every endpoint is a node
// that exists.
func TestLinkRailsFilters_NoDanglingEdges(t *testing.T) {
	nodes, edges, _ := filterFixture(t)
	byID := map[string]bool{}
	for i := range nodes {
		byID[nodes[i].ID] = true
	}
	require.NotEmpty(t, edges)
	for _, e := range edges {
		assert.True(t, byID[e.From], "edge from a node that does not exist: %s", e.From)
		assert.True(t, byID[e.To], "edge to a node that does not exist: %s", e.To)
		assert.NotEqual(t, e.From, e.To, "self-loop")
	}
}

// TestLinkRailsFilters_Deterministic (bug-class #2): the pass is built on maps
// keyed by service, file and qualified name.
func TestLinkRailsFilters_Deterministic(t *testing.T) {
	_, firstEdges, firstUnresolved := filterFixture(t)
	for i := 0; i < 3; i++ {
		_, edges, unresolved := filterFixture(t)
		require.Equal(t, firstEdges, edges, "edges differ across runs")
		require.Equal(t, firstUnresolved, unresolved, "ledger differs across runs")
	}
}
