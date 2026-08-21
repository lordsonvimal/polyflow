package setupagents

import "testing"

func TestGeminiCLI_SettingsPath_RepoVsUser(t *testing.T) {
	repoPath, err := geminiCLISettingsPath("repo")
	if err != nil {
		t.Fatal(err)
	}
	if repoPath != ".gemini/settings.json" {
		t.Errorf("repo path = %q, want .gemini/settings.json", repoPath)
	}
	userPath, err := geminiCLISettingsPath("user")
	if err != nil {
		t.Fatal(err)
	}
	if userPath == repoPath {
		t.Error("user path should differ from repo path")
	}
}

func TestGeminiCLI_MCPStatus_FalseThenTrueAfterSetup(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := geminiCLIAgent{}

	configured, err := agent.MCPStatus("repo")
	if err != nil {
		t.Fatal(err)
	}
	if configured {
		t.Fatal("expected not configured before SetupMCP runs")
	}

	if _, err := agent.SetupMCP("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}

	configured, err = agent.MCPStatus("repo")
	if err != nil {
		t.Fatal(err)
	}
	if !configured {
		t.Fatal("expected configured after SetupMCP runs")
	}
}

func TestGeminiCLI_HooksStatus_AlwaysErrors(t *testing.T) {
	if _, err := (geminiCLIAgent{}).HooksStatus("repo"); err == nil {
		t.Fatal("expected error — gemini cli has no hook mechanism")
	}
}

func TestGeminiCLI_MergeMCPServers_Idempotent(t *testing.T) {
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
