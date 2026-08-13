package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func promoteNode(id, label, service, file string) *graph.Node {
	return &graph.Node{
		ID:      id,
		Type:    graph.NodeTypeFunction,
		Label:   label,
		Service: service,
		File:    file,
		Line:    1,
	}
}

func promoteEdge(id, from, to, state string) *graph.Edge {
	return &graph.Edge{
		ID:                id,
		From:              from,
		To:                to,
		Type:              graph.EdgeTypeCalls,
		VerificationState: state,
	}
}

func seedGapGraph(t *testing.T) *graph.SQLiteStore {
	t.Helper()
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	require.NoError(t, s.UpsertNode(ctx, promoteNode("svc:handler.go:function:handleReport:1", "handleReport", "go-svcc", "handler.go")))
	require.NoError(t, s.UpsertNode(ctx, promoteNode("svc:csv.go:function:parseCSV:1", "parseCSV", "go-svcc", "csv.go")))
	require.NoError(t, s.UpsertNode(ctx, promoteNode("svc:main.go:function:startup:1", "startup", "go-svcc", "main.go")))
	require.NoError(t, s.UpsertNode(ctx, promoteNode("svc:main.go:function:shutdown:1", "shutdown", "go-svcc", "main.go")))

	require.NoError(t, s.UpsertEdge(ctx, promoteEdge("e1", "svc:handler.go:function:handleReport:1", "svc:csv.go:function:parseCSV:1", graph.StateObservedOnlyGap)))
	require.NoError(t, s.UpsertEdge(ctx, promoteEdge("e2", "svc:main.go:function:startup:1", "svc:main.go:function:shutdown:1", graph.StateVerified)))
	return s
}

func TestPromoteGaps_OnlyPromotesGapEdges(t *testing.T) {
	s := seedGapGraph(t)
	cases, err := eval.PromoteGaps(context.Background(), s, &eval.Manifest{})
	require.NoError(t, err)
	require.Len(t, cases, 1)

	c := cases[0]
	assert.Equal(t, eval.GapCaseID("svc:handler.go:function:handleReport:1", "svc:csv.go:function:parseCSV:1"), c.ID)
	assert.Equal(t, "node", c.Kind)
	assert.Equal(t, "parseCSV", c.Target)
	assert.Equal(t, "go-svcc", c.Service)
	assert.Empty(t, c.ExpectedImpacted)
	assert.Equal(t, []string{"handler.go"}, c.MustNotMiss)
}

func TestPromoteGaps_DedupesAgainstExisting(t *testing.T) {
	s := seedGapGraph(t)
	id := eval.GapCaseID("svc:handler.go:function:handleReport:1", "svc:csv.go:function:parseCSV:1")
	existing := &eval.Manifest{Cases: []eval.Case{{ID: id}}}

	cases, err := eval.PromoteGaps(context.Background(), s, existing)
	require.NoError(t, err)
	assert.Empty(t, cases)
}

func TestPromoteGaps_SortedByID(t *testing.T) {
	s := seedGapGraph(t)
	ctx := context.Background()
	require.NoError(t, s.UpsertNode(ctx, promoteNode("svc:other.go:function:other:1", "other", "go-svcc", "other.go")))
	require.NoError(t, s.UpsertEdge(ctx, promoteEdge("e3", "svc:other.go:function:other:1", "svc:main.go:function:shutdown:1", graph.StateObservedOnlyGap)))

	cases, err := eval.PromoteGaps(ctx, s, &eval.Manifest{})
	require.NoError(t, err)
	require.Len(t, cases, 2)
	assert.Less(t, cases[0].ID, cases[1].ID)
}

func TestAppendCasesToManifest_IdempotentAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	original := "repo:\n  name: fixture\n  path: .\n  sha: deadbeef\n  workspace: polyflow.yml\n\ncases:\n  - id: existing-case\n    kind: node\n    target: Foo\n    expected_impacted:\n      - a.go\n    must_not_miss:\n      - a.go\n"
	require.NoError(t, os.WriteFile(manifestPath, []byte(original), 0o644))

	cases := []eval.Case{
		{ID: "gap-aaaa1111", Kind: "node", Target: "parseCSV", Service: "go-svcc", ExpectedImpacted: []string{}, MustNotMiss: []string{"handler.go"}},
	}
	require.NoError(t, eval.AppendCasesToManifest(dir, cases))

	first, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Contains(t, string(first), "gap-aaaa1111")
	assert.Contains(t, string(first), "expected_impacted: []")

	// Applying an empty case list (the idempotent second-run shape once the
	// caller dedupes against the now-updated manifest) must not touch the file.
	require.NoError(t, eval.AppendCasesToManifest(dir, nil))
	second, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, first, second)

	m, err := eval.LoadManifest(dir)
	require.NoError(t, err)
	require.Len(t, m.Cases, 2)
	assert.Equal(t, "gap-aaaa1111", m.Cases[1].ID)
}

func TestAppendCasesToManifest_NoopOnEmpty(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte("repo:\n  name: fixture\n"), 0o644))

	require.NoError(t, eval.AppendCasesToManifest(dir, nil))

	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, "repo:\n  name: fixture\n", string(data))
}
