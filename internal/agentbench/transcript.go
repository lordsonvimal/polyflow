// Package agentbench provides transcript parsing, scoring, and report generation
// for the P.1 agent outcome benchmark.  Actual claude invocations are performed
// by the bench command (cmd/polyflow/bench.go); this package is the testable core.
package agentbench

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// Transcript is the parsed result of one `claude -p --output-format json` run.
type Transcript struct {
	DurationMs   int64   `json:"duration_ms"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Result       string  `json:"result"`
	IsError      bool    `json:"is_error"`
	SessionID    string  `json:"session_id"`
}

// claudeEnvelope mirrors the `claude -p --output-format json` JSON envelope.
type claudeEnvelope struct {
	Type         string  `json:"type"`
	IsError      bool    `json:"is_error"`
	DurationMs   int64   `json:"duration_ms"`
	Result       string  `json:"result"`
	SessionID    string  `json:"session_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// ParseTranscript decodes the JSON produced by `claude -p --output-format json`.
func ParseTranscript(data []byte) (Transcript, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Transcript{}, fmt.Errorf("parse transcript: %w", err)
	}
	if env.Type != "result" {
		return Transcript{}, fmt.Errorf("unexpected transcript type %q (want \"result\")", env.Type)
	}
	return Transcript{
		DurationMs:   env.DurationMs,
		InputTokens:  env.Usage.InputTokens,
		OutputTokens: env.Usage.OutputTokens,
		TotalCostUSD: env.TotalCostUSD,
		Result:       env.Result,
		IsError:      env.IsError,
		SessionID:    env.SessionID,
	}, nil
}

// filePathRe matches relative source-file paths in agent text.
// Anchored after a non-path character so leading backtick/space/newline is not
// captured.  Supports the file extensions polyflow recognises.
var filePathRe = regexp.MustCompile(
	`(?:^|[\s` + "`" + `"'(\[])` +
		`([A-Za-z_.][A-Za-z0-9_./-]*/[A-Za-z0-9_./-]*\.` +
		`(?:go|ts|tsx|js|jsx|mjs|rb|py|yaml|yml|json|md|templ|erb|rake|sh|toml|mod|sum))`,
)

// ExtractFiles finds source file paths mentioned in agent response text.
// Paths are deduplicated and returned in sorted order (rule 2 determinism).
func ExtractFiles(text string) []string {
	seen := make(map[string]bool)
	for _, m := range filePathRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
