package deadcode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// fixtureIndex builds:
//
//	backend:handler (http_handler, zero inbound calls — an entry point, must NOT be flagged)
//	backend:handler --calls--> backend:used (real caller, must NOT be flagged)
//	backend:orphan (zero inbound edges at all, must be flagged)
//	backend:File --contains--> backend:contained (only a structural inbound edge, must still be flagged)
func fixtureIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "be:handler", Type: graph.NodeTypeHTTPHandler, Label: "GET /api/x", Service: "backend", File: "handler.go", Line: 10})
	idx.AddNode(&graph.Node{ID: "be:used", Type: graph.NodeTypeFunction, Label: "used", Service: "backend", File: "used.go", Line: 20})
	idx.AddNode(&graph.Node{ID: "be:orphan", Type: graph.NodeTypeFunction, Label: "orphan", Service: "backend", File: "orphan.go", Line: 30})
	idx.AddNode(&graph.Node{ID: "be:file", Type: graph.NodeTypeFile, Label: "contained.go", Service: "backend", File: "contained.go", Line: 0})
	idx.AddNode(&graph.Node{ID: "be:contained", Type: graph.NodeTypeFunction, Label: "contained", Service: "backend", File: "contained.go", Line: 5})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "be:handler", To: "be:used", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "be:file", To: "be:contained", Type: graph.EdgeTypeContains})
	return idx
}

func TestBuild_FlagsOnlyZeroCallerNonEntrypoints(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	require.Equal(t, 2, out.Total)
	ids := []string{out.Functions[0].ID, out.Functions[1].ID}
	assert.ElementsMatch(t, []string{"be:orphan", "be:contained"}, ids)
}

func TestBuild_EntrypointNotFlaggedDespiteZeroCallers(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	for _, f := range out.Functions {
		assert.NotEqual(t, "be:handler", f.ID)
	}
}

func TestBuild_ContainsEdgeIsNotARealCaller(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	var found bool
	for _, f := range out.Functions {
		if f.ID == "be:contained" {
			found = true
		}
	}
	assert.True(t, found, "a node reached only by a structural contains edge must still be flagged dead")
}

func TestBuild_RealCallerExcludesFromResult(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	for _, f := range out.Functions {
		assert.NotEqual(t, "be:used", f.ID)
	}
}

func TestBuild_ServiceFilter(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "fe:orphan", Type: graph.NodeTypeFunction, Label: "orphan", Service: "frontend", File: "orphan.js", Line: 1})

	out := deadcode.Build(idx, deadcode.Options{Service: "backend"})
	for _, f := range out.Functions {
		assert.Equal(t, "backend", f.Service)
	}
}

func TestBuild_FileFilter(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{File: "orphan.go"})

	require.Equal(t, 1, out.Total)
	assert.Equal(t, "be:orphan", out.Functions[0].ID)
}

func TestBuild_CallbackRootKindNotFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "be:on_proceed", Type: graph.NodeTypeFunction, Label: "onProceed",
		Service: "backend", File: "popovers.js", Line: 60,
		Meta: map[string]string{"root_kind": "callback"},
	})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:on_proceed", f.ID, "root_kind=callback (object-literal handler / SSA-referenced value) must not be flagged")
	}
}

func TestBuild_UnreachableRootKindStillFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "be:dead", Type: graph.NodeTypeFunction, Label: "dead",
		Service: "backend", File: "dead.go", Line: 60,
		Meta: map[string]string{"root_kind": "unreachable"},
	})

	out := deadcode.Build(idx, deadcode.Options{})
	var found bool
	for _, f := range out.Functions {
		if f.ID == "be:dead" {
			found = true
		}
	}
	assert.True(t, found, "root_kind=unreachable is exactly the deadcode verdict and must still be flagged")
}

func TestBuild_ReflectDispatchedMethodNotFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "be:table_name", Type: graph.NodeTypeMethod, Label: "TableName",
		Service: "backend", File: "model.go", Line: 40, Language: "go",
		Meta: map[string]string{graph.MetaReflectDispatched: "true"},
	})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:table_name", f.ID, "a node the indexer stamped reflect_dispatched must not be flagged")
	}
}

// TestBuild_DeviseOverrideHookNotFlagged is Tier DV.3's worked example:
// orion-atlas/app/models/user.rb:105's `password_required?` is a private
// method DatabaseAuthenticatable calls on `self` from Devise's own gem
// source — zero inbound calls/spawns edges, and no in-repo call site will
// ever produce one. patterns/ruby/devise.yaml's reflect_dispatched_methods
// (package: devise) is what the indexer's stampReflectDispatched reads to
// stamp graph.MetaReflectDispatched here before Build ever runs; this test
// exercises the same mechanism TestBuild_ReflectDispatchedMethodNotFlagged
// does, pinned to the exact live method name and Ruby's node-type shape
// (graph.NodeTypeFunction — Ruby has no separate method/function split by
// receiver the way Go does, see indexer.stampReflectDispatched).
func TestBuild_DeviseOverrideHookNotFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "atlas:password_required", Type: graph.NodeTypeFunction, Label: "password_required?",
		Service: "orion-atlas", File: "app/models/user.rb", Line: 105, Language: "ruby",
		Meta: map[string]string{graph.MetaReflectDispatched: "true"},
	})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "atlas:password_required", f.ID, "a Devise override hook stamped reflect_dispatched must not be flagged")
	}
}

// TestBuild_MigrationMethodNotFlagged is Tier DC.2's worked example: a Rails
// migration's change/up/down methods (db/migrate/*.rb) are invoked by the
// `rails db:migrate` runner by filename+method-name convention — zero
// in-repo call sites, ever. internal/indexer/stampReflectDispatched (gated by
// patterns/ruby/active_record_migration.yaml, package: activerecord, scoped
// to db/migrate/ via reflect_dispatched_path_prefix) is what stamps
// graph.MetaReflectDispatched here before Build runs; this exercises the
// same mechanism TestBuild_ReflectDispatchedMethodNotFlagged does.
func TestBuild_MigrationMethodNotFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "be:migration_change", Type: graph.NodeTypeFunction, Label: "change",
		Service: "backend", File: "db/migrate/20260101_add_x.rb", Line: 3, Language: "ruby",
		Meta: map[string]string{graph.MetaReflectDispatched: "true"},
	})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:migration_change", f.ID, "a Rails migration's change method stamped reflect_dispatched must not be flagged")
	}
}

// TestBuild_SameNamedMethodOutsideMigrationStillFlagged guards against the
// over-widening risk DC.2's plan explicitly calls out: change/up/down are
// common English words with no gem-specific spelling, so the exclusion must
// only apply to a node the indexer actually stamped (i.e. one that passed
// the db/migrate/ path-prefix check upstream) — a same-named method the
// indexer did NOT stamp (no gem, or outside a migration-shaped path) must
// stay a real deadcode candidate.
func TestBuild_SameNamedMethodOutsideMigrationStillFlagged(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{
		ID: "be:coin_change", Type: graph.NodeTypeFunction, Label: "change",
		Service: "backend", File: "app/models/coin.rb", Line: 10, Language: "ruby",
	})

	out := deadcode.Build(idx, deadcode.Options{})
	var found bool
	for _, f := range out.Functions {
		if f.ID == "be:coin_change" {
			found = true
		}
	}
	assert.True(t, found, "a same-named method outside db/migrate/ (never stamped reflect_dispatched) must still be flagged")
}

func TestBuild_SpawnsEdgeCountsAsCaller(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "be:worker_loop", Type: graph.NodeTypeMethod, Label: "loop", Service: "backend", File: "scheduler.go", Line: 50})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "be:handler", To: "be:worker_loop", Type: graph.EdgeTypeSpawns})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:worker_loop", f.ID, "a `go x.method()` target has a real caller via EdgeTypeSpawns")
	}
}

func TestBuild_RendersEdgeCountsAsCaller(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "fe:alert", Type: graph.NodeTypeFunction, Label: "Alert", Service: "backend", File: "Alert.tsx", Line: 5})
	idx.AddEdge(&graph.Edge{ID: "e4", From: "be:handler", To: "fe:alert", Type: graph.EdgeTypeRenders})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "fe:alert", f.ID, "a JSX usage site is a real caller via EdgeTypeRenders")
	}
}

func TestBuild_JobEnqueueAndPerformEdgesCountAsCallers(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "be:enqueued", Type: graph.NodeTypeFunction, Label: "perform", Service: "backend", File: "report_job.rb", Line: 5})
	idx.AddNode(&graph.Node{ID: "be:performed", Type: graph.NodeTypeMethod, Label: "perform", Service: "backend", File: "job.rb", Line: 5})
	idx.AddEdge(&graph.Edge{ID: "e5", From: "be:handler", To: "be:enqueued", Type: graph.EdgeTypeJobEnqueue})
	idx.AddEdge(&graph.Edge{ID: "e6", From: "be:handler", To: "be:performed", Type: graph.EdgeTypeJobPerform})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:enqueued", f.ID, "the contract engine resolves job_enqueue straight onto the real perform method")
		assert.NotEqual(t, "be:performed", f.ID, "job_perform is a real invocation the same way job_enqueue is")
	}
}

func TestBuild_SidekiqAliasEdgesCountAsCallers(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "be:sk_enqueued", Type: graph.NodeTypeFunction, Label: "perform_async", Service: "backend", File: "worker.rb", Line: 5})
	idx.AddNode(&graph.Node{ID: "be:sk_performed", Type: graph.NodeTypeMethod, Label: "perform", Service: "backend", File: "worker.rb", Line: 10})
	idx.AddEdge(&graph.Edge{ID: "e7", From: "be:handler", To: "be:sk_enqueued", Type: graph.EdgeTypeSidekiqEnqueue})
	idx.AddEdge(&graph.Edge{ID: "e8", From: "be:handler", To: "be:sk_performed", Type: graph.EdgeTypeSidekiqPerform})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:sk_enqueued", f.ID, "sidekiq_enqueue is a deprecated alias for job_enqueue, same treatment")
		assert.NotEqual(t, "be:sk_performed", f.ID, "sidekiq_perform is a deprecated alias for job_perform, same treatment")
	}
}

func TestBuild_SubscribesEdgeCountsAsCaller(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "be:consumer", Type: graph.NodeTypeFunction, Label: "handle_message", Service: "backend", File: "consumer.rb", Line: 5})
	idx.AddEdge(&graph.Edge{ID: "e9", From: "be:handler", To: "be:consumer", Type: graph.EdgeTypeSubscribes})

	out := deadcode.Build(idx, deadcode.Options{})
	for _, f := range out.Functions {
		assert.NotEqual(t, "be:consumer", f.ID, "an AMQP subscribes edge resolved onto a real handler is a real invocation")
	}
}

func TestBuild_EmptyResultIsEmptySliceNotNil(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "be:handler", Type: graph.NodeTypeHTTPHandler, Label: "GET /x", Service: "backend", File: "h.go", Line: 1})

	out := deadcode.Build(idx, deadcode.Options{})
	require.NotNil(t, out.Functions)
	assert.Equal(t, 0, out.Total)
}
