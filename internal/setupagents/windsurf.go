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
	return "MCP support via mcp_config.json (user scope only) — no post-tool-use hook mechanism"
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

func (windsurfAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	return "", fmt.Errorf("windsurf has no post-tool-use hook mechanism")
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
	return false, fmt.Errorf("windsurf has no post-tool-use hook mechanism")
}

func windsurfMCPPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), nil
}
