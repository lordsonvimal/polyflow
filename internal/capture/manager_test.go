package capture

import (
	"bytes"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(t.TempDir())
}

// postSpan posts a minimal OTLP/JSON export with one span to the receiver's
// HTTP endpoint, mirroring a real SDK export.
func postSpan(t *testing.T, port int) {
	t.Helper()
	body := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"web"}}]},"scopeSpans":[{"spans":[{"traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanId":"bbbbbbbbbbbbbbbb","name":"GET /x","kind":2,"startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"http.route","value":{"stringValue":"/x"}},{"key":"http.request.method","value":{"stringValue":"GET"}}]}]}]}]}`
	resp, err := http.Post(
		"http://127.0.0.1:"+strconv.Itoa(port)+"/v1/traces",
		"application/json",
		bytes.NewBufferString(body),
	)
	if err != nil {
		t.Fatalf("post span: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post span: status %d", resp.StatusCode)
	}
}

func TestManager_StartStopLifecycle(t *testing.T) {
	m := newTestManager(t)

	h, err := m.Start("s1", "partial", 0, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if h.Receiver.HTTPPort() == 0 {
		t.Fatalf("expected an assigned HTTP port")
	}

	postSpan(t, h.Receiver.HTTPPort())

	// Give the async Append a moment (HTTP handler appends synchronously
	// before responding, so this should already be reflected, but poll
	// briefly to avoid any flakiness).
	deadline := time.Now().Add(2 * time.Second)
	for {
		n, active := m.SpanCount("s1")
		if !active {
			t.Fatalf("expected session s1 to be active")
		}
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("span count never incremented")
		}
		time.Sleep(10 * time.Millisecond)
	}

	status := m.Status()
	if len(status.Active) != 1 || status.Active[0].Session != "s1" {
		t.Fatalf("expected s1 active, got %+v", status.Active)
	}
	if status.Active[0].SpansReceived < 1 {
		t.Fatalf("expected spans_received >= 1, got %d", status.Active[0].SpansReceived)
	}

	res, err := m.Stop("s1", "")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !res.Finalized {
		t.Fatalf("expected in-process stop to finalize")
	}

	status = m.Status()
	if len(status.Active) != 0 {
		t.Fatalf("expected no active sessions after stop, got %+v", status.Active)
	}
	if len(status.Sessions) != 1 || status.Sessions[0].Name != "s1" {
		t.Fatalf("expected s1 in on-disk sessions, got %+v", status.Sessions)
	}
	if status.Sessions[0].SpanCount < 1 {
		t.Fatalf("expected finalized span_count >= 1, got %d", status.Sessions[0].SpanCount)
	}
}

func TestManager_StartPortConflict(t *testing.T) {
	m := newTestManager(t)

	h, err := m.Start("s1", "partial", 0, 0)
	if err != nil {
		t.Fatalf("start s1: %v", err)
	}
	defer m.Stop("s1", "")

	_, err = m.Start("s2", "partial", h.Receiver.HTTPPort(), 0)
	if err == nil {
		t.Fatalf("expected port conflict error")
	}
	portErr, ok := err.(ErrPortInUse)
	if !ok {
		t.Fatalf("expected ErrPortInUse, got %T: %v", err, err)
	}
	if portErr.Port != h.Receiver.HTTPPort() || portErr.Transport != "HTTP" {
		t.Fatalf("unexpected ErrPortInUse: %+v", portErr)
	}
}

// TestManager_StopFallsBackToPID exercises the cross-process path: a
// session that exists on disk with a pidfile but was NOT started by this
// Manager instance (as if the CLI had started it in a separate process)
// must still be stoppable — Stop falls back to signalling the pidfile's
// PID. We use our own PID so the signal is safe to observe in-test; a
// signal.Notify handler intercepts it before Go's default terminate action.
func TestManager_StopFallsBackToPID(t *testing.T) {
	m := newTestManager(t)

	sess, err := trace_ingest.NewSession(m.baseDir, "cli-started", "partial")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	// Overwrite the pidfile with our own PID (stand-in for "another process").
	if err := os.WriteFile(filepath.Join(m.baseDir, "cli-started", "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	res, err := m.Stop("cli-started", "")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if res.Finalized {
		t.Fatalf("expected fallback (not finalized in-process)")
	}
	if res.PID != os.Getpid() {
		t.Fatalf("expected PID %d, got %d", os.Getpid(), res.PID)
	}

	select {
	case <-sigCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected to receive SIGTERM")
	}

	_ = sess.Finalize("")
}

func TestManager_Ingest(t *testing.T) {
	m := newTestManager(t)

	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "evidence", "runtime", "http_2svc.otlp.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	n, err := m.Ingest("imported", fixture)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if n == 0 {
		t.Fatalf("expected ingested spans > 0")
	}

	status := m.Status()
	if len(status.Sessions) != 1 || status.Sessions[0].Name != "imported" {
		t.Fatalf("expected imported session on disk, got %+v", status.Sessions)
	}
	if status.Sessions[0].SpanCount != n {
		t.Fatalf("meta span_count %d != parsed %d", status.Sessions[0].SpanCount, n)
	}

	spans, flows, ledger, err := m.Flows("imported")
	if err != nil {
		t.Fatalf("flows: %v", err)
	}
	if len(spans) != n {
		t.Fatalf("expected %d spans back from Flows, got %d", n, len(spans))
	}
	_ = flows
	_ = ledger
}
