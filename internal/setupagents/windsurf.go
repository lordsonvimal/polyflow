package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() { register(windsurfAgent{}) }

type windsurfAgent struct{}

func (windsurfAgent) Name() string        { return "windsurf" }
func (windsurfAgent) DisplayName() string { return "Windsurf" }
func (windsurfAgent) Description() string {
	// Windsurf does have a Cascade Hooks mechanism (docs.windsurf.com/windsurf/cascade/hooks,
	// hooks.json in ~/.codeium/windsurf or .windsurf) — just not yet wired up
	// here. Ship as its own increment once that contract is pinned, per this
	// codebase's "one client at a time" convention.
	return "MCP support via mcp_config.json (user scope only) and a .windsurfrules/global_rules.md tool-preference nudge — hook support not yet implemented"
}
func (windsurfAgent) SupportsHooks() bool       { return false }
func (windsurfAgent) SupportsGlobalScope() bool { return false }

func (windsurfAgent) SetupMCP(scope, polyflowBin string) (string, error) {
	if scope == "repo" {
		return "", fmt.Errorf("windsurf has no project-level MCP config — only a single per-user mcp_config.json, use --scope user")
	}
	path, err := windsurfMCPPath()
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

func (windsurfAgent) RemoveMCP(scope string) (string, error) {
	if scope == "repo" {
		return "", fmt.Errorf("windsurf has no project-level MCP config — only a single per-user mcp_config.json, use --scope user")
	}
	path, err := windsurfMCPPath()
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

func (windsurfAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	return "", fmt.Errorf("windsurf hook support is not yet implemented")
}

func (windsurfAgent) RemoveHooks(scope string) (string, error) {
	return "", fmt.Errorf("windsurf hook support is not yet implemented")
}

func (windsurfAgent) SupportsNudge() bool { return true }

// NudgeFile returns Windsurf's rules-file path for scope. Confirmed via
// docs.windsurf.com/windsurf/cascade/memories (checked at implementation
// time): project-level rules live in .windsurfrules at the repo root
// (12,000-char cap — this package's nudge block is a few hundred chars, well
// under it), and user-level rules live in
// ~/.codeium/windsurf/memories/global_rules.md (6,000-char cap), a sibling
// of the mcp_config.json path windsurfMCPPath already resolves.
func (windsurfAgent) NudgeFile(scope string) (string, error) {
	if scope == "repo" {
		return ".windsurfrules", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codeium", "windsurf", "memories", "global_rules.md"), nil
}

func (windsurfAgent) MCPStatus(scope string) (bool, error) {
	if scope == "repo" {
		return false, fmt.Errorf("windsurf has no project-level MCP config — only a single per-user mcp_config.json, use --scope user")
	}
	path, err := windsurfMCPPath()
	if err != nil {
		return false, err
	}
	return mcpServerConfigured(path, "polyflow")
}

func (windsurfAgent) HooksStatus(scope string) (bool, error) {
	return false, fmt.Errorf("windsurf hook support is not yet implemented")
}

func windsurfMCPPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), nil
}
