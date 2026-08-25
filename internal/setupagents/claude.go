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
	return "Anthropic's CLI agent — full MCP + post-tool-use hook support, plus a CLAUDE.md tool-preference nudge"
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

// RemoveMCP is SetupMCP's inverse, shelling out to `claude mcp remove` for
// the same reason SetupMCP shells to `claude mcp add`: this package doesn't
// own Claude Code's config storage format.
func (claudeAgent) RemoveMCP(scope string) (string, error) {
	claudeScope := "user"
	if scope == "repo" {
		claudeScope = "project"
	}
	manual := fmt.Sprintf("claude mcp remove --scope %s polyflow", claudeScope)

	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Sprintf("claude CLI not found on PATH — run this yourself:\n      %s", manual), nil
	}
	cmd := exec.Command("claude", "mcp", "remove", "--scope", claudeScope, "polyflow")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Removing an entry that isn't there errors instead of no-op'ing,
		// same asymmetry SetupMCP already works around for `add`.
		if strings.Contains(string(out), "not found") || strings.Contains(string(out), "No MCP server") {
			return fmt.Sprintf("MCP server was not registered (%s scope).", claudeScope), nil
		}
		return "", fmt.Errorf("%s\n%s\nrun it manually:\n      %s", err, strings.TrimSpace(string(out)), manual)
	}
	return fmt.Sprintf("MCP server unregistered (%s scope).", claudeScope), nil
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

// MCPStatus shells out to `claude mcp list` since Claude Code's own config
// storage (~/.claude.json plus possibly project-local state) isn't a file
// format this package owns or wants to parse — the same reasoning SetupMCP
// already applies by shelling to `claude mcp add` instead of hand-writing it.
// RemoveHooks strips only polyflow's own context-injection entries from
// settings.json, leaving any other hooks the user has configured (and the
// file itself) untouched.
func (claudeAgent) RemoveHooks(scope string) (string, error) {
	path, err := claudeSettingsPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	if !existed {
		return fmt.Sprintf("No %s file present — nothing to remove.", path), nil
	}
	if !unmergeClaudeHooks(doc) {
		return fmt.Sprintf("No context-injection hook found in %s.", path), nil
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed the context-injection hook from %s.", path), nil
}

func (claudeAgent) SupportsNudge() bool { return true }

// NudgeFile returns CLAUDE.md's path for scope: repo-scoped setup writes to
// the project root (checked into version control, same as this repo's own
// CLAUDE.md), user-scoped setup writes to ~/.claude/CLAUDE.md — Claude
// Code's documented global instructions file, loaded for every project
// regardless of which repo is open.
func (claudeAgent) NudgeFile(scope string) (string, error) {
	if scope == "repo" {
		return "CLAUDE.md", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

func (claudeAgent) MCPStatus(scope string) (bool, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return false, fmt.Errorf("claude CLI not found on PATH")
	}
	out, err := exec.Command("claude", "mcp", "list").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Contains(string(out), "polyflow"), nil
}

func (claudeAgent) HooksStatus(scope string) (bool, error) {
	path, err := claudeSettingsPath(scope)
	if err != nil {
		return false, err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return false, err
	}
	if !existed {
		return false, nil
	}
	return claudeHooksWired(doc), nil
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
// Grep, and Edit matchers, matching this repo's own .claude/settings.json
// shape. Grep is the native Claude Code search tool — distinct from `Bash
// grep`, which the Bash matcher already covers — and shares the same
// symbol-mode dedupe key, so a Grep call and a shell-grep call for the same
// symbol in one session only inject context once. Edit shares extractTarget's
// existing file_path handling (the same code path Read already uses) — an
// edit's target file already carries a file_path in tool_input, so wiring
// the matcher is the only change needed to surface a symbol's callers/
// callees right as the agent edits it. Idempotent: if a matcher group
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
	ensureMatcher("Edit")

	hooks["PostToolUse"] = postToolUse
	return added
}

// unmergeClaudeHooks removes any hook entry whose command contains
// "hook-context-inject" from every PostToolUse matcher group, matching
// claudeHooksWired's own substring reasoning. Matcher groups and the
// PostToolUse list itself are pruned once empty so removal doesn't leave
// dangling scaffolding behind; every other hook the user configured is
// left exactly as it was.
func unmergeClaudeHooks(doc map[string]any) (removed bool) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	postToolUse, _ := hooks["PostToolUse"].([]any)
	kept := postToolUse[:0:0]
	for _, g := range postToolUse {
		group, ok := g.(map[string]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		hookList, _ := group["hooks"].([]any)
		keptHooks := hookList[:0:0]
		for _, h := range hookList {
			if hm, ok := h.(map[string]any); ok {
				if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook-context-inject") {
					removed = true
					continue
				}
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 {
			continue // drop the now-empty matcher group entirely
		}
		group["hooks"] = keptHooks
		kept = append(kept, group)
	}
	if len(kept) == 0 {
		delete(hooks, "PostToolUse")
	} else {
		hooks["PostToolUse"] = kept
	}
	return removed
}

// claudeHooksWired reports whether Bash, Read, Grep, and Edit are all
// already wired to a context-injection hook command — matched by substring
// ("hook-context-inject") rather than an exact command string, since the
// polyflow binary path baked into the command can legitimately differ
// between the machine that ran setup and the one checking status.
func claudeHooksWired(doc map[string]any) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	postToolUse, _ := hooks["PostToolUse"].([]any)
	wired := map[string]bool{"Bash": false, "Read": false, "Grep": false, "Edit": false}
	for _, g := range postToolUse {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		matcher, _ := group["matcher"].(string)
		if _, tracked := wired[matcher]; !tracked {
			continue
		}
		hookList, _ := group["hooks"].([]any)
		for _, h := range hookList {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook-context-inject") {
				wired[matcher] = true
			}
		}
	}
	for _, ok := range wired {
		if !ok {
			return false
		}
	}
	return true
}
