package agentbench_test

import (
	"os"
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
		"internal/impact/impact.go":    true,
		"internal/impact/file.go":      true,
		"internal/trace/trace.go":      true,
		"internal/context/builder.go":  true,
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

func TestExtractFiles_Deduplication(t *testing.T) {
	text := "internal/a/b.go is important. See also internal/a/b.go."
	files := agentbench.ExtractFiles(text)
	if len(files) != 1 {
		t.Errorf("expected 1 (deduped), got %v", files)
	}
}
