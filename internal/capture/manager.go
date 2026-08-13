// Package capture is the shared capture-session lifecycle API used by both
// the CLI (`polyflow capture start|stop|run`, `ingest`, `flows`) and the
// server's /api/capture and /api/runtime endpoints (UB.7). Both surfaces
// read and write the same on-disk session store
// (<workspace>/.polyflow/captures/<session>/{meta.json,spans.otlp.json,pid}),
// so a session started by one surface is visible and stoppable from the
// other. A Manager additionally tracks sessions it started itself
// in-process (the API server keeps one Manager alive for its whole
// lifetime) so it can stop them directly instead of relying on the
// PID/SIGTERM fallback CLI-started sessions require.
package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
	"github.com/lordsonvimal/polyflow/internal/meta"
)

// BaseDir returns the directory that holds all capture sessions, relative
// to the process's working directory — the same assumption every other
// polyflow entry point (CLI and server) makes about the workspace root.
func BaseDir() string {
	return filepath.Join(meta.DBDir, "captures")
}

// DefaultSessionName returns the effective session name: the supplied flag
// value, or a UTC timestamp when empty.
func DefaultSessionName(flag string) string {
	if flag != "" {
		return flag
	}
	return time.Now().UTC().Format("2006-01-02T15-04-05")
}

// ErrPortInUse is returned by Start when the OTLP HTTP or gRPC listener
// could not bind because the port is already in use by another process.
type ErrPortInUse struct {
	Port      int
	Transport string // "HTTP" | "gRPC"
}

func (e ErrPortInUse) Error() string {
	return fmt.Sprintf("%s port %d is already in use", e.Transport, e.Port)
}

var bindErrRe = regexp.MustCompile(`bind (HTTP|gRPC) :(\d+):`)

// classifyStartErr recognises the "address already in use" case out of
// Receiver.Start's wrapped bind errors and turns it into ErrPortInUse so
// callers (the API handler) can map it to 409 without string-matching.
func classifyStartErr(err error) error {
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "address already in use") {
		return err
	}
	m := bindErrRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	port, convErr := strconv.Atoi(m[2])
	if convErr != nil {
		return err
	}
	return ErrPortInUse{Port: port, Transport: m[1]}
}

// activeSession is a session this Manager started and still owns in-process.
type activeSession struct {
	session   *trace_ingest.Session
	receiver  *trace_ingest.Receiver
	startedAt time.Time
}

// Manager owns the in-process registry of active (API- or long-running
// CLI-process-started) sessions, layered over the on-disk session store in
// baseDir. A Manager is safe for concurrent use.
type Manager struct {
	baseDir string

	mu     sync.Mutex
	active map[string]*activeSession
}

// NewManager builds a Manager rooted at baseDir (typically capture.BaseDir()).
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir, active: make(map[string]*activeSession)}
}

// ActiveStatus is one entry in Status's "active" list.
type ActiveStatus struct {
	Session       string    `json:"session"`
	Since         time.Time `json:"since"`
	SpansReceived int       `json:"spans_received"`
	HTTPPort      int       `json:"http_port"`
	GRPCPort      int       `json:"grpc_port"`
}

// StatusResponse is the shape of GET /api/capture/status.
type StatusResponse struct {
	Active   []ActiveStatus             `json:"active"`
	Sessions []trace_ingest.SessionInfo `json:"sessions"`
}

// Handle is what Start returns: everything a caller needs to report the
// session as started and, for the CLI, to keep the process alive.
type Handle struct {
	Session  *trace_ingest.Session
	Receiver *trace_ingest.Receiver
}

// Start creates a new session directory and an embedded OTLP receiver bound
// to httpPort/grpcPort, and registers it in this Manager's in-process
// registry so Stop/Status can find it without touching the pidfile. mode is
// "partial" (capture start) or "full" (capture run) per the on-disk schema.
//
// Returns ErrPortInUse (wrapped) when either port is already bound.
func (m *Manager) Start(name, mode string, httpPort, grpcPort int) (*Handle, error) {
	sess, err := trace_ingest.NewSession(m.baseDir, name, mode)
	if err != nil {
		return nil, err
	}

	recv := trace_ingest.NewReceiver(sess, httpPort, grpcPort)
	if err := recv.Start(); err != nil {
		return nil, classifyStartErr(err)
	}

	if err := sess.WritePID(); err != nil {
		recv.Stop()
		<-recv.Done()
		return nil, fmt.Errorf("capture: write pid: %w", err)
	}

	m.mu.Lock()
	m.active[name] = &activeSession{session: sess, receiver: recv, startedAt: time.Now().UTC()}
	m.mu.Unlock()

	return &Handle{Session: sess, Receiver: recv}, nil
}

// StopResult reports how a session was stopped.
type StopResult struct {
	// Finalized is true when this Manager owned the session in-process and
	// has already finalised it (meta.json is final). It is false when the
	// session was owned by another process — a SIGTERM was sent, and that
	// process finalises the session asynchronously.
	Finalized bool
	PID       int // the signalled PID, set only when Finalized is false
}

// Stop stops the named session. If this Manager started it (in-process),
// the receiver is shut down and the session finalised synchronously with
// wrappedCommand recorded. Otherwise it falls back to reading the on-disk
// pidfile and sending SIGTERM — the same mechanism `polyflow capture stop`
// uses against a session another process (CLI or server) started, so a
// capture started via one surface is always stoppable from the other.
func (m *Manager) Stop(name, wrappedCommand string) (StopResult, error) {
	m.mu.Lock()
	as, ok := m.active[name]
	if ok {
		delete(m.active, name)
	}
	m.mu.Unlock()

	if ok {
		as.receiver.Stop()
		<-as.receiver.Done()
		as.session.RemovePID()
		if err := as.session.Finalize(wrappedCommand); err != nil {
			return StopResult{}, fmt.Errorf("capture: finalize: %w", err)
		}
		return StopResult{Finalized: true}, nil
	}

	sessDir := filepath.Join(m.baseDir, name)
	pid, err := trace_ingest.ReadSessionPID(sessDir)
	if err != nil {
		return StopResult{}, fmt.Errorf("capture stop: read pidfile for session %q: %w", name, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return StopResult{}, fmt.Errorf("capture stop: find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return StopResult{}, fmt.Errorf("capture stop: signal process %d: %w", pid, err)
	}
	return StopResult{Finalized: false, PID: pid}, nil
}

// SpanCount returns the live span counter for an in-process active session
// and whether it is currently active in this Manager. Used to throttle
// capture_progress SSE pushes without re-reading meta.json.
func (m *Manager) SpanCount(name string) (int, bool) {
	m.mu.Lock()
	as, ok := m.active[name]
	m.mu.Unlock()
	if !ok {
		return 0, false
	}
	return as.session.SpanCount(), true
}

// Status returns the in-process active sessions plus every on-disk session
// (active or finalised) — the shape GET /api/capture/status returns.
func (m *Manager) Status() StatusResponse {
	m.mu.Lock()
	active := make([]ActiveStatus, 0, len(m.active))
	for name, as := range m.active {
		active = append(active, ActiveStatus{
			Session:       name,
			Since:         as.startedAt,
			SpansReceived: as.session.SpanCount(),
			HTTPPort:      as.receiver.HTTPPort(),
			GRPCPort:      as.receiver.GRPCPort(),
		})
	}
	m.mu.Unlock()

	return StatusResponse{
		Active:   active,
		Sessions: trace_ingest.ListSessionInfos(m.baseDir, time.Now()),
	}
}

// Ingest imports a pre-captured OTLP dump (raw file bytes, JSON or JSONL)
// into a named session and finalises it immediately (mode "ingest"). It
// routes through the same Session/Finalize machinery `capture start` uses
// instead of hand-rolling meta.json, so both paths agree on the on-disk
// schema.
func (m *Manager) Ingest(name string, data []byte) (spanCount int, err error) {
	// Validate before touching disk, matching the pre-extraction CLI
	// behaviour: a syntactically malformed dump fails loudly instead of
	// silently creating an empty session (Session.Append tolerates parse
	// errors so mid-stream OTLP posts never abort a live receiver).
	spans, err := trace_ingest.ParseOTLPBytes(data)
	if err != nil {
		return 0, fmt.Errorf("ingest: parse: %w", err)
	}

	sess, err := trace_ingest.NewSession(m.baseDir, name, "ingest")
	if err != nil {
		return 0, fmt.Errorf("ingest: %w", err)
	}
	if err := sess.Append(data); err != nil {
		return 0, fmt.Errorf("ingest: append: %w", err)
	}
	if err := sess.Finalize(""); err != nil {
		return 0, fmt.Errorf("ingest: finalize: %w", err)
	}
	return len(spans), nil
}

// Spans reads every span recorded for a session off disk (works for both
// active and finalised sessions).
func (m *Manager) Spans(name string) ([]trace_ingest.Span, error) {
	spansFile := filepath.Join(m.baseDir, name, "spans.otlp.json")
	return trace_ingest.ReadSessionSpans(spansFile)
}

// Flows maps a session's recorded spans to flow records + the ingest ledger
// (unmapped spans with reasons) — the exact pair GET /api/runtime/flows
// returns, and what `polyflow flows --session` prints.
func (m *Manager) Flows(name string) ([]trace_ingest.Span, []trace_ingest.FlowRecord, []trace_ingest.IngestLedgerEntry, error) {
	spans, err := m.Spans(name)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("flows: read session %q: %w", name, err)
	}
	flows, ledger := trace_ingest.MapSpans(spans, name, nil)
	return spans, flows, ledger, nil
}
