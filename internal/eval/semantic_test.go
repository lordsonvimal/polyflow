package eval

// Internal tests for semantic case runner (S.4).
// Uses package eval (not eval_test) to access unexported helpers.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// makeSemanticTestDB creates an in-memory SQLite store with schema applied and
// populates entities_fts with a set of synthetic entity entries.
func makeSemanticTestDB(t *testing.T, entities []semantic.Entity) *graph.SQLiteStore {
	t.Helper()
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()

	// Insert node records so LoadEntitiesByIDs can enrich file/line fields.
	for _, e := range entities {
		if e.Type != "node" {
			continue
		}
		n := &graph.Node{
			ID:      e.ID,
			Label:   nodeIDLabel(e.ID),
			Type:    graph.NodeTypeFunction,
			Service: "testsvc",
			File:    e.File,
			Line:    e.Line,
		}
		require.NoError(t, store.UpsertNode(ctx, n))
	}

	// Populate entities_fts via the semantic store.
	semStore := semantic.NewStore(store.DB())
	require.NoError(t, semStore.UpsertEntitiesFTS(ctx, entities))
	return store
}

// entityFTS builds a minimal Entity for FTS-only testing (no vector embedding).
func entityFTS(id, label, file string) semantic.Entity {
	return semantic.Entity{
		ID:          id,
		Type:        "node",
		Text:        fmt.Sprintf("%s function testsvc %s", label, file),
		ContentHash: label,
		NodeID:      id,
		File:        file,
		Line:        1,
	}
}

// TestSemanticCaseRunner_HitInTop10 — if the expected label appears in top-10
// results, recall=1 and hard_fail=false.
func TestSemanticCaseRunner_HitInTop10(t *testing.T) {
	entities := []semantic.Entity{
		entityFTS("svc:file.go:function:Foo:1", "Foo", "file.go"),
		entityFTS("svc:file.go:function:Bar:10", "Bar", "file.go"),
	}
	store := makeSemanticTestDB(t, entities)

	c := Case{
		ID:          "hit-test",
		Kind:        "semantic",
		Query:       "Foo function",
		Section:     "nodes",
		ExpectAnyOf: []string{"Foo"},
		MustNotMiss: []string{"Foo"},
	}

	cr, err := runSemanticCase(context.Background(), store, c)
	require.NoError(t, err)
	assert.Equal(t, "semantic", cr.Kind)
	assert.InDelta(t, 1.0, cr.Recall, 1e-9, "expected Foo to be in top-10 results")
	assert.False(t, cr.HardFail)
}

// TestSemanticCaseRunner_MissTop10 — if the expected label is absent from
// the DB entirely, recall=0 and hard_fail=true.
func TestSemanticCaseRunner_MissTop10(t *testing.T) {
	entities := []semantic.Entity{
		entityFTS("svc:file.go:function:Bar:10", "Bar", "file.go"),
	}
	store := makeSemanticTestDB(t, entities)

	c := Case{
		ID:          "miss-test",
		Kind:        "semantic",
		Query:       "NotHere function",
		Section:     "nodes",
		ExpectAnyOf: []string{"NotHere"},
		MustNotMiss: []string{"NotHere"},
	}

	cr, err := runSemanticCase(context.Background(), store, c)
	require.NoError(t, err)
	assert.Equal(t, "semantic", cr.Kind)
	assert.InDelta(t, 0.0, cr.Recall, 1e-9)
	assert.True(t, cr.HardFail)
}

// TestSemanticCaseRunner_Determinism — running the same semantic case twice on
// the same DB must return byte-identical CaseResult (bug-class rule 2).
func TestSemanticCaseRunner_Determinism(t *testing.T) {
	entities := []semantic.Entity{
		entityFTS("svc:file.go:function:Alpha:1", "Alpha", "file.go"),
		entityFTS("svc:file.go:function:Beta:5", "Beta", "file.go"),
		entityFTS("svc:file.go:function:Gamma:9", "Gamma", "file.go"),
	}
	store := makeSemanticTestDB(t, entities)

	c := Case{
		ID:          "determ-test",
		Kind:        "semantic",
		Query:       "Alpha function svc",
		Section:     "nodes",
		ExpectAnyOf: []string{"Alpha"},
		MustNotMiss: []string{"Alpha"},
	}

	ctx := context.Background()
	cr1, err1 := runSemanticCase(ctx, store, c)
	cr2, err2 := runSemanticCase(ctx, store, c)

	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, cr1, cr2, "two runs must produce identical CaseResult (rule 2)")
}

// TestSemanticCaseRunner_NoDB — if the DB has no entities_fts (pre-S.1 schema),
// the case scores as 0 results instead of returning an error.
func TestSemanticCaseRunner_NoDB(t *testing.T) {
	// Use a graph store but drop the entities_fts table to simulate pre-S.1 DB.
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	_, dropErr := store.DB().Exec(`DROP TABLE IF EXISTS entities_fts`)
	require.NoError(t, dropErr)

	c := Case{
		ID:          "nodb-test",
		Kind:        "semantic",
		Query:       "anything",
		Section:     "nodes",
		ExpectAnyOf: []string{"SomeFunc"},
		MustNotMiss: []string{"SomeFunc"},
	}

	cr, err := runSemanticCase(context.Background(), store, c)
	require.NoError(t, err, "pre-S.1 DB must not return an error — scores as 0 results")
	assert.Equal(t, "semantic", cr.Kind)
	assert.InDelta(t, 0.0, cr.Recall, 1e-9)
}

// TestNodeIDLabel tests label extraction from various node ID formats.
func TestNodeIDLabel(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"svc:path/file.go:function:MyFunc:42", "MyFunc"},
		{"svc:path/file.go:struct:MyStruct:1", "MyStruct"},
		{"svc:path/file.go:file", "file"},       // 3-component file node
		{"svc:file.go:function:Foo:0", "Foo"},
		{"", ""},                                  // empty
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := nodeIDLabel(tc.id)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAggregateReport_SemanticRecall verifies SemanticRecall is computed only
// from kind=semantic cases and excluded from the overall recall denominator
// (overall recall is macro-average over ALL cases).
func TestAggregateReport_SemanticRecall(t *testing.T) {
	results := []CaseResult{
		{CaseID: "impact-a", Kind: "", Recall: 1.0, Precision: 1.0},
		{CaseID: "sem-b", Kind: "semantic", Recall: 0.5, Precision: 1.0},
		{CaseID: "sem-c", Kind: "semantic", Recall: 1.0, Precision: 1.0},
	}
	r := AggregateReport("repo", results)

	// Overall recall = (1.0+0.5+1.0)/3 = 0.833...
	assert.InDelta(t, 2.5/3.0, r.Recall, 1e-9)
	// SemanticRecall = (0.5+1.0)/2 = 0.75
	assert.InDelta(t, 0.75, r.SemanticRecall, 1e-9)
}

// TestAggregateReport_NoSemanticCases — SemanticRecall is 0 (omitted from JSON)
// when no semantic cases exist.
func TestAggregateReport_NoSemanticCases(t *testing.T) {
	results := []CaseResult{
		{CaseID: "impact-a", Kind: "", Recall: 0.8, Precision: 1.0},
	}
	r := AggregateReport("repo", results)
	assert.InDelta(t, 0.0, r.SemanticRecall, 1e-9)
}
