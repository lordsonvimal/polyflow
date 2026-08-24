package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

func TestExtractTarget_NativeGrepTool(t *testing.T) {
	tests := []struct {
		name        string
		toolInput   map[string]any
		wantMode    string
		wantSymbols []string
	}{
		{
			name:        "bare identifier pattern",
			toolInput:   map[string]any{"pattern": "sendHeartbeat"},
			wantMode:    "symbol",
			wantSymbols: []string{"sendHeartbeat"},
		},
		{
			name:        "alternation pattern",
			toolInput:   map[string]any{"pattern": `heartbeat\|Heartbeat\|RunnerHeartbeat`},
			wantMode:    "symbol",
			wantSymbols: []string{"heartbeat", "Heartbeat", "RunnerHeartbeat"},
		},
		{
			name:      "empty pattern",
			toolInput: map[string]any{"pattern": ""},
			wantMode:  "",
		},
		{
			name:      "missing pattern",
			toolInput: map[string]any{},
			wantMode:  "",
		},
		{
			name:      "non-identifier pattern with no alternation",
			toolInput: map[string]any{"pattern": `^func \(`},
			wantMode:  "",
		},
		{
			name:        "file_path takes precedence over pattern",
			toolInput:   map[string]any{"file_path": "/repo/foo.go", "pattern": "bar"},
			wantMode:    "file",
			wantSymbols: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, _, symbols := extractTarget("Grep", tt.toolInput)
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
			if tt.wantMode == "symbol" && !reflect.DeepEqual(symbols, tt.wantSymbols) {
				t.Fatalf("symbols = %v, want %v", symbols, tt.wantSymbols)
			}
		})
	}
}

// TestExtractTarget_GrepAndBashShareDedupeKey proves the dedupe key —
// derived from symbols via strings.Join(symbols, "|") in
// runHookContextInject — is identical whether a symbol was greped through
// the native Grep tool or `Bash grep`, so a repeated lookup via either path
// only injects context once per session.
func TestExtractTarget_GrepAndBashShareDedupeKey(t *testing.T) {
	_, _, grepSymbols := extractTarget("Grep", map[string]any{"pattern": "sendHeartbeat"})
	_, _, bashSymbols := extractTarget("Bash", map[string]any{"command": "grep -rn sendHeartbeat ."})
	if !reflect.DeepEqual(grepSymbols, bashSymbols) {
		t.Fatalf("Grep symbols %v != Bash-grep symbols %v", grepSymbols, bashSymbols)
	}
}

func TestExtractTarget_BashToolUnaffected(t *testing.T) {
	mode, file, symbols := extractTarget("Bash", map[string]any{"command": "cat internal/graph/model.go"})
	if mode != "file" || file != "internal/graph/model.go" || symbols != nil {
		t.Fatalf("mode=%q file=%q symbols=%v", mode, file, symbols)
	}
}

// runHookForTest feeds payload through a pipe, running runHookContextInject
// synchronously, and returns whatever it wrote to stdout.
func runHookForTest(t *testing.T, payload hookPayload) string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		inW.Write(data)
		inW.Close()
	}()

	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(outR)
		close(done)
	}()

	runHookContextInject(inR, outW)
	outW.Close()
	<-done
	return string(out)
}

// TestRunHookContextInject_DeadlineExceeded proves a slow/blocking DB query
// (stubbed here rather than induced via real lock contention, so the test is
// deterministic) makes the hook exit cleanly within POLYFLOW_HOOK_DEADLINE_MS
// with no stdout, and leaves a breadcrumb — CBM's #858 postmortem: a
// too-tight deadline that silently zeroes output is indistinguishable from
// "no matches" without one.
func TestRunHookContextInject_DeadlineExceeded(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv(hookDeadlineEnv, "10")

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".polyflow", "graph.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := runHookQuery
	t.Cleanup(func() { runHookQuery = orig })
	runHookQuery = func(dbPath, mode, file string, symbols []string, cwd string) string {
		time.Sleep(200 * time.Millisecond)
		return "[polyflow graph — should never be seen]"
	}

	sessionID := fmt.Sprintf("deadline-test-session-%d", time.Now().UnixNano())
	t.Cleanup(func() { os.Remove(filepath.Join(hookSeenDir, sessionID+".json")) })

	out := runHookForTest(t, hookPayload{
		ToolName:  "Grep",
		ToolInput: map[string]any{"pattern": "sendHeartbeat"},
		Cwd:       repoDir,
		SessionID: sessionID,
	})
	if out != "" {
		t.Fatalf("expected no stdout on deadline, got %q", out)
	}

	logPath, err := hookTimeoutLogPath()
	if err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected breadcrumb log at %s: %v", logPath, err)
	}
	if !strings.Contains(string(logData), "deadline exceeded") {
		t.Fatalf("breadcrumb log missing expected content: %q", string(logData))
	}
}

// TestTruncateBlock_UTF8Boundary asserts a multi-byte character straddling
// the hookMaxContextChars boundary is dropped whole, not corrupted into
// invalid UTF-8, per the fix for the raw byte-slice truncation bug.
func TestTruncateBlock_UTF8Boundary(t *testing.T) {
	var b strings.Builder
	for b.Len() < hookMaxContextChars-1 {
		b.WriteByte('a')
	}
	b.WriteRune('世') // 3-byte rune straddling the cut point

	got := truncateBlock(b.String())
	if !utf8.ValidString(got) {
		t.Fatalf("truncateBlock produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}

func TestTruncateBlock_NoTruncationNeeded(t *testing.T) {
	short := "short block"
	if got := truncateBlock(short); got != short {
		t.Fatalf("truncateBlock(%q) = %q, want unchanged", short, got)
	}
}

// newHookTestDB builds a real polyflow graph.db with one function node in
// relFile (so fileContext has something to report) plus any unresolved_refs
// rows the caller supplies.
func newHookTestDB(t *testing.T, relFile string, unresolvedCount int) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(graph.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO nodes (id, type, label, service, file, line, end_line, language) VALUES (?, 'function', 'DoThing', 'svc', ?, 1, 5, 'go')`,
		"n1", relFile); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < unresolvedCount; i++ {
		if _, err := db.Exec(
			`INSERT INTO unresolved_refs (service, file, line, name, kind) VALUES ('svc', ?, ?, 'thing', 'call_ref')`,
			relFile, i+1); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

func TestDefaultHookQuery_UnresolvedRefsNote(t *testing.T) {
	dbPath := newHookTestDB(t, "app/foo.go", 3)
	block := defaultHookQuery(dbPath, "file", "/repo/app/foo.go", nil, "/repo")
	if !strings.Contains(block, "3 unresolved refs in this file") {
		t.Fatalf("expected unresolved-refs note, got %q", block)
	}
}

func TestDefaultHookQuery_CleanFileNoNote(t *testing.T) {
	dbPath := newHookTestDB(t, "app/foo.go", 0)
	block := defaultHookQuery(dbPath, "file", "/repo/app/foo.go", nil, "/repo")
	if strings.Contains(block, "unresolved refs") {
		t.Fatalf("expected no unresolved-refs note on a clean file, got %q", block)
	}
	if block == "" {
		t.Fatalf("expected file context for a file with a declared node")
	}
}

// TestDefaultHookQuery_CrossServiceCallerFromBridge proves GR.3's hook-
// injection wiring: a workspace registered (via internal/registry) as a
// member of a fleet whose bridge.db already contains a cross-service edge
// into a locally-declared symbol gets that edge's far endpoint surfaced as a
// "callers:" entry, sourced entirely from the ATTACHed bridge — no fleet
// sync is triggered (NoSync is always true for hook injection).
func TestDefaultHookQuery_CrossServiceCallerFromBridge(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(repoDir, ".polyflow", "graph.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(graph.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO nodes (id, type, label, service, file, line, end_line, language) VALUES ('n1', 'function', 'DoThing', 'svc', 'app/foo.go', 1, 5, 'go')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)

	regPath := filepath.Join(home, "registry.yml")
	if err := registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: repoDir, Fleets: []string{"myfleet"}}},
	}); err != nil {
		t.Fatal(err)
	}

	bridgePath, err := fleetsync.DefaultBridgePath("myfleet")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bridgePath), 0o755); err != nil {
		t.Fatal(err)
	}
	bdb, err := sql.Open("sqlite", bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bdb.Exec(graph.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := bdb.Exec(
		`INSERT INTO nodes (id, type, label, service, file, line, end_line, language) VALUES ('remote1', 'function', 'RemoteCaller', 'other', 'other/bar.go', 1, 5, 'go')`); err != nil {
		t.Fatal(err)
	}
	// A bridge always carries a stub node for BOTH edge endpoints
	// (internal/indexer.BuildBridge), even though n1's real content lives
	// in the local graph.db above.
	if _, err := bdb.Exec(
		`INSERT INTO nodes (id, type, label, service, file, line, end_line, language) VALUES ('n1', 'function', 'DoThing', 'svc', 'app/foo.go', 1, 5, 'go')`); err != nil {
		t.Fatal(err)
	}
	if _, err := bdb.Exec(
		`INSERT INTO edges (id, "from", "to", type) VALUES ('e1', 'remote1', 'n1', 'http_call')`); err != nil {
		t.Fatal(err)
	}
	bdb.Close()

	block := defaultHookQuery(dbPath, "symbol", "", []string{"DoThing"}, repoDir)
	if !strings.Contains(block, "RemoteCaller") {
		t.Fatalf("expected cross-service caller RemoteCaller from bridge.db, got %q", block)
	}
}

// runHookForTestRaw is runHookForTest's counterpart for feeding a raw JSON
// stdin payload rather than a marshaled hookPayload — used to prove
// hookPayload's json tags actually parse each client's real captured field
// names, not just the Go struct's own round-trip.
func runHookForTestRaw(t *testing.T, rawJSON string) string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		inW.Write([]byte(rawJSON))
		inW.Close()
	}()

	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(outR)
		close(done)
	}()

	runHookContextInject(inR, outW)
	outW.Close()
	<-done
	return string(out)
}

// TestRunHookContextInject_CursorPayloadShape feeds a payload shaped exactly
// like Cursor's real postToolUse hook input (cursor.com/docs/hooks,
// verified at implementation time: tool_name/tool_input/cwd/conversation_id,
// no session_id field at all) and asserts the response includes Cursor's
// documented "additional_context" (snake_case) output key.
func TestRunHookContextInject_CursorPayloadShape(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := newHookTestDB(t, "app/foo.go", 0)
	if err := os.Rename(dbPath, filepath.Join(repoDir, ".polyflow", "graph.db")); err != nil {
		t.Fatal(err)
	}

	conversationID := fmt.Sprintf("cursor-conv-%d", time.Now().UnixNano())
	t.Cleanup(func() { os.Remove(filepath.Join(hookSeenDir, conversationID+".json")) })

	raw := fmt.Sprintf(`{
		"conversation_id": %q,
		"generation_id": "gen-1",
		"model": "test-model",
		"hook_event_name": "postToolUse",
		"cursor_version": "1.7.0",
		"workspace_roots": [%q],
		"user_email": null,
		"transcript_path": null,
		"tool_name": "Read",
		"tool_input": {"file_path": %q},
		"tool_output": "{}",
		"tool_use_id": "tu-1",
		"cwd": %q,
		"duration": 12
	}`, conversationID, repoDir, filepath.Join(repoDir, "app/foo.go"), repoDir)

	out := runHookForTestRaw(t, raw)
	if !strings.Contains(out, `"additional_context"`) {
		t.Fatalf("expected Cursor's additional_context key in output, got %q", out)
	}
	if !strings.Contains(out, `"additionalContext"`) {
		t.Fatalf("expected additionalContext key in output too (Claude compatibility), got %q", out)
	}
}

// TestRunHookContextInject_GeminiCLIPayloadShape feeds a payload shaped
// exactly like Gemini CLI's real AfterTool hook input
// (geminicli.com/docs/hooks/reference, verified at implementation time:
// tool_name/tool_input/tool_response/cwd/session_id) and asserts the
// response nests context under the documented "hookSpecificOutput.
// additionalContext" path.
func TestRunHookContextInject_GeminiCLIPayloadShape(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := newHookTestDB(t, "app/foo.go", 0)
	if err := os.Rename(dbPath, filepath.Join(repoDir, ".polyflow", "graph.db")); err != nil {
		t.Fatal(err)
	}

	sessionID := fmt.Sprintf("gemini-session-%d", time.Now().UnixNano())
	t.Cleanup(func() { os.Remove(filepath.Join(hookSeenDir, sessionID+".json")) })

	raw := fmt.Sprintf(`{
		"session_id": %q,
		"transcript_path": "/tmp/transcript.json",
		"cwd": %q,
		"hook_event_name": "AfterTool",
		"timestamp": "2026-08-21T00:00:00Z",
		"tool_name": "read_file",
		"tool_input": {"file_path": %q},
		"tool_response": {"llmContent": "...", "returnDisplay": "..."},
		"mcp_context": {},
		"original_request_name": "read_file"
	}`, sessionID, repoDir, filepath.Join(repoDir, "app/foo.go"))

	out := runHookForTestRaw(t, raw)
	if !strings.Contains(out, `"hookSpecificOutput"`) {
		t.Fatalf("expected Gemini CLI's hookSpecificOutput wrapper in output, got %q", out)
	}
	if !strings.Contains(out, `"additionalContext"`) {
		t.Fatalf("expected nested additionalContext key in output, got %q", out)
	}
	// Claude Code's own PostToolUse hook schema requires hookEventName
	// alongside additionalContext inside hookSpecificOutput — omitting it
	// fails Claude Code's validation even though additionalContext is
	// present ("hookSpecificOutput is missing required field
	// \"hookEventName\""), so this must hold for every client, not just
	// Gemini CLI's documented shape.
	if !strings.Contains(out, `"hookEventName":"PostToolUse"`) {
		t.Fatalf("expected hookSpecificOutput.hookEventName=PostToolUse in output, got %q", out)
	}
}

func TestGrepPatternSymbols(t *testing.T) {
	tests := []struct {
		pattern string
		want    []string
	}{
		{"sendHeartbeat", []string{"sendHeartbeat"}},
		{`a\|b\|c`, []string{"a", "b", "c"}},
		{"a|b", []string{"a", "b"}},
		{`^func \(`, nil},
		{"", nil},
	}
	for _, tt := range tests {
		got := grepPatternSymbols(tt.pattern)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("grepPatternSymbols(%q) = %v, want %v", tt.pattern, got, tt.want)
		}
	}
}
