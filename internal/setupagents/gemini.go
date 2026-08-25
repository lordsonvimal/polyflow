package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() { register(geminiCLIAgent{}) }

type geminiCLIAgent struct{}

func (geminiCLIAgent) Name() string        { return "gemini-cli" }
func (geminiCLIAgent) DisplayName() string { return "Gemini CLI" }
func (geminiCLIAgent) Description() string {
	return "MCP support plus an AfterTool context-injection hook via settings.json, and a GEMINI.md tool-preference nudge"
}
func (geminiCLIAgent) SupportsHooks() bool       { return true }
func (geminiCLIAgent) SupportsGlobalScope() bool { return false }

// geminiHookMatcher restricts the hook to the two built-in tools whose
// argument shape is confirmed (geminicli.com/docs/tools/file-system,
// .../tools/shell): read_file's file_path and run_shell_command's command,
// both field names extractTarget already parses for Claude/Cursor. Gemini
// CLI's grep-equivalent tool name/params weren't independently verifiable at
// implementation time, so it's deliberately left out rather than guessed.
const geminiHookMatcher = "read_file|run_shell_command"

func (geminiCLIAgent) SetupMCP(scope, polyflowBin string) (string, error) {
	path, err := geminiCLISettingsPath(scope)
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

func (geminiCLIAgent) RemoveMCP(scope string) (string, error) {
	path, err := geminiCLISettingsPath(scope)
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

func (geminiCLIAgent) SupportsNudge() bool { return true }

// NudgeFile returns GEMINI.md's path for scope. Confirmed against
// geminicli.com/docs/cli/gemini-md (fetched at implementation time):
// Gemini CLI's default context filename is GEMINI.md (configurable via
// settings.json's context.fileName, which this package doesn't second-guess),
// loaded from the project root for repo scope and from ~/.gemini/GEMINI.md
// for user scope — a sibling of settings.json inside the same .gemini
// directory geminiCLISettingsPath already resolves.
func (geminiCLIAgent) NudgeFile(scope string) (string, error) {
	if scope == "repo" {
		return "GEMINI.md", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "GEMINI.md"), nil
}

// SetupHooks wires polyflow's context-injection binary as a Gemini CLI
// AfterTool hook. Confirmed against geminicli.com/docs/hooks/reference
// (fetched at implementation time): settings.json's "hooks" key, an
// "AfterTool" event carrying tool_name/tool_input/cwd/session_id — the same
// field names hookPayload already parses — and a response nested under
// hookSpecificOutput.additionalContext, which hook_context_inject.go now
// also emits alongside Claude's and Cursor's flat shapes.
func (geminiCLIAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	path, err := geminiCLISettingsPath(scope)
	if err != nil {
		return "", err
	}
	doc, existed, err := readJSONDoc(path)
	if err != nil {
		return "", err
	}
	added := mergeGeminiHooks(doc, polyflowBin+" hook-context-inject")
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

func (geminiCLIAgent) RemoveHooks(scope string) (string, error) {
	path, err := geminiCLISettingsPath(scope)
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
	if !unmergeGeminiHooks(doc) {
		return fmt.Sprintf("No context-injection hook found in %s.", path), nil
	}
	if err := writeJSONDoc(path, doc); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed the context-injection hook from %s.", path), nil
}

func (geminiCLIAgent) MCPStatus(scope string) (bool, error) {
	path, err := geminiCLISettingsPath(scope)
	if err != nil {
		return false, err
	}
	return mcpServerConfigured(path, "polyflow")
}

func (geminiCLIAgent) HooksStatus(scope string) (bool, error) {
	path, err := geminiCLISettingsPath(scope)
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
	return geminiHooksWired(doc), nil
}

// mergeGeminiHooks wires command as an AfterTool hook, matching
// settings.json's documented {"hooks":{"AfterTool":[{"matcher":...,
// "hooks":[{"type":"command","command":...}]}]}} shape — structurally the
// same matcher-grouped list Claude's PostToolUse uses, just one event name
// and one combined matcher regex instead of three separate matchers.
func mergeGeminiHooks(doc map[string]any, command string) (added bool) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	doc["hooks"] = hooks

	afterTool, _ := hooks["AfterTool"].([]any)
	for _, g := range afterTool {
		group, ok := g.(map[string]any)
		if !ok || group["matcher"] != geminiHookMatcher {
			continue
		}
		hookList, _ := group["hooks"].([]any)
		for _, h := range hookList {
			if hm, ok := h.(map[string]any); ok {
				if cmd, _ := hm["command"].(string); cmd == command {
					return false // already wired
				}
			}
		}
		group["hooks"] = append(hookList, map[string]any{"type": "command", "command": command})
		hooks["AfterTool"] = afterTool
		return true
	}
	afterTool = append(afterTool, map[string]any{
		"matcher": geminiHookMatcher,
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	})
	hooks["AfterTool"] = afterTool
	return true
}

// unmergeGeminiHooks removes any hook entry whose command contains
// "hook-context-inject" from every AfterTool matcher group, mirroring
// unmergeClaudeHooks' own pruning behavior for the analogous PostToolUse
// shape.
func unmergeGeminiHooks(doc map[string]any) (removed bool) {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	afterTool, _ := hooks["AfterTool"].([]any)
	kept := afterTool[:0:0]
	for _, g := range afterTool {
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
			continue
		}
		group["hooks"] = keptHooks
		kept = append(kept, group)
	}
	if len(kept) == 0 {
		delete(hooks, "AfterTool")
	} else {
		hooks["AfterTool"] = kept
	}
	return removed
}

// geminiHooksWired matches by substring ("hook-context-inject"), mirroring
// claudeHooksWired's and cursorHooksWired's own reasoning: the polyflow
// binary path baked into the command can legitimately differ between the
// machine that ran setup and the one checking status.
func geminiHooksWired(doc map[string]any) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	afterTool, _ := hooks["AfterTool"].([]any)
	for _, g := range afterTool {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hookList, _ := group["hooks"].([]any)
		for _, h := range hookList {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook-context-inject") {
				return true
			}
		}
	}
	return false
}

func geminiCLISettingsPath(scope string) (string, error) {
	if scope == "repo" {
		return filepath.Join(".gemini", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "settings.json"), nil
}
