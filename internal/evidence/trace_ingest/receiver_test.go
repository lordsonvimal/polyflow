package trace_ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTestSession creates a temporary session in its own t.TempDir() subdir.
func newTestSession(t *testing.T, name, mode string) *Session {
	t.Helper()
	sess, err := NewSession(t.TempDir(), name, mode)
	require.NoError(t, err, "NewSession")
	return sess
}

// readBackSpans reads all spans from the session's JSONL file.
func readBackSpans(t *testing.T, sess *Session) []Span {
	t.Helper()
	spans, err := ReadSessionSpans(sess.Dir() + "/spans.otlp.json")
	require.NoError(t, err, "ReadSessionSpans")
	return spans
}

// buildExportRequest builds a minimal single-span ExportTraceServiceRequest.
func buildExportRequest(svc, traceID, spanID string) *collectortrace.ExportTraceServiceRequest {
	kv := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{
			Value: &commonv1.AnyValue_StringValue{StringValue: v},
		}}
	}
	tid := hexBytes(traceID)
	sid := hexBytes(spanID)
	return &collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracev1.ResourceSpans{{
			Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{kv("service.name", svc)}},
			ScopeSpans: []*tracev1.ScopeSpans{{
				Spans: []*tracev1.Span{{
					TraceId:           tid,
					SpanId:            sid,
					Name:              "GET /test",
					Kind:              tracev1.Span_SPAN_KIND_SERVER,
					StartTimeUnixNano: 1752480000000000000,
					EndTimeUnixNano:   1752480000120000000,
					Attributes: []*commonv1.KeyValue{
						kv("http.request.method", "GET"),
						kv("http.route", "/test"),
					},
				}},
			}},
		}},
	}
}

func hexBytes(h string) []byte {
	b := make([]byte, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		var v byte
		fmt.Sscanf(h[i:i+2], "%02x", &v)
		b[i/2] = v
	}
	return b
}

// startTestReceiver starts a receiver on OS-assigned ports; registers Stop as
// a t.Cleanup so tests don't have to call it manually. Stop is idempotent.
func startTestReceiver(t *testing.T, sess *Session) *Receiver {
	t.Helper()
	recv := NewReceiver(sess, 0, 0)
	require.NoError(t, recv.Start(), "receiver.Start")
	t.Cleanup(func() { recv.Stop() })
	return recv
}

// postProto sends a single protobuf export to the receiver's HTTP endpoint.
func postProto(t *testing.T, recv *Receiver, req *collectortrace.ExportTraceServiceRequest) *http.Response {
	t.Helper()
	body, err := proto.Marshal(req)
	require.NoError(t, err)
	url := fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort())
	resp, err := http.Post(url, "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	return resp
}

// ─── TestReceiverHTTPProtobufRoundTrip ───────────────────────────────────────

// Verifies that a protobuf POST to /v1/traces is written to the session file.
// Drives through real OTLP bytes (bug-class rule 6).
func TestReceiverHTTPProtobufRoundTrip(t *testing.T) {
	sess := newTestSession(t, "http-proto", "partial")
	recv := startTestReceiver(t, sess)

	resp := postProto(t, recv, buildExportRequest("api", "5b8efff798038103d269b633813fc60c", "aaa19b7ec3c1b174"))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	recv.Stop()
	require.NoError(t, sess.Finalize(""))

	spans := readBackSpans(t, sess)
	require.Len(t, spans, 1, "one span exported → one span recorded")
	assert.Equal(t, "api", spans[0].Service)
	assert.Equal(t, "5b8efff798038103d269b633813fc60c", spans[0].TraceID)
}

// ─── TestReceiverHTTPJSONRoundTrip ───────────────────────────────────────────

// Verifies that an OTLP JSON body is accepted and parsed correctly.
func TestReceiverHTTPJSONRoundTrip(t *testing.T) {
	sess := newTestSession(t, "http-json", "partial")
	recv := startTestReceiver(t, sess)

	body, err := os.ReadFile(testFixturesDir + "/http_2svc.otlp.json")
	require.NoError(t, err)
	url := fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort())
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	recv.Stop()
	require.NoError(t, sess.Finalize(""))

	spans := readBackSpans(t, sess)
	assert.Len(t, spans, 2, "2-service fixture has 2 spans")
}

// ─── TestReceiverCORS ────────────────────────────────────────────────────────

// Verifies CORS preflight returns the correct headers for browser SDK access.
func TestReceiverCORS(t *testing.T) {
	sess := newTestSession(t, "cors", "partial")
	recv := startTestReceiver(t, sess)

	req, err := http.NewRequest(http.MethodOptions,
		fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort()), nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
}

// ─── TestReceiverHTTPUnsupportedContentType (negative) ───────────────────────

func TestReceiverHTTPUnsupportedContentType(t *testing.T) {
	sess := newTestSession(t, "ct-neg", "partial")
	recv := startTestReceiver(t, sess)

	url := fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort())
	resp, err := http.Post(url, "text/plain", bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)
}

// ─── TestReceiverGRPCRoundTrip ────────────────────────────────────────────────

// Verifies that a gRPC Export call is recorded in the session file.
func TestReceiverGRPCRoundTrip(t *testing.T) {
	sess := newTestSession(t, "grpc-rt", "partial")
	recv := startTestReceiver(t, sess)

	conn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", recv.GRPCPort()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	_, err = collectortrace.NewTraceServiceClient(conn).Export(
		context.Background(),
		buildExportRequest("api", "5b8efff798038103d269b633813fc60c", "aaa19b7ec3c1b174"),
	)
	require.NoError(t, err, "gRPC Export must succeed")

	recv.Stop()
	require.NoError(t, sess.Finalize(""))

	spans := readBackSpans(t, sess)
	require.Len(t, spans, 1)
	assert.Equal(t, "api", spans[0].Service)
}

// ─── TestReceiverBothTransportsSameSession ────────────────────────────────────

// Fan-out test: spans exported over HTTP and gRPC in the same session both
// appear in the final session file. (Bug-class rule 1: fan-out, never first-match.)
func TestReceiverBothTransportsSameSession(t *testing.T) {
	sess := newTestSession(t, "both", "partial")
	recv := startTestReceiver(t, sess)

	// HTTP export for "web".
	resp := postProto(t, recv, buildExportRequest("web", "aaaa0000bbbb0000aaaa0000bbbb0000", "1111000022220000"))
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// gRPC export for "api".
	conn, err := grpc.NewClient(
		fmt.Sprintf("localhost:%d", recv.GRPCPort()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	_, err = collectortrace.NewTraceServiceClient(conn).Export(
		context.Background(),
		buildExportRequest("api", "cccc0000dddd0000cccc0000dddd0000", "3333000044440000"),
	)
	require.NoError(t, err)

	recv.Stop()
	require.NoError(t, sess.Finalize(""))

	spans := readBackSpans(t, sess)
	require.Len(t, spans, 2, "one span from HTTP + one from gRPC must both be present")

	svcs := map[string]bool{}
	for _, sp := range spans {
		svcs[sp.Service] = true
	}
	assert.True(t, svcs["web"], "HTTP span (web) must be present")
	assert.True(t, svcs["api"], "gRPC span (api) must be present")
}

// ─── TestPartialWindow ────────────────────────────────────────────────────────

// Verifies that spans sent AFTER Stop are NOT recorded.
// This is the partial-capture contract: only what arrived during the window counts.
func TestPartialWindow(t *testing.T) {
	sess := newTestSession(t, "partial-win", "partial")
	recv := startTestReceiver(t, sess)

	// Send one span inside the window.
	resp := postProto(t, recv, buildExportRequest("svc1", "aaaabbbbccccddddaaaabbbbccccdddd", "1111222233334444"))
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	recv.Stop()
	<-recv.Done()
	require.NoError(t, sess.Finalize(""))

	// Post after stop must fail at the transport level (connection refused).
	body, err := proto.Marshal(buildExportRequest("svc2", "eeee0000ffff0000eeee0000ffff0000", "5555666677778888"))
	require.NoError(t, err)
	_, postErr := http.Post(
		fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort()),
		"application/x-protobuf", bytes.NewReader(body),
	)
	assert.Error(t, postErr, "posting after stop must fail (connection refused)")

	spans := readBackSpans(t, sess)
	require.Len(t, spans, 1, "only the pre-stop span must be in the session file")
	assert.Equal(t, "svc1", spans[0].Service)
}

// ─── TestSessionCollisionError ────────────────────────────────────────────────

// Creating a second session with the same name while a live pidfile is present
// must return an error. (Concurrent capture collision guard.)
func TestSessionCollisionError(t *testing.T) {
	dir := t.TempDir()
	name := "same-name"

	sess1, err := NewSession(dir, name, "partial")
	require.NoError(t, err)
	require.NoError(t, sess1.WritePID()) // fake-live with current PID

	_, err = NewSession(dir, name, "partial")
	require.Error(t, err, "second NewSession with live pidfile must fail")
	assert.Contains(t, err.Error(), "already exists and is active")

	// After removing the pidfile the same name is reusable.
	sess1.RemovePID()
	sess2, err := NewSession(dir, name, "partial")
	require.NoError(t, err, "NewSession must succeed after pidfile is removed")
	require.NoError(t, sess2.Finalize(""))
}

// ─── TestConcurrentWrites ─────────────────────────────────────────────────────

// Concurrent Append calls must not interleave bytes: every JSONL line must be
// independently valid JSON. (The pinned R.2 concurrency requirement.)
func TestConcurrentWrites(t *testing.T) {
	sess := newTestSession(t, "concurrent", "partial")

	body, err := os.ReadFile(testFixturesDir + "/http_2svc.otlp.json")
	require.NoError(t, err)

	const goroutines = 20
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() { errs <- sess.Append(body) }()
	}
	for i := 0; i < goroutines; i++ {
		require.NoError(t, <-errs)
	}
	require.NoError(t, sess.Finalize(""))

	data, err := os.ReadFile(sess.Dir() + "/spans.otlp.json")
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	assert.Len(t, lines, goroutines, "each Append must produce exactly one JSONL line")
	for i, line := range lines {
		var obj map[string]interface{}
		assert.NoError(t, json.Unmarshal(line, &obj), "line %d must be valid JSON: %s", i, string(line))
	}
}

// ─── TestSessionMeta ─────────────────────────────────────────────────────────

// Finalize must write a well-formed meta.json with correct counts and
// sorted services_seen (bug-class rule 2).
func TestSessionMeta(t *testing.T) {
	sess := newTestSession(t, "meta-test", "full")

	body, err := os.ReadFile(testFixturesDir + "/http_2svc.otlp.json")
	require.NoError(t, err)
	require.NoError(t, sess.Append(body))
	require.NoError(t, sess.Finalize("go test ./..."))

	data, err := os.ReadFile(sess.Dir() + "/meta.json")
	require.NoError(t, err)

	var m SessionMeta
	require.NoError(t, json.Unmarshal(data, &m))

	assert.Equal(t, "meta-test", m.Name)
	assert.Equal(t, "full", m.Mode)
	assert.Equal(t, "go test ./...", m.WrappedCommand)
	assert.Equal(t, 2, m.SpanCount)
	assert.Equal(t, 1, m.TraceCount)
	require.NotNil(t, m.StoppedAt)

	sorted := append([]string{}, m.ServicesSeen...)
	sort.Strings(sorted)
	assert.Equal(t, sorted, m.ServicesSeen, "services_seen must be sorted (rule 2)")
	assert.Contains(t, m.ServicesSeen, "api")
	assert.Contains(t, m.ServicesSeen, "web")
}

// ─── TestAppendAfterFinalize ──────────────────────────────────────────────────

// Verifies that Append returns an error once the session is finalised.
// Uses real span data (not empty) so the Append path reaches the write check.
func TestAppendAfterFinalize(t *testing.T) {
	sess := newTestSession(t, "post-fin", "partial")
	require.NoError(t, sess.Finalize(""))
	// The canonical 2-service fixture carries real spans, so the finalize
	// check is reached and must return an error.
	body, err := os.ReadFile(testFixturesDir + "/http_2svc.otlp.json")
	require.NoError(t, err)
	err = sess.Append(body)
	assert.Error(t, err, "Append after Finalize must return an error")
}

// ─── TestReceiverDeterminism ──────────────────────────────────────────────────

// Two-run determinism: sending identical spans through separate receiver
// instances produces byte-identical parsed output. (Bug-class rule 2.)
func TestReceiverDeterminism(t *testing.T) {
	run := func(label string) []Span {
		sess := newTestSession(t, label, "partial")
		recv := startTestReceiver(t, sess)

		body, err := os.ReadFile(testFixturesDir + "/http_2svc.otlp.json")
		require.NoError(t, err)
		url := fmt.Sprintf("http://localhost:%d/v1/traces", recv.HTTPPort())
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()

		recv.Stop()
		require.NoError(t, sess.Finalize(""))
		return readBackSpans(t, sess)
	}

	b1, _ := json.Marshal(run("det-1"))
	b2, _ := json.Marshal(run("det-2"))
	assert.Equal(t, string(b1), string(b2), "two-run determinism: byte-identical parsed spans")
}

// ─── TestIsProcessAlive ───────────────────────────────────────────────────────

func TestIsProcessAlive(t *testing.T) {
	assert.True(t, IsProcessAlive(os.Getpid()), "current process must be reported alive")
	assert.False(t, IsProcessAlive(999999999), "obviously-invalid PID must not be alive")
}

// ─── TestSessionPIDLifecycle ──────────────────────────────────────────────────

func TestSessionPIDLifecycle(t *testing.T) {
	sess := newTestSession(t, "pid", "partial")
	require.NoError(t, sess.WritePID())

	pid, err := ReadSessionPID(sess.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)

	sess.RemovePID()
	_, err = ReadSessionPID(sess.Dir())
	assert.Error(t, err, "ReadSessionPID after RemovePID must error")
}

// ─── TestSpansToOTLPJSONLine ──────────────────────────────────────────────────

// Verifies that the protobuf→JSON conversion used by the HTTP receiver
// produces OTLP JSON that round-trips cleanly through ParseOTLPBytes.
func TestSpansToOTLPJSONLine(t *testing.T) {
	spans, err := ParseOTLPFile(testFixturesDir + "/http_2svc.otlp.json")
	require.NoError(t, err)

	line, err := spansToOTLPJSONLine(spans)
	require.NoError(t, err)

	got, err := ParseOTLPBytes(line)
	require.NoError(t, err)
	require.Len(t, got, len(spans))
	for i, sp := range spans {
		assert.Equal(t, sp.TraceID, got[i].TraceID, "span %d TraceID", i)
		assert.Equal(t, sp.SpanID, got[i].SpanID, "span %d SpanID", i)
		assert.Equal(t, sp.Service, got[i].Service, "span %d Service", i)
	}
}
