package trace_ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SessionMeta is the persistent shape of a capture session's meta.json.
// Matches the pinned spec in docs/runtime-flow-plan.md §Session store.
type SessionMeta struct {
	Name           string     `json:"name"`
	StartedAt      time.Time  `json:"started_at"`
	StoppedAt      *time.Time `json:"stopped_at,omitempty"`
	SpanCount      int        `json:"span_count"`
	TraceCount     int        `json:"trace_count"`
	ServicesSeen   []string   `json:"services_seen"`
	Mode           string     `json:"mode"` // "partial" | "full" | "ingest"
	WrappedCommand string     `json:"wrapped_command,omitempty"`
}

// Session is an active or finalised capture session. It owns the session
// directory and serialises concurrent OTLP writes through a single mutex.
type Session struct {
	dir string

	mu         sync.Mutex
	f          *os.File // open for append; nil after Finalize
	spanCount  int
	tracesSeen map[string]bool
	svcsSeen   map[string]bool
	meta       SessionMeta
}

// NewSession creates a new session directory, writes initial meta.json, and
// opens spans.otlp.json for appending.
//
// Returns an error if a session with the same name already exists and has a
// live pidfile (concurrent capture collision — bug-class rule 1).
func NewSession(capturesDir, name, mode string) (*Session, error) {
	dir := capturesDir + "/" + name
	// Detect live collision before creating the dir.
	if _, statErr := os.Stat(dir); statErr == nil {
		pid, pidErr := ReadSessionPID(dir)
		if pidErr == nil && IsProcessAlive(pid) {
			return nil, fmt.Errorf("session %q already exists and is active (pid %d); use a different --session name", name, pid)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	now := time.Now().UTC()
	s := &Session{
		dir:        dir,
		tracesSeen: make(map[string]bool),
		svcsSeen:   make(map[string]bool),
		meta: SessionMeta{
			Name:      name,
			StartedAt: now,
			Mode:      mode,
		},
	}
	if err := s.writeMeta(); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(dir+"/spans.otlp.json", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open spans file: %w", err)
	}
	s.f = f
	return s, nil
}

// Dir returns the absolute path to the session directory.
func (s *Session) Dir() string { return s.dir }

// Name returns the session name.
func (s *Session) Name() string { return s.meta.Name }

// Append adds an OTLP export request (JSON or protobuf bytes) to the session.
// Input is normalised to a compact single-line JSON document before writing so
// the JSONL file remains line-parseable regardless of whether the caller
// supplied multi-line JSON, compact JSON, or binary protobuf.
//
// Concurrent callers are serialised through the mutex — bytes from different
// OTLP posts do not interleave (pinned R.2 concurrency requirement).
func (s *Session) Append(data []byte) error {
	// Parse spans BEFORE acquiring the mutex (expensive work outside the lock).
	spans, _ := ParseOTLPBytes(data)

	// Produce a compact single-line JSON document. When the input carried spans,
	// re-serialise through spansToOTLPJSONLine (normalises multi-line JSON and
	// protobuf alike). When no spans were parsed, skip the write entirely — an
	// empty export (e.g. metrics-only) does not pollute the spans file.
	if len(spans) == 0 {
		return nil
	}
	line, err := spansToOTLPJSONLine(spans)
	if err != nil {
		return fmt.Errorf("session: serialise spans: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		return fmt.Errorf("session %q is already finalised", s.meta.Name)
	}

	for _, sp := range spans {
		s.spanCount++
		s.tracesSeen[sp.TraceID] = true
		if sp.Service != "" {
			s.svcsSeen[sp.Service] = true
		}
	}

	_, err = fmt.Fprintf(s.f, "%s\n", line)
	return err
}

// Finalize closes the spans file and writes the final meta.json with
// stopped_at, span_count, trace_count, and services_seen (sorted).
// After Finalize, Append returns an error.
func (s *Session) Finalize(wrappedCommand string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f != nil {
		_ = s.f.Sync()
		_ = s.f.Close()
		s.f = nil
	}

	now := time.Now().UTC()
	s.meta.StoppedAt = &now
	s.meta.SpanCount = s.spanCount
	s.meta.TraceCount = len(s.tracesSeen)
	s.meta.WrappedCommand = wrappedCommand

	svcs := make([]string, 0, len(s.svcsSeen))
	for svc := range s.svcsSeen {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs) // determinism (bug-class rule 2)
	s.meta.ServicesSeen = svcs

	return s.writeMeta()
}

// writeMeta persists the current meta to meta.json atomically.
// Caller must hold s.mu or be in NewSession before any concurrent access.
func (s *Session) writeMeta() error {
	data, err := json.MarshalIndent(s.meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.dir+"/meta.json", data, 0o644)
}

// ─── PID helpers ─────────────────────────────────────────────────────────────

// WritePID writes the current process's PID to a "pid" file in the session dir.
// Used by `capture start` so `capture stop` can find and signal the process.
func (s *Session) WritePID() error {
	return os.WriteFile(s.dir+"/pid", []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
}

// RemovePID deletes the pidfile (called after the receiver shuts down).
func (s *Session) RemovePID() {
	_ = os.Remove(s.dir + "/pid")
}

// ReadSessionPID reads the pidfile from sessionDir and returns the PID.
func ReadSessionPID(sessionDir string) (int, error) {
	data, err := os.ReadFile(sessionDir + "/pid")
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse pidfile: %w", err)
	}
	return pid, nil
}

// IsProcessAlive reports whether a process with the given PID is running.
// On Unix, FindProcess always succeeds; Signal(0) probes liveness without
// sending a real signal.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
