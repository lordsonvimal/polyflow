package setupagents

import "testing"

func mcpServerRegistered(doc map[string]any, name, command string) bool {
	servers, _ := doc["mcpServers"].(map[string]any)
	entry, ok := servers[name].(map[string]any)
	if !ok {
		return false
	}
	return entry["command"] == command
}

func TestWindsurf_SetupMCP_RejectsRepoScope(t *testing.T) {
	if _, err := (windsurfAgent{}).SetupMCP("repo", "polyflow"); err == nil {
		t.Fatal("expected error for repo scope — windsurf has no project-level MCP config")
	}
}

func TestWindsurf_MCPStatus_RejectsRepoScope(t *testing.T) {
	if _, err := (windsurfAgent{}).MCPStatus("repo"); err == nil {
		t.Fatal("expected error for repo scope — windsurf has no project-level MCP config")
	}
}

func TestWindsurf_HooksStatus_AlwaysErrors(t *testing.T) {
	if _, err := (windsurfAgent{}).HooksStatus("user"); err == nil {
		t.Fatal("expected error — windsurf has no hook mechanism")
	}
}

func TestWindsurf_MergeMCPServers_Idempotent(t *testing.T) {
	doc := map[string]any{}
	mergeMCPServers(doc, "polyflow", "polyflow", []string{"mcp"})
	if !mcpServerRegistered(doc, "polyflow", "polyflow") {
		t.Fatal("expected polyflow entry after first merge")
	}
	mergeMCPServers(doc, "polyflow", "polyflow", []string{"mcp"})
	servers, _ := doc["mcpServers"].(map[string]any)
	if len(servers) != 1 {
		t.Fatalf("expected exactly one entry after re-merge, got %d", len(servers))
	}
}
