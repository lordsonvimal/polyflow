package eval_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestSaveTrustStamp_PersistsScoredReport(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	report := eval.AggregateReport("chessleap", []eval.CaseResult{
		{CaseID: "c1", Recall: 1.0, SilentMisses: 0, HardFail: false},
		{CaseID: "c2", Recall: 0.5, SilentMisses: 2, HardFail: true},
	})

	require.NoError(t, eval.SaveTrustStamp(ctx, s, "chessleap", &report))

	got, err := graph.LoadTrustStamp(ctx, s)
	require.NoError(t, err)
	assert.True(t, got.Measured)
	assert.Equal(t, "chessleap", got.Corpus)
	assert.Equal(t, 2, got.Cases)
	assert.InDelta(t, 0.75, got.Recall, 1e-9)
	assert.Equal(t, 1, got.HardFails)
	assert.Equal(t, 2, got.SilentMisses)
	assert.NotEmpty(t, got.MeasuredAt)
}
