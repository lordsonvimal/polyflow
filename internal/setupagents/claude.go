package setupagents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { register(claudeAgent{}) }

type claudeAgent struct{}

func (claudeAgent) Name() string        { return "claude" }
func (claudeAgent) DisplayName() string { return "Claude Code" }
func (claudeAgent) Description() string {
	return "Anthropic's CLI agent — full MCP + post-tool-use hook support"
}
func (claudeAgent) SupportsHooks() bool       { return true }
func (claudeAgent) SupportsGlobalScope() bool { return false }

// SetupMCP shells out to `claude mcp add` rather than hand-writing Claude
// Code's own config files: `--scope project` already knows to write .mcp.json
// and `--scope user` already knows the right place inside ~/.claude.json
// (a file with plenty of other state we don't want to risk corrupting by
// guessing its schema).
func (claudeAgent) SetupMCP(scope, polyflowBin string) (string, error) {
	claudeScope := "user"
	if scope == "repo" {
		claudeScope = "project"
	}
	manual := fmt.Sprintf("claude mcp add --scope %s polyflow -- %s mcp", claudeScope, polyflowBin)

	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Sprintf("claude CLI not found on PATH — run this yourself once it's installed:\n      %s", manual), nil
	}
	cmd := exec.Command("claude", "mcp", "add", "--scope", claudeScope, "polyflow", "--", polyflowBin, "mcp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// `claude mcp add` isn't itself idempotent — a second `setup` run
		// against the same scope errors instead of no-op'ing. Treat that one
		// case as success so re-running setup stays safe, matching the hook
		// merge's own idempotency below.
		if strings.Contains(string(out), "already exists") {
			return fmt.Sprintf("MCP server already registered (%s scope).", claudeScope), nil
		}
		return "", fmt.Errorf("%s\n%s\nrun it manually:\n      %s", err, strings.TrimSpace(string(out)), manual)
	}
	return fmt.Sprintf("MCP server registered (%s scope). %s", claudeScope, strings.TrimSpace(string(out))), nil
}

func (claudeAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	path, err := claudeSettingsPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	added := mergeClaudeHooks(doc, polyflowBin+" hook-context-inject")
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	switch {
	case !existed:
		return fmt.Sprintf("Created %s with the context-injection hook.", path), nil
	case added:
		return fmt.Sprintf("Added the context-injection hook to %s.", path), nil
	default:
		return fmt.Sprintf("Context-injection hook already present in %s.", path), nil
	}
}

func claudeSettingsPath(scope string) (string, error) {
	if scope == "repo" {
		return filepath.Join(".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// mergeClaudeHooks wires command as a PostToolUse hook for the Bash, Read,
// and Grep matchers, matching this repo's own .claude/settings.json shape.
// Grep is the native Claude Code search tool — distinct from `Bash grep`,
// which the Bash matcher already covers — and shares the same symbol-mode
// dedupe key, so a Grep call and a shell-grep call for the same symbol in
// one session only inject context once. Idempotent: if a matcher group
// already contains a hook with this exact command, it's left alone rather
// than duplicated.
func mergeClaudeHooks(doc map[string]any, command string) (added bool) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	doc["hooks"] = hooks

	postToolUse, _ := hooks["PostToolUse"].([]any)

	ensureMatcher := func(matcher string) {
		for _, g := range postToolUse {
			group, ok := g.(map[string]any)
			if !ok || group["matcher"] != matcher {
				continue
			}
			hookList, _ := group["hooks"].([]any)
			for _, h := range hookList {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); cmd == command {
						return // already wired
					}
				}
			}
			group["hooks"] = append(hookList, map[string]any{"type": "command", "command": command})
			added = true
			return
		}
		postToolUse = append(postToolUse, map[string]any{
			"matcher": matcher,
			"hooks":   []any{map[string]any{"type": "command", "command": command}},
		})
		added = true
	}
	ensureMatcher("Bash")
	ensureMatcher("Read")
	ensureMatcher("Grep")

	hooks["PostToolUse"] = postToolUse
	return added
}
