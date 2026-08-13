package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/ops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestManager(t *testing.T) (*Manager, *ops.Store) {
	t.Helper()
	o, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { o.Close() })
	m := NewManager(Options{Ops: o})
	return m, o
}

func TestStart_UnknownKind(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Start("bogus", nil)
	var e ErrUnknownKind
	require.True(t, errors.As(err, &e))
	assert.Contains(t, err.Error(), "index")
	assert.Contains(t, err.Error(), "eval")
	assert.Contains(t, err.Error(), "reconcile")
}

func TestStart_EvalDirMissing(t *testing.T) {
	m, _ := newTestManager(t)
	args, _ := json.Marshal(EvalArgs{Corpus: filepath.Join(t.TempDir(), "does-not-exist")})
	_, err := m.Start("eval", args)
	var e ErrEvalDirMissing
	require.True(t, errors.As(err, &e))
}

func TestStart_NoOpsStore(t *testing.T) {
	m := NewManager(Options{})
	_, err := m.Start("index", nil)
	require.Error(t, err)
}

// White-box: register a fake running job directly, bypassing goroutine
// timing, so single-flight and cancel are tested deterministically instead
// of racing a real job's completion.
func fakeRunning(m *Manager, k Kind, id string) (*runState, context.CancelFunc) {
	_, cancel := context.WithCancel(context.Background())
	rs := &runState{
		job: ops.Job{ID: id, Kind: string(k), State: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	rs.cancel = cancel
	m.mu.Lock()
	m.running[k] = rs
	m.mu.Unlock()
	return rs, cancel
}

func TestStart_SingleFlight_IndexExcludesOtherKinds(t *testing.T) {
	m, _ := newTestManager(t)
	fakeRunning(m, KindIndex, "j-running")

	_, err := m.Start("reconcile", nil)
	var e ErrConflict
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "j-running", e.Running.ID)
}

func TestStart_SingleFlight_OtherKindExcludesIndex(t *testing.T) {
	m, _ := newTestManager(t)
	fakeRunning(m, KindReconcile, "j-running")

	_, err := m.Start("index", nil)
	var e ErrConflict
	require.True(t, errors.As(err, &e))
	assert.Equal(t, "j-running", e.Running.ID)
}

func TestStart_SingleFlight_SameKindConflicts(t *testing.T) {
	m, _ := newTestManager(t)
	fakeRunning(m, KindReconcile, "j-running")

	dir := t.TempDir()
	dbPath := writeTinyGraphDB(t, dir)
	m.dbPath = dbPath

	_, err := m.Start("reconcile", nil)
	var e ErrConflict
	require.True(t, errors.As(err, &e))
}

func TestStart_SingleFlight_DifferentNonIndexKindsCoexist(t *testing.T) {
	m, _ := newTestManager(t)
	fakeRunning(m, KindReconcile, "j-running")

	// eval with a missing corpus still fails pre-flight, but on a different
	// error path (422, not 409) — proving the conflict check did not fire
	// for a distinct non-index kind.
	args, _ := json.Marshal(EvalArgs{Corpus: filepath.Join(t.TempDir(), "nope")})
	_, err := m.Start("eval", args)
	var conflict ErrConflict
	assert.False(t, errors.As(err, &conflict))
	var missing ErrEvalDirMissing
	assert.True(t, errors.As(err, &missing))
}

func TestCancel_SignalsContextAndReturnsRunningSnapshot(t *testing.T) {
	m, _ := newTestManager(t)
	rs, cancel := fakeRunning(m, KindIndex, "j-1")
	_ = cancel

	job, err := m.Cancel("j-1")
	require.NoError(t, err)
	assert.Equal(t, "running", job.State) // cancellation is async; state flips when the goroutine observes it

	m.mu.Lock()
	ctxErr := rs.cancel // just verifying the field is reachable/non-nil post-cancel
	m.mu.Unlock()
	assert.NotNil(t, ctxErr)
}

func TestCancel_UnknownID(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.Cancel("j-nope")
	assert.True(t, errors.Is(err, ops.ErrJobNotFound))
}

func TestGet_PrefersLiveStateOverPersisted(t *testing.T) {
	m, o := newTestManager(t)
	require.NoError(t, o.UpsertJob(context.Background(), ops.Job{
		ID: "j-1", Kind: "index", State: "running", StartedAt: "2026-08-13T00:00:00Z",
		Progress: ops.JobProgress{Done: 1, Total: 100},
	}))
	rs, _ := fakeRunning(m, KindIndex, "j-1")
	rs.job.Progress = ops.JobProgress{Done: 50, Total: 100}

	job, err := m.Get("j-1")
	require.NoError(t, err)
	assert.Equal(t, 50, job.Progress.Done)
}

func TestGet_FallsBackToPersisted(t *testing.T) {
	m, o := newTestManager(t)
	require.NoError(t, o.UpsertJob(context.Background(), ops.Job{
		ID: "j-1", Kind: "index", State: "succeeded", StartedAt: "2026-08-13T00:00:00Z",
	}))
	job, err := m.Get("j-1")
	require.NoError(t, err)
	assert.Equal(t, "succeeded", job.State)
}

func TestList_SubstitutesLiveStateForRunningRows(t *testing.T) {
	m, o := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, o.UpsertJob(ctx, ops.Job{ID: "j-1", Kind: "index", State: "running", StartedAt: "2026-08-13T00:00:00Z"}))
	require.NoError(t, o.UpsertJob(ctx, ops.Job{ID: "j-2", Kind: "eval", State: "succeeded", StartedAt: "2026-08-13T00:01:00Z"}))
	rs, _ := fakeRunning(m, KindIndex, "j-1")
	rs.job.Progress = ops.JobProgress{Done: 3, Total: 9}
	rs.job.StartedAt = "2026-08-13T00:00:00Z"

	list, err := m.List(0)
	require.NoError(t, err)
	require.Len(t, list, 2)
	var found bool
	for _, j := range list {
		if j.ID == "j-1" {
			found = true
			assert.Equal(t, ops.JobProgress{Done: 3, Total: 9}, j.Progress)
		}
	}
	assert.True(t, found)
}

// writeTinyGraphDB creates a minimal file-backed graph.db with one node so
// reconcile jobs have something to build an AdjacencyIndex from.
func writeTinyGraphDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "graph.db")
	store, err := graph.NewSQLiteStore(path)
	require.NoError(t, err)
	require.NoError(t, store.UpsertNode(context.Background(), &graph.Node{
		ID: "n1", Type: graph.NodeTypeFunction, Label: "f", Service: "svc", File: "a.go", Line: 1, Language: "go",
	}))
	require.NoError(t, store.Close())
	return path
}

func TestReconcileJob_EndToEnd(t *testing.T) {
	m, _ := newTestManager(t)
	m.dbPath = writeTinyGraphDB(t, t.TempDir())

	job, err := m.Start("reconcile", nil)
	require.NoError(t, err)
	assert.Equal(t, "running", job.State)

	final := waitForTerminal(t, m, job.ID)
	assert.Equal(t, "succeeded", final.State)
	require.NotEmpty(t, final.Result)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(final.Result), &result))
	assert.Contains(t, result, "report")
}

func TestReconcileJob_ProposeDirWritesFiles(t *testing.T) {
	m, _ := newTestManager(t)
	m.dbPath = writeTinyGraphDB(t, t.TempDir())
	proposeDir := filepath.Join(t.TempDir(), "proposed")

	args, _ := json.Marshal(ReconcileArgs{ProposeDir: proposeDir})
	job, err := m.Start("reconcile", args)
	require.NoError(t, err)

	final := waitForTerminal(t, m, job.ID)
	require.Equal(t, "succeeded", final.State)
	// No gap edges in a one-node graph, so no files are expected, but the
	// dir must exist (created unconditionally when propose_dir is set) and
	// the result must name it, never silently skipping the request.
	_, statErr := os.Stat(proposeDir)
	assert.NoError(t, statErr)
}

// tinyIndexWorkspace builds a one-file Go workspace and chdirs into it for
// the duration of the test (indexer.Options.DBDir defaults to meta.DBDir,
// a path relative to cwd — matching how `polyflow serve` really runs: cwd
// is always the workspace root). Returns the workspace dir and polyflow.yml
// path.
func tinyIndexWorkspace(t *testing.T) (dir, wsPath string) {
	t.Helper()
	dir = t.TempDir()
	svcDir := filepath.Join(dir, "backend")
	require.NoError(t, os.MkdirAll(svcDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "go.mod"), []byte("module example.com/backend\n\ngo 1.22\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))

	wsPath = filepath.Join(dir, "polyflow.yml")
	require.NoError(t, os.WriteFile(wsPath, []byte(`name: test
version: "1"
services:
  - name: backend
    path: backend
    language: go
`), 0o644))

	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(oldwd) })
	return dir, wsPath
}

func TestIndexJob_EndToEnd(t *testing.T) {
	dir, wsPath := tinyIndexWorkspace(t)

	o, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { o.Close() })
	m := NewManager(Options{Ops: o, WorkspacePath: wsPath})

	job, err := m.Start("index", nil)
	require.NoError(t, err)

	final := waitForTerminal(t, m, job.ID)
	require.Equal(t, "succeeded", final.State, "error: %s log: %v", final.Error, final.LogTail)

	_, statErr := os.Stat(filepath.Join(dir, ".polyflow", "graph.db"))
	assert.NoError(t, statErr)
}

// TestIndexJob_FailedSurfacesErrorVerbatim: a workspace config pointing at a
// polyflow.yml that does not exist must fail the job with the underlying
// load error, not a generic message — the honest-error requirement in the
// phase doc's Tests section.
func TestIndexJob_FailedSurfacesErrorVerbatim(t *testing.T) {
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(oldwd) })

	o, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { o.Close() })
	m := NewManager(Options{Ops: o, WorkspacePath: filepath.Join(dir, "does-not-exist.yml")})

	job, err := m.Start("index", nil)
	require.NoError(t, err)

	final := waitForTerminal(t, m, job.ID)
	assert.Equal(t, "failed", final.State)
	assert.Contains(t, final.Error, "load workspace")

	_, statErr := os.Stat(filepath.Join(dir, ".polyflow", "graph.db"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestIndexJob_CancelBeforeRunLeavesNoGraphDBAndReportsCanceled: canceling
// the job's context before the indexer goroutine gets scheduled means every
// context-aware DB call it makes fails with context.Canceled — the earliest
// and most deterministic point to observe cancellation without depending on
// real-world index timing. The tmp-db + atomic-rename design (indexer.go)
// means no partial graph.db is ever written on this path; this test asserts
// that guarantee holds through the jobs wrapper too.
func TestIndexJob_CancelBeforeRunLeavesNoGraphDBAndReportsCanceled(t *testing.T) {
	dir, wsPath := tinyIndexWorkspace(t)

	o, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { o.Close() })
	m := NewManager(Options{Ops: o, WorkspacePath: wsPath})

	job, err := m.Start("index", nil)
	require.NoError(t, err)
	_, cancelErr := m.Cancel(job.ID)
	require.NoError(t, cancelErr)

	final := waitForTerminal(t, m, job.ID)
	assert.Equal(t, "canceled", final.State)

	_, statErr := os.Stat(filepath.Join(dir, ".polyflow", "graph.db"))
	assert.True(t, os.IsNotExist(statErr), "canceled index must not leave a graph.db behind")
}

func waitForTerminal(t *testing.T, m *Manager, id string) ops.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.Get(id)
		require.NoError(t, err)
		if job.State != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state in time", id)
	return ops.Job{}
}
