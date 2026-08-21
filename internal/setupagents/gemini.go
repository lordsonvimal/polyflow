package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() { register(geminiCLIAgent{}) }

type geminiCLIAgent struct{}

func (geminiCLIAgent) Name() string        { return "gemini-cli" }
func (geminiCLIAgent) DisplayName() string { return "Gemini CLI" }
func (geminiCLIAgent) Description() string {
	return "MCP support via settings.json — no post-tool-use hook mechanism"
}
func (geminiCLIAgent) SupportsHooks() bool       { return false }
func (geminiCLIAgent) SupportsGlobalScope() bool { return false }

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

func (geminiCLIAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	return "", fmt.Errorf("gemini cli has no post-tool-use hook mechanism")
}

func (geminiCLIAgent) MCPStatus(scope string) (bool, error) {
	path, err := geminiCLISettingsPath(scope)
	if err != nil {
		return false, err
	}
	return mcpServerConfigured(path, "polyflow")
}

func (geminiCLIAgent) HooksStatus(scope string) (bool, error) {
	return false, fmt.Errorf("gemini cli has no post-tool-use hook mechanism")
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
