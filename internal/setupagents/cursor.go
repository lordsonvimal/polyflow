package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() { register(cursorAgent{}) }

type cursorAgent struct{}

func (cursorAgent) Name() string        { return "cursor" }
func (cursorAgent) DisplayName() string { return "Cursor" }
func (cursorAgent) Description() string {
	return "MCP support via mcp.json, a postToolUse context-injection hook, and an AGENTS.md tool-preference nudge"
}
func (cursorAgent) SupportsHooks() bool       { return true }
func (cursorAgent) SupportsGlobalScope() bool { return false }

func (cursorAgent) SetupMCP(scope, polyflowBin string) (string, error) {
	path, err := cursorMCPPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	mergeMCPServers(doc, "polyflow", polyflowBin, []string{"mcp"})
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	if !existed {
		return fmt.Sprintf("Created %s with the polyflow MCP server.", path), nil
	}
	return fmt.Sprintf("Registered the polyflow MCP server in %s.", path), nil
}

func (cursorAgent) RemoveMCP(scope string) (string, error) {
	path, err := cursorMCPPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	if !existed || !removeMCPServer(doc, "polyflow") {
		return fmt.Sprintf("MCP server was not registered in %s.", path), nil
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("Unregistered the polyflow MCP server from %s.", path), nil
}

func (cursorAgent) SupportsNudge() bool { return true }

// NudgeFile returns AGENTS.md's path for scope. Confirmed against
// cursor.com/docs/context/rules (fetched at implementation time): Cursor
// reads a plain AGENTS.md at the project root as an alternative to
// .cursor/rules, with no frontmatter and no user/global-level equivalent —
// user-level instructions go through Cursor's own Settings > Rules UI
// instead, which this package doesn't have a file to write to.
func (cursorAgent) NudgeFile(scope string) (string, error) {
	if scope != "repo" {
		return "", fmt.Errorf("cursor has no file-based user/global instructions equivalent to AGENTS.md — only Settings > Rules (not scriptable), use --scope repo")
	}
	return "AGENTS.md", nil
}

// SetupHooks wires polyflow's context-injection binary as a Cursor
// postToolUse hook. Confirmed against cursor.com/docs/hooks (fetched at
// implementation time, not assumed from any other client's shape):
// .cursor/hooks.json (repo scope) or ~/.cursor/hooks.json (user scope), a
// top-level {"version":1,"hooks":{...}} document, and a postToolUse payload
// carrying tool_name/tool_input/cwd — the same field names hookPayload
// already parses for Claude Code, so the existing hook-context-inject binary
// needs no Cursor-specific fork, only the extra output key added in
// hook_context_inject.go for Cursor's additional_context response shape.
func (cursorAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	path, err := cursorHooksPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	added := mergeCursorHooks(doc, polyflowBin+" hook-context-inject")
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

func (cursorAgent) RemoveHooks(scope string) (string, error) {
	path, err := cursorHooksPath(scope)
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
	if !unmergeCursorHooks(doc) {
		return fmt.Sprintf("No context-injection hook found in %s.", path), nil
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed the context-injection hook from %s.", path), nil
}

func (cursorAgent) MCPStatus(scope string) (bool, error) {
	path, err := cursorMCPPath(scope)
	if err != nil {
		return false, err
	}
	return mcpServerConfigured(path, "polyflow")
}

func (cursorAgent) HooksStatus(scope string) (bool, error) {
	path, err := cursorHooksPath(scope)
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
	return cursorHooksWired(doc), nil
}

func cursorMCPPath(scope string) (string, error) {
	if scope == "repo" {
		return filepath.Join(".cursor", "mcp.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cursor", "mcp.json"), nil
}

func cursorHooksPath(scope string) (string, error) {
	if scope == "repo" {
		return filepath.Join(".cursor", "hooks.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cursor", "hooks.json"), nil
}

// mergeCursorHooks wires command as a postToolUse hook, matching
// .cursor/hooks.json's documented {"version":1,"hooks":{"postToolUse":[...]}}
// shape. Idempotent: a matcher-less list is deduped purely on exact command
// string, since postToolUse (unlike Claude's PostToolUse) has no per-tool
// matcher grouping to key off of.
func mergeCursorHooks(doc map[string]any, command string) (added bool) {
	if _, ok := doc["version"]; !ok {
		doc["version"] = 1
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	doc["hooks"] = hooks

	postToolUse, _ := hooks["postToolUse"].([]any)
	for _, h := range postToolUse {
		if hm, ok := h.(map[string]any); ok {
			if cmd, _ := hm["command"].(string); cmd == command {
				return false // already wired
			}
		}
	}
	hooks["postToolUse"] = append(postToolUse, map[string]any{"command": command})
	return true
}

// unmergeCursorHooks removes any hook entry whose command contains
// "hook-context-inject" from postToolUse. No matcher grouping to prune here
// (unlike Claude/Gemini) — postToolUse is a flat list.
func unmergeCursorHooks(doc map[string]any) (removed bool) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	postToolUse, _ := hooks["postToolUse"].([]any)
	kept := postToolUse[:0:0]
	for _, h := range postToolUse {
		if hm, ok := h.(map[string]any); ok {
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook-context-inject") {
				removed = true
				continue
			}
		}
		kept = append(kept, h)
	}
	hooks["postToolUse"] = kept
	return removed
}

// cursorHooksWired matches by substring ("hook-context-inject") rather than
// an exact command string, mirroring claudeHooksWired's own reasoning: the
// polyflow binary path baked into the command can legitimately differ
// between the machine that ran setup and the one checking status.
func cursorHooksWired(doc map[string]any) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	postToolUse, _ := hooks["postToolUse"].([]any)
	for _, h := range postToolUse {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook-context-inject") {
			return true
		}
	}
	return false
}
