package contract_test

// Golden harness for chessleap cross-service edges.
//
// G.0 creates this infrastructure. G.1 uses it to assert parity after porting
// Linker.Link to the contract engine: run with -run=TestUpdateGoldenChessleap
// to regenerate the snapshot, then assert the new engine produces an identical
// edge set.
//
// The chessleap repo is private; clone it to eval/.cache/chessleap and run
//   make eval-corpus
// before activating these tests. Tests skip automatically when the repo is
// not present.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// chessleapCachePath returns the path to the local chessleap clone used by
// the eval harness, or "" if it cannot be determined.
func chessleapCachePath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// internal/contract/golden_test.go → repo root is 3 levels up
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(root, "eval", ".cache", "chessleap")
}

const goldenDir = "testdata/golden"

// TestUpdateGoldenChessleap regenerates the golden edge snapshot from the
// current (pre-contract-engine) linker. Run this once before G.1 ports
// Linker.Link so the parity assertion has a baseline.
//
// Usage:
//
//	go test ./internal/contract/... -run=TestUpdateGoldenChessleap -v
func TestUpdateGoldenChessleap(t *testing.T) {
	chessleap := chessleapCachePath()
	if _, err := os.Stat(chessleap); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap")
	}
	t.Log("chessleap present; golden snapshot update would run here (G.1 implements)")
	t.Log("Snapshot path:", filepath.Join(goldenDir, "chessleap_http_edges.json"))
}

// TestGoldenChessleapParity asserts that the contract engine produces an
// edge set identical to the golden snapshot captured by TestUpdateGolden.
// This test is a no-op until the golden file exists (created in G.1).
func TestGoldenChessleapParity(t *testing.T) {
	goldenFile := filepath.Join(goldenDir, "chessleap_http_edges.json")
	if _, err := os.Stat(goldenFile); os.IsNotExist(err) {
		t.Skip("golden snapshot not yet generated; run TestUpdateGoldenChessleap first")
	}
	data, err := os.ReadFile(goldenFile)
	require.NoError(t, err)
	var golden []graph.Edge
	require.NoError(t, json.Unmarshal(data, &golden))

	t.Logf("golden snapshot has %d edges; G.1 will assert parity", len(golden))
	t.Skip("parity assertion implemented in G.1")
}
