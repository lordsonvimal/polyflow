package setupagents

import "testing"

func TestCursor_MCPStatus_FalseThenTrueAfterSetup(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := cursorAgent{}

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

func TestCursor_HooksStatus_FalseThenTrueAfterSetup(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := cursorAgent{}

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

func TestCursor_SetupHooks_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := cursorAgent{}

	if _, err := agent.SetupHooks("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}
	doc, _, err := readJSONDoc(".cursor/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	if got := len(hooks["postToolUse"].([]any)); got != 1 {
		t.Fatalf("expected 1 hook entry after first run, got %d", got)
	}

	if _, err := agent.SetupHooks("repo", "polyflow"); err != nil {
		t.Fatal(err)
	}
	doc, _, err = readJSONDoc(".cursor/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks = doc["hooks"].(map[string]any)
	if got := len(hooks["postToolUse"].([]any)); got != 1 {
		t.Fatalf("expected still 1 hook entry after second run (idempotent), got %d", got)
	}
}
