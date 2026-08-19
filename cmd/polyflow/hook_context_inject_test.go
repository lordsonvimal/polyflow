package main

import (
	"reflect"
	"testing"
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
