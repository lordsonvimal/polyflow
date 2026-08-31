// Package agentbench provides transcript parsing, scoring, and report generation
// for the P.1 agent outcome benchmark.  Actual claude invocations are performed
// by the bench command (cmd/polyflow/bench.go); this package is the testable core.
package agentbench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Transcript is the parsed result of one `claude -p --output-format json` run.
//
// InputTokens/OutputTokens are the raw (uncached) prompt and completion tokens
// the result envelope reports. On their own they badly understate a tool-using
// agent's cost: the growing transcript is re-fed to the model on every tool
// round-trip, and that bulk is billed as cached-input tokens. ContextTokens
// sums input + cache-creation + cache-read into the total the model actually
// processed across the run — the metric that separates an agent that answered
// from one MCP call from one that ground through a dozen grep/read round-trips.
type Transcript struct {
	DurationMs          int64   `json:"duration_ms"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	ContextTokens       int     `json:"context_tokens"`
	NumTurns            int     `json:"num_turns"`
	TotalCostUSD        float64 `json:"total_cost_usd"`
	Result              string  `json:"result"`
	IsError             bool    `json:"is_error"`
	SessionID           string  `json:"session_id"`
}

// claudeEnvelope mirrors the `claude -p --output-format json` JSON envelope.
type claudeEnvelope struct {
	Type         string  `json:"type"`
	IsError      bool    `json:"is_error"`
	DurationMs   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	Result       string  `json:"result"`
	SessionID    string  `json:"session_id"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
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
		DurationMs:          env.DurationMs,
		InputTokens:         env.Usage.InputTokens,
		OutputTokens:        env.Usage.OutputTokens,
		CacheCreationTokens: env.Usage.CacheCreationInputTokens,
		CacheReadTokens:     env.Usage.CacheReadInputTokens,
		ContextTokens: env.Usage.InputTokens + env.Usage.CacheCreationInputTokens +
			env.Usage.CacheReadInputTokens,
		NumTurns:     env.NumTurns,
		TotalCostUSD: env.TotalCostUSD,
		Result:       env.Result,
		IsError:      env.IsError,
		SessionID:    env.SessionID,
	}, nil
}

// SessionTranscriptDir returns the directory Claude Code stores session logs
// in for a given working directory, mirroring the CLI's own escaping: every
// "/" in the absolute path becomes "-".
func SessionTranscriptDir(homeDir, cwd string) string {
	return filepath.Join(homeDir, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
}

// sessionLine is the subset of a Claude Code session-log JSONL entry needed
// to pull assistant text out of it. Most lines (tool_use, tool_result, meta
// events) don't match "assistant" and are skipped rather than erroring.
type sessionLine struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// SessionAssistantText reads the Claude Code session log for sessionID and
// concatenates the text of every assistant turn, in order.
//
// `claude -p --output-format json`'s "result" field is only the *last*
// turn's text. An agent that finds the full answer via a tool call and then
// takes one more turn to verify a side detail (e.g. "does this spec file
// exist?") has its complete earlier answer invisible to anything reading
// just the envelope, even though the session log still has it. This reads
// the log directly so a trailing narrow follow-up can't erase an otherwise-
// correct answer.
func SessionAssistantText(homeDir, cwd, sessionID string) (string, error) {
	path := filepath.Join(SessionTranscriptDir(homeDir, cwd), sessionID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry sessionLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // non-message lines (summaries, meta events) aren't this shape
		}
		if entry.Type != "assistant" {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type == "text" && c.Text != "" {
				sb.WriteString(c.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String(), nil
}

// filePathRe matches relative source-file paths in agent text.
// Anchored after a non-path character so leading backtick/space/newline is not
// captured.  Supports the file extensions polyflow recognises.
var filePathRe = regexp.MustCompile(
	`(?:^|[\s` + "`" + `"'(\[])` +
		`([A-Za-z_.][A-Za-z0-9_./-]*/[A-Za-z0-9_./-]*\.` +
		`(?:go|ts|tsx|js|jsx|mjs|rb|py|yaml|yml|json|md|templ|erb|rake|sh|toml|mod|sum))`,
)

// blankLineRe splits agent text into blocks on a blank line. A markdown
// heading and the fenced code block directly beneath it are one block (only
// a single "\n" separates them), which is exactly the shape negationCueRe
// needs: the caveat lives in the heading prose, the files it caveats live in
// the fence.
var blankLineRe = regexp.MustCompile(`\n[ \t]*\n`)

// negationCueRe matches phrasing an agent uses to name a file while
// explicitly saying it is NOT part of the answer. Found via orion-atlas's
// bench precision (0.538 on a task where the agent's own text read "The
// deeper files... are reached transitively through the job, not through
// `#create` directly — only relevant if you're changing what the job
// receives" and every one of those correctly-caveated files still counted
// as a false positive): ExtractFiles had no concept of negation, only
// "does a file-shaped token appear anywhere in the text".
var negationCueRe = regexp.MustCompile(`(?i)(` +
	`not (?:directly )?(?:relevant|needed|required|necessary)|` +
	`don'?t need|do not need|no need to|` +
	`won'?t need|will not need|` +
	`only (?:relevant|needed|applicable) if|only if you|` +
	`unless you|not necessary unless|not required unless` +
	`)`)

// ExtractFiles finds source file paths mentioned in agent response text.
// Paths are deduplicated and returned in sorted order (rule 2 determinism).
//
// Text is split into blocks on blank lines first, and a block containing a
// negationCueRe match is skipped entirely — coarser than tying the caveat to
// the specific file names it refers to, but that's the safe direction for a
// precision metric: undercounting a real hit here is silent (the file just
// isn't in the returned set, same as never having been mentioned), whereas
// the status quo before this — no negation awareness — actively scored a
// file the agent explicitly said not to touch as if it had been recommended.
func ExtractFiles(text string) []string {
	seen := make(map[string]bool)
	for _, block := range blankLineRe.Split(text, -1) {
		if negationCueRe.MatchString(block) {
			continue
		}
		for _, m := range filePathRe.FindAllStringSubmatch(block, -1) {
			if len(m) >= 2 {
				seen[m[1]] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
