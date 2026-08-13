package ops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJob_UpsertGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	j := ops.Job{
		ID:        "j-1",
		Kind:      "index",
		Args:      `{"full":false}`,
		State:     "running",
		StartedAt: "2026-08-13T00:00:00Z",
		Progress:  ops.JobProgress{Done: 1, Total: 10},
		LogTail:   []string{"line 1", "line 2"},
	}
	require.NoError(t, s.UpsertJob(ctx, j))

	got, err := s.GetJob(ctx, "j-1")
	require.NoError(t, err)
	assert.Equal(t, j.ID, got.ID)
	assert.Equal(t, j.Kind, got.Kind)
	assert.Equal(t, j.State, got.State)
	assert.Equal(t, j.Progress, got.Progress)
	assert.Equal(t, j.LogTail, got.LogTail)

	// Upsert again with a terminal state — same id, full replace.
	j.State = "succeeded"
	j.EndedAt = "2026-08-13T00:01:00Z"
	j.Progress = ops.JobProgress{Done: 10, Total: 10}
	j.Result = `{"nodes":5}`
	require.NoError(t, s.UpsertJob(ctx, j))

	got, err = s.GetJob(ctx, "j-1")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", got.State)
	assert.Equal(t, j.EndedAt, got.EndedAt)
	assert.Equal(t, j.Result, got.Result)
	assert.Equal(t, ops.JobProgress{Done: 10, Total: 10}, got.Progress)
}

func TestJob_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetJob(context.Background(), "j-missing")
	assert.True(t, errors.Is(err, ops.ErrJobNotFound))
}

func TestJob_ListNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertJob(ctx, ops.Job{ID: "j-1", Kind: "index", State: "succeeded", StartedAt: "2026-08-13T00:00:00Z"}))
	require.NoError(t, s.UpsertJob(ctx, ops.Job{ID: "j-2", Kind: "eval", State: "succeeded", StartedAt: "2026-08-13T00:01:00Z"}))
	require.NoError(t, s.UpsertJob(ctx, ops.Job{ID: "j-3", Kind: "reconcile", State: "running", StartedAt: "2026-08-13T00:02:00Z"}))

	list, err := s.ListJobs(ctx, 0)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []string{"j-3", "j-2", "j-1"}, []string{list[0].ID, list[1].ID, list[2].ID})
}

func TestJob_ListLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, s.UpsertJob(ctx, ops.Job{
			ID: string(rune('a' + i)), Kind: "index", State: "succeeded",
			StartedAt: "2026-08-13T00:0" + string(rune('0'+i)) + ":00Z",
		}))
	}
	list, err := s.ListJobs(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}
