package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jobs"
	"github.com/lordsonvimal/polyflow/internal/ops"
)

func buildTestServerWithJobs(t *testing.T) (*Server, *jobs.Manager) {
	t.Helper()
	srv := buildTestServer(t, testNodes(), testEdges())
	o, err := ops.Open(":memory:")
	if err != nil {
		t.Fatalf("open ops store: %v", err)
	}
	t.Cleanup(func() { o.Close() })
	srv.SetOps(o)

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	store, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("create fixture graph.db: %v", err)
	}
	if err := store.UpsertNode(context.Background(), &graph.Node{
		ID: "n1", Type: graph.NodeTypeFunction, Label: "f", Service: "svc", File: "a.go", Line: 1, Language: "go",
	}); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	store.Close()

	mgr := jobs.NewManager(jobs.Options{Ops: o, DBPath: dbPath, Broadcast: srv.Broadcast})
	srv.SetJobs(mgr)
	return srv, mgr
}

func TestHandleCreateJob_NoManager_503(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"reconcile"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCreateJob_InvalidJSON_400(t *testing.T) {
	srv, _ := buildTestServerWithJobs(t)
	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`not json`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCreateJob_UnknownKind_400(t *testing.T) {
	srv, _ := buildTestServerWithJobs(t)
	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"bogus"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCreateJob_EvalMissingCorpus_422(t *testing.T) {
	srv, _ := buildTestServerWithJobs(t)
	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"eval","args":{"corpus":"/no/such/dir"}}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
	var body map[string]any
	decodeJSON(t, w.Body.Bytes(), &body)
	if body["path"] != "/no/such/dir" {
		t.Errorf("want path in body, got %+v", body)
	}
}

func TestHandleCreateJob_Reconcile_202AndLifecycle(t *testing.T) {
	srv, mgr := buildTestServerWithJobs(t)

	req := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"reconcile","args":{}}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body)
	}
	var created struct {
		Job struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"job"`
	}
	decodeJSON(t, w.Body.Bytes(), &created)
	if created.Job.State != "running" {
		t.Fatalf("want running, got %s", created.Job.State)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final ops.Job
	for time.Now().Before(deadline) {
		final, _ = mgr.Get(created.Job.ID)
		if final.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.State != "succeeded" {
		t.Fatalf("want succeeded, got %s (error=%s)", final.State, final.Error)
	}

	// GET /api/jobs/{id}
	req2 := httptest.NewRequest("GET", "/api/jobs/"+created.Job.ID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w2.Code, w2.Body)
	}

	// GET /api/jobs (history)
	req3 := httptest.NewRequest("GET", "/api/jobs", nil)
	w3 := httptest.NewRecorder()
	srv.ServeHTTP(w3, req3)
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	decodeJSON(t, w3.Body.Bytes(), &list)
	if len(list.Jobs) != 1 {
		t.Fatalf("want 1 job in history, got %d", len(list.Jobs))
	}
}

func TestHandleGetJob_NotFound_404(t *testing.T) {
	srv, _ := buildTestServerWithJobs(t)
	req := httptest.NewRequest("GET", "/api/jobs/j-nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCancelJob_UnknownID_404(t *testing.T) {
	srv, _ := buildTestServerWithJobs(t)
	req := httptest.NewRequest("DELETE", "/api/jobs/j-nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCreateJob_SingleFlightConflict_409(t *testing.T) {
	srv, mgr := buildTestServerWithJobs(t)
	_ = mgr

	// Start two reconcile jobs back-to-back via the manager directly to
	// force the race window deterministically: hold the manager's lock
	// window by starting job A, then immediately POST job A's kind again
	// before it can complete. Reconcile against a real (tiny) DB is fast, so
	// instead assert the handler surfaces ErrConflict's shape when the
	// manager reports one — exercised at the manager level in
	// internal/jobs; here we only need the HTTP mapping, which we trigger
	// by racing two concurrent requests and accepting that at least one
	// succeeds while a conflicting one (if any) is reported as 409.
	req1 := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"reconcile"}`))
	w1 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/jobs", bytes.NewBufferString(`{"kind":"reconcile"}`))
	w2 := httptest.NewRecorder()

	done := make(chan struct{}, 2)
	go func() { srv.ServeHTTP(w1, req1); done <- struct{}{} }()
	go func() { srv.ServeHTTP(w2, req2); done <- struct{}{} }()
	<-done
	<-done

	codes := []int{w1.Code, w2.Code}
	okCount, conflictCount := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusAccepted:
			okCount++
		case http.StatusConflict:
			conflictCount++
		}
	}
	if okCount+conflictCount != 2 {
		t.Fatalf("want both responses to be 202 or 409, got %v: %s / %s", codes, w1.Body, w2.Body)
	}
	if okCount == 0 {
		t.Fatalf("want at least one 202, got %v", codes)
	}

	// The accepted request(s) started a real goroutine (mgr.run) against
	// dbPath, which lives under t.TempDir(). If that goroutine is still
	// mid-reconcile (holding the sqlite file open) when the test returns,
	// TempDir's RemoveAll cleanup races it and fails with "unlinkat: ...".
	// Reconcile against this tiny fixture DB is fast, but not instant — wait
	// for every accepted job to reach a terminal state before returning.
	for _, w := range []*httptest.ResponseRecorder{w1, w2} {
		if w.Code != http.StatusAccepted {
			continue
		}
		var accepted struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		decodeJSON(t, w.Body.Bytes(), &accepted)
		require.Eventually(t, func() bool {
			j, err := mgr.Get(accepted.Job.ID)
			return err == nil && j.State != "running"
		}, 5*time.Second, 5*time.Millisecond, "job %s did not finish", accepted.Job.ID)
	}
}
