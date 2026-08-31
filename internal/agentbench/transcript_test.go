package agentbench_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
)

func TestParseTranscript_Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/transcript_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := agentbench.ParseTranscript(data)
	if err != nil {
		t.Fatalf("ParseTranscript: %v", err)
	}
	if tr.DurationMs != 4821 {
		t.Errorf("DurationMs = %d, want 4821", tr.DurationMs)
	}
	if tr.InputTokens != 1523 {
		t.Errorf("InputTokens = %d, want 1523", tr.InputTokens)
	}
	if tr.OutputTokens != 112 {
		t.Errorf("OutputTokens = %d, want 112", tr.OutputTokens)
	}
	if tr.CacheCreationTokens != 2000 {
		t.Errorf("CacheCreationTokens = %d, want 2000", tr.CacheCreationTokens)
	}
	if tr.CacheReadTokens != 15000 {
		t.Errorf("CacheReadTokens = %d, want 15000", tr.CacheReadTokens)
	}
	// ContextTokens is the whole point of the fix: input + cache creation + cache
	// read = the real context the model processed, not the 1523 raw input tokens.
	if tr.ContextTokens != 1523+2000+15000 {
		t.Errorf("ContextTokens = %d, want %d", tr.ContextTokens, 1523+2000+15000)
	}
	if tr.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", tr.NumTurns)
	}
	if tr.TotalCostUSD != 0.00512 {
		t.Errorf("TotalCostUSD = %v, want 0.00512", tr.TotalCostUSD)
	}
	if tr.IsError {
		t.Error("IsError should be false")
	}
	if tr.SessionID != "sess_01ABCdef123" {
		t.Errorf("SessionID = %q", tr.SessionID)
	}
	if tr.Result == "" {
		t.Error("Result should not be empty")
	}
}

func TestParseTranscript_ErrorEnvelope(t *testing.T) {
	data, err := os.ReadFile("testdata/transcript_error.json")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := agentbench.ParseTranscript(data)
	if err != nil {
		t.Fatalf("ParseTranscript: %v", err)
	}
	if !tr.IsError {
		t.Error("IsError should be true")
	}
	if tr.Result != "" {
		t.Error("Result should be empty for error transcript")
	}
}

func TestParseTranscript_InvalidJSON(t *testing.T) {
	_, err := agentbench.ParseTranscript([]byte("{not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseTranscript_WrongType(t *testing.T) {
	data := []byte(`{"type":"assistant","usage":{"input_tokens":1,"output_tokens":1}}`)
	_, err := agentbench.ParseTranscript(data)
	if err == nil {
		t.Error("expected error for non-result type")
	}
}

func TestExtractFiles_FromFixtureResult(t *testing.T) {
	data, _ := os.ReadFile("testdata/transcript_fixture.json")
	tr, _ := agentbench.ParseTranscript(data)

	files := agentbench.ExtractFiles(tr.Result)
	// The fixture result mentions 4 files with path separators.
	want := map[string]bool{
		"internal/impact/impact.go":   true,
		"internal/impact/file.go":     true,
		"internal/trace/trace.go":     true,
		"internal/context/builder.go": true,
	}
	if len(files) != len(want) {
		t.Errorf("ExtractFiles returned %d files, want %d: %v", len(files), len(want), files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file: %q", f)
		}
	}
}

func TestExtractFiles_Determinism(t *testing.T) {
	text := "internal/a/b.go and internal/c/d.go also internal/e/f.go"
	a := agentbench.ExtractFiles(text)
	b := agentbench.ExtractFiles(text)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic: %v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("non-deterministic at [%d]: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestExtractFiles_NoPaths(t *testing.T) {
	files := agentbench.ExtractFiles("No files mentioned here.")
	if len(files) != 0 {
		t.Errorf("expected 0 files, got: %v", files)
	}
}

func TestExtractFiles_Backtick(t *testing.T) {
	text := "See `internal/graph/store.go` for details and also internal/eval/score.go."
	files := agentbench.ExtractFiles(text)
	want := map[string]bool{
		"internal/graph/store.go": true,
		"internal/eval/score.go":  true,
	}
	if len(files) != len(want) {
		t.Errorf("got %v, want %v", files, want)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file: %q", f)
		}
	}
}

func TestSessionTranscriptDir_EscapesSlashes(t *testing.T) {
	dir := agentbench.SessionTranscriptDir("/Users/lordson", "/Users/lordson/Projects/orion-atlas")
	want := "/Users/lordson/.claude/projects/-Users-lordson-Projects-orion-atlas"
	if dir != want {
		t.Errorf("SessionTranscriptDir = %q, want %q", dir, want)
	}
}

// TestSessionAssistantText_ConcatenatesAllTurns is the regression case for
// the orion-atlas recall-0 finding: `claude -p`'s "result" field is only the
// last turn, so a session that answers correctly and then takes one more
// turn to verify a side detail loses the correct earlier answer if scored
// off "result" alone. Reading the session log and concatenating every
// assistant text turn recovers it.
func TestSessionAssistantText_ConcatenatesAllTurns(t *testing.T) {
	home := t.TempDir()
	cwd := "/fake/project"
	sessionDir := agentbench.SessionTranscriptDir(home, cwd)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/session_multiturn.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "session_multiturn"
	if err := os.WriteFile(filepath.Join(sessionDir, sessionID+".jsonl"), fixture, 0644); err != nil {
		t.Fatal(err)
	}

	text, err := agentbench.SessionAssistantText(home, cwd, sessionID)
	if err != nil {
		t.Fatalf("SessionAssistantText: %v", err)
	}

	// The complete first-turn answer must survive even though a narrower
	// follow-up turn came after it.
	for _, want := range []string{
		"app/controllers/api/v1/license_report_jobs_controller.rb",
		"app/models/license_report_job.rb",
		"app/jobs/license_report_creation_job.rb",
		"app/blueprints/license_report_job_blueprint.rb",
		"config/routes.rb",
		"spec/factories/license_report_jobs.rb",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("SessionAssistantText missing %q; got: %s", want, text)
		}
	}

	files := agentbench.ExtractFiles(text)
	if len(files) < 6 {
		t.Errorf("ExtractFiles on full session text returned %d files, want >= 6: %v", len(files), files)
	}
}

func TestSessionAssistantText_MissingSession(t *testing.T) {
	home := t.TempDir()
	_, err := agentbench.SessionAssistantText(home, "/fake/project", "does-not-exist")
	if err == nil {
		t.Error("expected error for missing session file")
	}
}

// TestExtractFiles_NegatedProseSentenceExcluded covers the em-dash-caveat
// shape found live on orion-atlas: the cue and the file mentions share one
// sentence/paragraph, no fence involved.
func TestExtractFiles_NegatedProseSentenceExcluded(t *testing.T) {
	text := "The deeper files (`lib/license_reports/report_service.rb`, " +
		"`lib/license_reports/category_evaluator.rb`) are reached transitively " +
		"through the job, not through `#create` directly — only relevant if " +
		"you're changing what the job receives."
	files := agentbench.ExtractFiles(text)
	if len(files) != 0 {
		t.Errorf("negated files must not be extracted, got: %v", files)
	}
}

// TestExtractFiles_NegatedHeadingExcludesFollowingFence covers the shape
// that actually produced orion-atlas's 0.538 precision: the caveat sits in a
// markdown heading, and the files it caveats are listed in a fenced code
// block directly beneath it (a single "\n", not a blank line, separates
// them) — real text captured from a live bench trial's session log.
func TestExtractFiles_NegatedHeadingExcludesFollowingFence(t *testing.T) {
	text := "Filtering to files reached via `calls`/`job_enqueue` chains — " +
		"these are the ones you'd actually need to edit.\n\n" +
		"**Direct dependencies of `create` (depth 1):**\n" +
		"```\n" +
		"app/controllers/api/v1/license_report_jobs_controller.rb\n" +
		"app/jobs/license_report_creation_job.rb\n" +
		"```\n\n" +
		"**Deeper `calls` chain — only if you change what the job produces:**\n" +
		"```\n" +
		"app/lib/license_reports/report_service.rb\n" +
		"app/lib/license_reports/license_report.rb\n" +
		"app/lib/license_reports/category_evaluator.rb\n" +
		"app/lib/license_reports/predicate_builder.rb\n" +
		"```\n"
	files := agentbench.ExtractFiles(text)
	want := map[string]bool{
		"app/controllers/api/v1/license_report_jobs_controller.rb": true,
		"app/jobs/license_report_creation_job.rb":                  true,
	}
	if len(files) != len(want) {
		t.Errorf("ExtractFiles = %v, want exactly %v (caveated fence must be dropped)", files, want)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file survived negation filtering: %q", f)
		}
	}
}

// TestExtractFiles_UnrelatedBlocksStillExtracted guards against the coarse
// block-level filter over-suppressing: a negation cue in one block must not
// blank out files named in a different (blank-line-separated) block.
func TestExtractFiles_UnrelatedBlocksStillExtracted(t *testing.T) {
	text := "You'll need internal/impact/impact.go.\n\n" +
		"internal/eval/score.go is not relevant here, don't touch it."
	files := agentbench.ExtractFiles(text)
	want := map[string]bool{"internal/impact/impact.go": true}
	if len(files) != len(want) {
		t.Errorf("ExtractFiles = %v, want exactly %v", files, want)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("unexpected file: %q", f)
		}
	}
}

func TestExtractFiles_Deduplication(t *testing.T) {
	text := "internal/a/b.go is important. See also internal/a/b.go."
	files := agentbench.ExtractFiles(text)
	if len(files) != 1 {
		t.Errorf("expected 1 (deduped), got %v", files)
	}
}
