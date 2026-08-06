package graph_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrustStamp_UnmeasuredZeroState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	stamp, err := graph.LoadTrustStamp(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, graph.TrustStamp{Measured: false}, stamp)
}

func TestTrustStamp_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	written := graph.TrustStamp{
		Measured:     true,
		Corpus:       "chessleap",
		Cases:        12,
		Recall:       1.0,
		HardFails:    0,
		SilentMisses: 0,
		MeasuredAt:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := graph.EncodeTrustStamp(written)
	require.NoError(t, err)
	require.NoError(t, s.SetMeta(ctx, graph.TrustStampMetaKey, string(data)))

	got, err := graph.LoadTrustStamp(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, written, got)
}

func TestTrustStamp_EncodeIsSortedAndDeterministic(t *testing.T) {
	stamp := graph.TrustStamp{
		Measured:     true,
		Corpus:       "chessleap",
		Cases:        12,
		Recall:       1.0,
		HardFails:    0,
		SilentMisses: 0,
		MeasuredAt:   "2026-07-19T10:31:00Z",
	}
	a, err := graph.EncodeTrustStamp(stamp)
	require.NoError(t, err)
	b, err := graph.EncodeTrustStamp(stamp)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.Equal(t,
		`{"cases":12,"corpus":"chessleap","measured_at":"2026-07-19T10:31:00Z","recall":1}`,
		string(a))
}

func TestTrustStamp_Staleness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	measuredAt := time.Now().Add(-time.Hour).UTC()
	stamp := graph.TrustStamp{
		Measured:   true,
		Corpus:     "chessleap",
		Cases:      1,
		Recall:     1.0,
		MeasuredAt: measuredAt.Format(time.RFC3339),
	}
	data, err := graph.EncodeTrustStamp(stamp)
	require.NoError(t, err)
	require.NoError(t, s.SetMeta(ctx, graph.TrustStampMetaKey, string(data)))

	// Index runs after the measurement — the stamp should read as stale.
	reindexedAt := time.Now()
	require.NoError(t, s.SetMeta(ctx, "last_indexed", strconv.FormatInt(reindexedAt.Unix(), 10)))

	got, err := graph.LoadTrustStamp(ctx, s)
	require.NoError(t, err)
	assert.True(t, got.Stale)

	// A measurement taken after the last index is not stale.
	require.NoError(t, s.SetMeta(ctx, graph.TrustStampMetaKey, string(mustEncode(t, graph.TrustStamp{
		Measured:   true,
		MeasuredAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}))))
	got2, err := graph.LoadTrustStamp(ctx, s)
	require.NoError(t, err)
	assert.False(t, got2.Stale)
}

func mustEncode(t *testing.T, stamp graph.TrustStamp) []byte {
	t.Helper()
	data, err := graph.EncodeTrustStamp(stamp)
	require.NoError(t, err)
	return data
}
