package graph_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// makeRows builds a slice of UnresolvedHistoryRow from a compact spec.
// spec: []struct{runAt, service, kind, count}.
func makeRows(specs []struct {
	runAt   int64
	service string
	kind    string
	count   int
}) []graph.UnresolvedHistoryRow {
	out := make([]graph.UnresolvedHistoryRow, len(specs))
	for i, s := range specs {
		out[i] = graph.UnresolvedHistoryRow{RunAt: s.runAt, Service: s.service, Kind: s.kind, Count: s.count}
	}
	return out
}

// ── History write / read ─────────────────────────────────────────────────────

func TestUnresolvedHistory_WriteRead(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "*.db")
	f.Close()
	store, err := graph.NewSQLiteStore(f.Name())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	rows := []graph.UnresolvedHistoryRow{
		{RunAt: 1000, Service: "svc-a", Kind: "call_ref", Count: 5},
		{RunAt: 1000, Service: "svc-a", Kind: "import_ref", Count: 2},
		{RunAt: 1000, Service: "svc-b", Kind: "call_ref", Count: 1},
	}
	if err := store.WriteUnresolvedHistory(ctx, rows); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := store.ListUnresolvedHistory(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	// Newest first; within same run_at, sorted by service/kind.
	if got[0].Service != "svc-a" || got[0].Kind != "call_ref" || got[0].Count != 5 {
		t.Errorf("row[0] mismatch: %+v", got[0])
	}
	if got[1].Kind != "import_ref" {
		t.Errorf("row[1] mismatch: %+v", got[1])
	}
	if got[2].Service != "svc-b" {
		t.Errorf("row[2] mismatch: %+v", got[2])
	}
}

func TestUnresolvedHistory_MultipleRuns(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "*.db")
	f.Close()
	store, _ := graph.NewSQLiteStore(f.Name())
	defer store.Close()
	ctx := context.Background()

	for _, runAt := range []int64{1000, 2000, 3000} {
		_ = store.WriteUnresolvedHistory(ctx, []graph.UnresolvedHistoryRow{
			{RunAt: runAt, Service: "svc", Kind: "call_ref", Count: int(runAt / 100)},
		})
	}

	// nRuns=2 → only the two most recent run_at values (3000, 2000)
	got, _ := store.ListUnresolvedHistory(ctx, 2)
	if len(got) != 2 {
		t.Fatalf("want 2 rows (2 runs), got %d", len(got))
	}
	if got[0].RunAt != 3000 {
		t.Errorf("expected newest first, got run_at=%d", got[0].RunAt)
	}
}

// ── Retention / prune ────────────────────────────────────────────────────────

func TestUnresolvedHistory_Retention(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "*.db")
	f.Close()
	store, _ := graph.NewSQLiteStore(f.Name())
	defer store.Close()
	ctx := context.Background()

	// Write 55 distinct run_at values.
	for i := int64(1); i <= 55; i++ {
		_ = store.WriteUnresolvedHistory(ctx, []graph.UnresolvedHistoryRow{
			{RunAt: i * 1000, Service: "svc", Kind: "call_ref", Count: int(i)},
		})
	}

	if err := store.PruneUnresolvedHistory(ctx, 50); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// All rows should now be within the last 50 run timestamps.
	got, _ := store.ListUnresolvedHistory(ctx, 100)
	if len(got) != 50 {
		t.Errorf("want 50 rows after prune, got %d", len(got))
	}
	// Oldest remaining run should be run_at=6000 (runs 6..55 = 50 runs).
	oldest := got[len(got)-1]
	if oldest.RunAt != 6000 {
		t.Errorf("oldest remaining run_at should be 6000, got %d", oldest.RunAt)
	}
}

// ── Trend math ───────────────────────────────────────────────────────────────

func TestComputeTrend_Basic(t *testing.T) {
	// Three runs, newest first (as ListUnresolvedHistory returns them).
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 3000, Service: "svc-a", Kind: "call_ref", Count: 15},
		{RunAt: 2000, Service: "svc-a", Kind: "call_ref", Count: 10},
		{RunAt: 1000, Service: "svc-a", Kind: "call_ref", Count: 8},
	}
	rows := graph.ComputeTrend(history, 3) // compare latest vs 3 runs ago
	if len(rows) != 1 {
		t.Fatalf("want 1 trend row, got %d", len(rows))
	}
	r := rows[0]
	if r.Latest != 15 {
		t.Errorf("Latest: want 15, got %d", r.Latest)
	}
	if r.Baseline != 8 { // 3rd run from newest = oldest of 3 = run_at 1000
		t.Errorf("Baseline: want 8, got %d", r.Baseline)
	}
	if r.Delta != 7 {
		t.Errorf("Delta: want 7, got %d", r.Delta)
	}
	if r.Runs != 3 {
		t.Errorf("Runs: want 3, got %d", r.Runs)
	}
}

func TestComputeTrend_FewerRunsThanNBack(t *testing.T) {
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 2000, Service: "svc", Kind: "call_ref", Count: 10},
		{RunAt: 1000, Service: "svc", Kind: "call_ref", Count: 6},
	}
	rows := graph.ComputeTrend(history, 5) // want 5 back, only 2 exist
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Baseline != 6 { // oldest available
		t.Errorf("Baseline should be oldest available (6), got %d", r.Baseline)
	}
	if r.Delta != 4 {
		t.Errorf("Delta: want 4, got %d", r.Delta)
	}
}

func TestComputeTrend_DecreasingDelta(t *testing.T) {
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 2000, Service: "svc", Kind: "call_ref", Count: 3},
		{RunAt: 1000, Service: "svc", Kind: "call_ref", Count: 10},
	}
	rows := graph.ComputeTrend(history, 2)
	if rows[0].Delta != -7 {
		t.Errorf("want Delta=-7 (decreasing), got %d", rows[0].Delta)
	}
}

func TestComputeTrend_Empty(t *testing.T) {
	rows := graph.ComputeTrend(nil, 5)
	if rows != nil {
		t.Errorf("want nil for empty history")
	}
}

// ── Consecutive growth detection ─────────────────────────────────────────────

func TestDetectGrowth_Flags(t *testing.T) {
	// svc-a grows 3 runs in a row (newest first): 30, 20, 10.
	// svc-b does NOT grow (flat then up): 5, 5.
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 3000, Service: "svc-a", Kind: "call_ref", Count: 30},
		{RunAt: 2000, Service: "svc-a", Kind: "call_ref", Count: 20},
		{RunAt: 1000, Service: "svc-a", Kind: "call_ref", Count: 10},
		{RunAt: 2000, Service: "svc-b", Kind: "call_ref", Count: 5},
		{RunAt: 1000, Service: "svc-b", Kind: "call_ref", Count: 5},
	}
	flagged := graph.DetectGrowth(history, 3)
	if len(flagged) != 1 || flagged[0] != "svc-a" {
		t.Errorf("expected [svc-a] flagged, got %v", flagged)
	}
}

func TestDetectGrowth_InsufficientRuns(t *testing.T) {
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 2000, Service: "svc", Kind: "call_ref", Count: 10},
		{RunAt: 1000, Service: "svc", Kind: "call_ref", Count: 5},
	}
	// Only 2 runs, need 3 → not flagged.
	flagged := graph.DetectGrowth(history, 3)
	if len(flagged) != 0 {
		t.Errorf("want empty (insufficient runs), got %v", flagged)
	}
}

func TestDetectGrowth_Empty(t *testing.T) {
	if flagged := graph.DetectGrowth(nil, 3); len(flagged) != 0 {
		t.Errorf("want empty for nil history")
	}
}

func TestDetectGrowth_NotStrictlyIncreasing(t *testing.T) {
	// 30, 30, 10 — second run same as first: not strictly increasing.
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 3000, Service: "svc", Kind: "call_ref", Count: 30},
		{RunAt: 2000, Service: "svc", Kind: "call_ref", Count: 30},
		{RunAt: 1000, Service: "svc", Kind: "call_ref", Count: 10},
	}
	flagged := graph.DetectGrowth(history, 3)
	if len(flagged) != 0 {
		t.Errorf("want not flagged (not strictly increasing), got %v", flagged)
	}
}

// ── Determinism tests (bug-class rule 2) ─────────────────────────────────────

func TestComputeTrend_Determinism(t *testing.T) {
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 3000, Service: "svc-b", Kind: "import_ref", Count: 7},
		{RunAt: 3000, Service: "svc-a", Kind: "call_ref", Count: 15},
		{RunAt: 2000, Service: "svc-b", Kind: "import_ref", Count: 4},
		{RunAt: 2000, Service: "svc-a", Kind: "call_ref", Count: 10},
		{RunAt: 1000, Service: "svc-b", Kind: "import_ref", Count: 2},
		{RunAt: 1000, Service: "svc-a", Kind: "call_ref", Count: 8},
	}
	run1 := graph.ComputeTrend(history, 3)
	run2 := graph.ComputeTrend(history, 3)
	j1, _ := json.Marshal(run1)
	j2, _ := json.Marshal(run2)
	if string(j1) != string(j2) {
		t.Errorf("ComputeTrend not deterministic:\nrun1: %s\nrun2: %s", j1, j2)
	}
}

func TestDetectGrowth_Determinism(t *testing.T) {
	history := []graph.UnresolvedHistoryRow{
		{RunAt: 3000, Service: "svc-b", Kind: "call_ref", Count: 30},
		{RunAt: 3000, Service: "svc-a", Kind: "call_ref", Count: 30},
		{RunAt: 2000, Service: "svc-b", Kind: "call_ref", Count: 20},
		{RunAt: 2000, Service: "svc-a", Kind: "call_ref", Count: 20},
		{RunAt: 1000, Service: "svc-b", Kind: "call_ref", Count: 10},
		{RunAt: 1000, Service: "svc-a", Kind: "call_ref", Count: 10},
	}
	run1 := graph.DetectGrowth(history, 3)
	run2 := graph.DetectGrowth(history, 3)
	j1, _ := json.Marshal(run1)
	j2, _ := json.Marshal(run2)
	if string(j1) != string(j2) {
		t.Errorf("DetectGrowth not deterministic:\nrun1: %s\nrun2: %s", j1, j2)
	}
}

func TestListUnresolvedHistory_Determinism(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "*.db")
	f.Close()
	store, _ := graph.NewSQLiteStore(f.Name())
	defer store.Close()
	ctx := context.Background()

	_ = store.WriteUnresolvedHistory(ctx, []graph.UnresolvedHistoryRow{
		{RunAt: 2000, Service: "svc-b", Kind: "call_ref", Count: 3},
		{RunAt: 2000, Service: "svc-a", Kind: "import_ref", Count: 5},
		{RunAt: 1000, Service: "svc-b", Kind: "call_ref", Count: 2},
		{RunAt: 1000, Service: "svc-a", Kind: "import_ref", Count: 4},
	})

	got1, _ := store.ListUnresolvedHistory(ctx, 10)
	got2, _ := store.ListUnresolvedHistory(ctx, 10)
	j1, _ := json.Marshal(got1)
	j2, _ := json.Marshal(got2)
	if string(j1) != string(j2) {
		t.Errorf("ListUnresolvedHistory not deterministic:\nrun1: %s\nrun2: %s", j1, j2)
	}
}
