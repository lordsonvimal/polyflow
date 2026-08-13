package setupagents

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() { register(cursorAgent{}) }

type cursorAgent struct{}

func (cursorAgent) Name() string        { return "cursor" }
func (cursorAgent) DisplayName() string { return "Cursor" }
func (cursorAgent) Description() string {
	return "MCP support via mcp.json — no post-tool-use hook mechanism"
}
func (cursorAgent) SupportsHooks() bool       { return false }
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

func (cursorAgent) SetupHooks(scope, polyflowBin string) (string, error) {
	return "", fmt.Errorf("cursor has no post-tool-use hook mechanism")
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
