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

func TestGeminiCLI_HooksStatus_FalseThenTrueAfterSetup(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := geminiCLIAgent{}

	wired, err := agent.HooksStatus("repo")
	if err != nil {
		t.Fatal(err)
	}
	if wired {
		t.Fatal("expected not wired before SetupHooks runs")
	}

	if _, err := agent.SetupHooks("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}

	wired, err = agent.HooksStatus("repo")
	if err != nil {
		t.Fatal(err)
	}
	if !wired {
		t.Fatal("expected wired after SetupHooks runs")
	}
}

func TestGeminiCLI_SetupHooks_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := geminiCLIAgent{}

	if _, err := agent.SetupHooks("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}
	doc, _, err := readJSONDoc(".gemini/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	afterTool := hooks["AfterTool"].([]any)
	if len(afterTool) != 1 {
		t.Fatalf("expected 1 matcher group after first run, got %d", len(afterTool))
	}
	group := afterTool[0].(map[string]any)
	if got := len(group["hooks"].([]any)); got != 1 {
		t.Fatalf("expected 1 hook entry after first run, got %d", got)
	}

	if _, err := agent.SetupHooks("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}
	doc, _, err = readJSONDoc(".gemini/settings.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks = doc["hooks"].(map[string]any)
	afterTool = hooks["AfterTool"].([]any)
	if len(afterTool) != 1 {
		t.Fatalf("expected still 1 matcher group after second run (idempotent), got %d", len(afterTool))
	}
	group = afterTool[0].(map[string]any)
	if got := len(group["hooks"].([]any)); got != 1 {
		t.Fatalf("expected still 1 hook entry after second run (idempotent), got %d", got)
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
