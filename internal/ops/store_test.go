package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *ops.Store {
	t.Helper()
	s, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordCall_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	call, evicted, err := s.RecordCall(ctx, ops.Call{
		Source: "cli", Tool: "search", Params: `{"query":"foo"}`,
		DurationMS: 12, Status: "ok", Result: `{"nodes":[]}`,
	})
	require.NoError(t, err)
	assert.Empty(t, evicted)
	assert.NotZero(t, call.ID)
	assert.NotEmpty(t, call.TS)
	assert.Equal(t, "cli", call.Source)
	assert.Equal(t, int64(12), call.ResultBytes)
	assert.False(t, call.ResultTruncated)
}

func TestRecordCall_CapsResultAndReportsTrueSize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	big := strings.Repeat("x", ops.MaxResultBytes+500)
	call, _, err := s.RecordCall(ctx, ops.Call{
		Source: "mcp", Tool: "search", Params: "{}", Status: "ok", Result: big,
	})
	require.NoError(t, err)
	assert.True(t, call.ResultTruncated)
	assert.Equal(t, int64(len(big)), call.ResultBytes)
	assert.Len(t, call.Result, ops.MaxResultBytes)

	list, err := s.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	require.Len(t, list.Calls, 1)
	assert.True(t, list.Calls[0].ResultTruncated)
	assert.Equal(t, int64(len(big)), list.Calls[0].ResultBytes)
}

func TestRecordCall_RetentionEvictsOldestRegardlessOfSourceMix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.SetRetention(ctx, 100)
	require.NoError(t, err)

	sources := []string{"cli", "mcp", "ui"}
	var lastEvicted []int64
	for i := 0; i < 105; i++ {
		_, evicted, err := s.RecordCall(ctx, ops.Call{
			Source: sources[i%3], Tool: "t", Params: "{}", Status: "ok",
		})
		require.NoError(t, err)
		if len(evicted) > 0 {
			lastEvicted = evicted
		}
	}
	assert.Len(t, lastEvicted, 1, "eviction runs one row at a time once over the cap")

	list, err := s.ListCalls(ctx, ops.ListFilter{Limit: 1000})
	require.NoError(t, err)
	assert.Equal(t, 100, list.Total)
	assert.Len(t, list.Calls, 100)
}

func TestSetRetention_LowerValueTrimsImmediately(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_, _, err := s.RecordCall(ctx, ops.Call{Source: "cli", Tool: "t", Params: "{}", Status: "ok"})
		require.NoError(t, err)
	}

	evicted, err := s.SetRetention(ctx, 3)
	require.NoError(t, err)
	assert.Len(t, evicted, 7)

	list, err := s.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 3, list.Total)
}

func TestSetRetention_RaisingAfterEvictionDoesNotResurrect(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_, _, err := s.RecordCall(ctx, ops.Call{Source: "cli", Tool: "t", Params: "{}", Status: "ok"})
		require.NoError(t, err)
	}
	_, err := s.SetRetention(ctx, 2)
	require.NoError(t, err)

	_, err = s.SetRetention(ctx, 100)
	require.NoError(t, err)

	list, err := s.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 2, list.Total, "raising the cap must not resurrect evicted rows")
}

func TestListCalls_FiltersAndPagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mustRecord := func(source, tool, status, params, result string) {
		_, _, err := s.RecordCall(ctx, ops.Call{Source: source, Tool: tool, Params: params, Status: status, Result: result})
		require.NoError(t, err)
	}
	mustRecord("cli", "search", "ok", `{"q":"needle"}`, `{"nodes":[]}`)
	mustRecord("mcp", "search", "ok", `{"q":"other"}`, `{"nodes":[1]}`)
	mustRecord("ui", "search", "error", `{"q":"x"}`, ``)

	list, err := s.ListCalls(ctx, ops.ListFilter{Source: "cli"})
	require.NoError(t, err)
	require.Len(t, list.Calls, 1)
	assert.Equal(t, "cli", list.Calls[0].Source)

	list, err = s.ListCalls(ctx, ops.ListFilter{Status: "error"})
	require.NoError(t, err)
	require.Len(t, list.Calls, 1)
	assert.Equal(t, "error", list.Calls[0].Status)

	// q matches params...
	list, err = s.ListCalls(ctx, ops.ListFilter{Q: "needle"})
	require.NoError(t, err)
	require.Len(t, list.Calls, 1)

	// ...and result (full-text over input AND output).
	list, err = s.ListCalls(ctx, ops.ListFilter{Q: `"nodes":[1]`})
	require.NoError(t, err)
	require.Len(t, list.Calls, 1)
	assert.Equal(t, "mcp", list.Calls[0].Source)

	list, err = s.ListCalls(ctx, ops.ListFilter{Page: 1, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, 3, list.Total)
	assert.Len(t, list.Calls, 2)
	// newest first
	assert.Equal(t, "ui", list.Calls[0].Source)
}

func TestListCalls_GrandTotalAndFacetCounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mustRecord := func(source, status string) {
		_, _, err := s.RecordCall(ctx, ops.Call{Source: source, Tool: "t", Params: "{}", Status: status})
		require.NoError(t, err)
	}
	mustRecord("cli", "ok")
	mustRecord("cli", "error")
	mustRecord("mcp", "ok")
	mustRecord("ui", "ok")
	mustRecord("ui", "ok")

	// Filter by source=ui: Total reflects the filter, GrandTotal ignores it,
	// the source facet is computed WITHOUT the source filter (so all three
	// sources show), the status facet WITH it (only ui rows).
	list, err := s.ListCalls(ctx, ops.ListFilter{Source: "ui"})
	require.NoError(t, err)
	assert.Equal(t, 2, list.Total)
	assert.Equal(t, 5, list.GrandTotal)
	assert.Equal(t, map[string]int{"cli": 2, "mcp": 1, "ui": 2}, list.Counts.Source)
	assert.Equal(t, map[string]int{"ok": 2}, list.Counts.Status)

	// No filter: both facets span everything.
	list, err = s.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 5, list.GrandTotal)
	assert.Equal(t, map[string]int{"ok": 4, "error": 1}, list.Counts.Status)
	assert.Equal(t, map[string]int{"cli": 2, "mcp": 1, "ui": 2}, list.Counts.Source)
}

func TestDeleteAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _, err := s.RecordCall(ctx, ops.Call{Source: "cli", Tool: "t", Params: "{}", Status: "ok"})
		require.NoError(t, err)
	}

	n, err := s.DeleteAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)

	list, err := s.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 0, list.Total)
}

func TestGetRetention_DefaultsWhenUnset(t *testing.T) {
	s := newTestStore(t)
	n, err := s.GetRetention(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ops.DefaultRetention, n)
}

// TestOpsDB_SurvivesGraphDBRebuild simulates the indexer's rebuild-then-
// atomic-rename of graph.db (internal/indexer/indexer.go ~line 251):
// ops.db is a separate file the indexer never touches, so tool_calls must
// still be there after graph.db is deleted and recreated next to it.
func TestOpsDB_SurvivesGraphDBRebuild(t *testing.T) {
	dir := t.TempDir()
	opsPath := filepath.Join(dir, "ops.db")
	graphPath := filepath.Join(dir, "graph.db")

	s, err := ops.Open(opsPath)
	require.NoError(t, err)
	ctx := context.Background()
	_, _, err = s.RecordCall(ctx, ops.Call{Source: "cli", Tool: "index", Params: "{}", Status: "ok"})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Simulate indexer.Run: build graph.db.tmp, then atomically rename over
	// graph.db. ops.db is never opened or touched by this path.
	require.NoError(t, os.WriteFile(graphPath+".tmp", []byte("fake graph contents"), 0o644))
	require.NoError(t, os.Rename(graphPath+".tmp", graphPath))

	s2, err := ops.Open(opsPath)
	require.NoError(t, err)
	defer s2.Close()
	list, err := s2.ListCalls(ctx, ops.ListFilter{})
	require.NoError(t, err)
	assert.Equal(t, 1, list.Total, "tool_calls must survive a graph.db rebuild")
}
