package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultAgentCmd is the pinned default agent CLI invocation for
// `polyflow eval agent`: an agent restricted to only polyflow's MCP tools,
// answering on stdin, with JSON output for deterministic parsing.
// Override via --agent-cmd or the POLYFLOW_AGENT_CMD env var.
const DefaultAgentCmd = `claude -p --mcp-config {mcp_config} --allowedTools "mcp__polyflow__*" --max-turns {max_turns} --output-format json`

// AgentPromptPreamble is prepended to every agent_cases question, pinning
// the agent to tool-reported facts instead of guesses.
const AgentPromptPreamble = "Answer using only the polyflow tools provided. Name concrete file paths. " +
	"Do not guess: if the tools report unresolved references or unmeasured trust, verify or say so."

// ErrAgentCLIUnavailable means the configured agent CLI binary isn't on
// PATH. This phase needs network + a logged-in agent CLI — a release
// ritual, not CI — so callers should treat this as a distinct skip, not a
// silent pass or a hard error.
var ErrAgentCLIUnavailable = errors.New("agent CLI not found on PATH")

// AgentCaseResult is one scored agent_cases answer.
type AgentCaseResult struct {
	ID           string   `json:"id"`
	Correct      bool     `json:"correct"`
	MissingFacts []string `json:"missing_facts"`
	ForbiddenHit []string `json:"forbidden_hit"`
	Turns        int      `json:"turns,omitempty"`
	InputTokens  int      `json:"input_tokens,omitempty"`
	OutputTokens int      `json:"output_tokens,omitempty"`
	Answer       string   `json:"answer"`
}

// AgentReport aggregates AgentCaseResults for one corpus repo.
type AgentReport struct {
	Repo        string            `json:"repo"`
	Correctness float64           `json:"correctness"` // correct / total
	Results     []AgentCaseResult `json:"results"`
}

// AgentRunOptions configures `polyflow eval agent`.
type AgentRunOptions struct {
	CorpusDir string
	AgentCmd  string // command template; "" falls back to POLYFLOW_AGENT_CMD then DefaultAgentCmd
}

// normalizeFact lowercases and normalizes path separators so matching is
// case-insensitive and \ / agnostic.
func normalizeFact(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\\", "/"))
}

// pathToken strips leading/trailing punctuation an agent's prose commonly
// wraps around a path (backticks, quotes, brackets, trailing sentence
// punctuation) so token comparison isn't defeated by "`play.go`." vs "play.go".
func pathToken(s string) string {
	return strings.Trim(s, "`'\"(),;:.!?[]{}")
}

// matchFact reports whether fact is satisfied by an agent's (already
// normalized) answer text. Non-path facts (no "/") use plain substring
// containment. Path facts match at path-token granularity, not raw
// substring: a whole token in the answer must equal the fact, or equal a
// "/"-aligned suffix of the fact — a shorter, less-qualified path in the
// answer (e.g. "handlers/play.go") satisfies a fully-qualified fact (e.g.
// "activity/handlers/play.go") because it names the same file. The reverse
// does not hold: a short fact is not satisfied merely because a longer
// answer path contains it as a trailing substring, since the walk is over
// the FACT's suffixes checked against whole answer tokens, never the other
// way around.
func matchFact(answerNorm, factNorm string) bool {
	if !strings.Contains(factNorm, "/") {
		return strings.Contains(answerNorm, factNorm)
	}
	tokens := make(map[string]bool)
	for _, f := range strings.Fields(answerNorm) {
		tokens[pathToken(f)] = true
	}
	segs := strings.Split(factNorm, "/")
	for i := 0; i < len(segs); i++ {
		if tokens[strings.Join(segs[i:], "/")] {
			return true
		}
	}
	return false
}

// ScoreAgentCase deterministically scores a single agent_cases answer:
// correct iff every RequiredFacts entry is matched and no ForbiddenFacts
// entry is. MissingFacts/ForbiddenHit are always non-nil so JSON always
// reports [] rather than null when a case is fully correct.
func ScoreAgentCase(c AgentCase, answer string) AgentCaseResult {
	answerNorm := normalizeFact(answer)
	missing := []string{}
	for _, f := range c.RequiredFacts {
		if !matchFact(answerNorm, normalizeFact(f)) {
			missing = append(missing, f)
		}
	}
	hit := []string{}
	for _, f := range c.ForbiddenFacts {
		if matchFact(answerNorm, normalizeFact(f)) {
			hit = append(hit, f)
		}
	}
	return AgentCaseResult{
		ID:           c.ID,
		Correct:      len(missing) == 0 && len(hit) == 0,
		MissingFacts: missing,
		ForbiddenHit: hit,
		Answer:       answer,
	}
}

// mcpConfigFile is the `--mcp-config` JSON shape the agent CLI template expects:
// a named server entry pointing at `polyflow mcp`, run with cwd set to the
// corpus workspace so its relative .polyflow/graph.db resolves correctly.
type mcpConfigFile struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd,omitempty"`
}

func writeMCPConfig(dir, polyflowBin, repoRoot string) (string, error) {
	cfg := mcpConfigFile{MCPServers: map[string]mcpServerEntry{
		"polyflow": {Command: polyflowBin, Args: []string{"mcp"}, Cwd: repoRoot},
	}}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp-config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// splitCmd tokenizes a command template on whitespace, keeping
// double-quoted spans (e.g. "mcp__polyflow__*") as a single argument.
func splitCmd(s string) []string {
	var args []string
	var cur strings.Builder
	inQuotes := false
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case r == ' ' && !inQuotes:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return args
}

// claudeCLIOutput is the permissive shape of `claude -p --output-format
// json`'s stdout. Fields absent from a given agent CLI version are simply
// left zero — cost/usage reporting is best-effort per the T.2 plan.
type claudeCLIOutput struct {
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// runAgentCase invokes the agent CLI for one agent_cases question and
// returns its scored result. If stdout doesn't parse as the expected JSON
// shape (a different agent CLI, or an older/newer version), the raw trimmed
// stdout is scored as the answer instead of failing the case outright.
func runAgentCase(ctx context.Context, cmdTemplate, mcpConfigPath string, c AgentCase) (AgentCaseResult, error) {
	maxTurns := c.MaxTurns
	if maxTurns == 0 {
		maxTurns = 8
	}
	rendered := strings.NewReplacer(
		"{mcp_config}", mcpConfigPath,
		"{max_turns}", fmt.Sprintf("%d", maxTurns),
	).Replace(cmdTemplate)

	argv := splitCmd(rendered)
	if len(argv) == 0 {
		return AgentCaseResult{}, fmt.Errorf("empty agent command template")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(AgentPromptPreamble + "\n\n" + c.Question)
	stdout, err := cmd.Output()
	if err != nil {
		return AgentCaseResult{}, fmt.Errorf("run agent CLI: %w", err)
	}

	var out claudeCLIOutput
	answer := strings.TrimSpace(string(stdout))
	turns, inTok, outTok := 0, 0, 0
	if json.Unmarshal(stdout, &out) == nil && out.Result != "" {
		answer = out.Result
		turns = out.NumTurns
		inTok = out.Usage.InputTokens
		outTok = out.Usage.OutputTokens
	}

	res := ScoreAgentCase(c, answer)
	res.Turns = turns
	res.InputTokens = inTok
	res.OutputTokens = outTok
	return res, nil
}

// RunAgentCorpus runs every agent_cases entry in a corpus manifest against
// the configured agent CLI and returns a scored AgentReport. A manifest with
// no agent_cases yields an empty, zero-case report (not an error) — the T.2
// deliverable is additive, so most corpora simply opt out. If the resolved
// agent CLI binary isn't on PATH, returns ErrAgentCLIUnavailable so callers
// can render it as a distinct skip rather than a failure.
func RunAgentCorpus(ctx context.Context, opts AgentRunOptions) (*AgentReport, error) {
	m, err := LoadManifest(opts.CorpusDir)
	if err != nil {
		return nil, err
	}
	if len(m.AgentCases) == 0 {
		return &AgentReport{Repo: m.Repo.Name}, nil
	}

	cmdTemplate := opts.AgentCmd
	if cmdTemplate == "" {
		cmdTemplate = os.Getenv("POLYFLOW_AGENT_CMD")
	}
	if cmdTemplate == "" {
		cmdTemplate = DefaultAgentCmd
	}

	argv0 := splitCmd(cmdTemplate)
	if len(argv0) == 0 {
		return nil, fmt.Errorf("empty agent command template")
	}
	if _, err := exec.LookPath(argv0[0]); err != nil {
		return nil, fmt.Errorf("%w: %q (set --agent-cmd or POLYFLOW_AGENT_CMD)", ErrAgentCLIUnavailable, argv0[0])
	}

	polyflowBin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve polyflow binary path: %w", err)
	}

	repoRoot := "."
	if m.Repo.Path != "" && m.Repo.Path != "." {
		repoRoot = m.Repo.Path
	}

	tmpDir, err := os.MkdirTemp("", "polyflow-eval-agent-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	mcpConfigPath, err := writeMCPConfig(tmpDir, polyflowBin, repoRoot)
	if err != nil {
		return nil, err
	}

	var results []AgentCaseResult
	for _, c := range m.AgentCases {
		res, err := runAgentCase(ctx, cmdTemplate, mcpConfigPath, c)
		if err != nil {
			return nil, fmt.Errorf("agent case %s: %w", c.ID, err)
		}
		results = append(results, res)
	}

	correct := 0
	for _, r := range results {
		if r.Correct {
			correct++
		}
	}
	correctness := 0.0
	if len(results) > 0 {
		correctness = float64(correct) / float64(len(results))
	}

	return &AgentReport{Repo: m.Repo.Name, Correctness: correctness, Results: results}, nil
}
