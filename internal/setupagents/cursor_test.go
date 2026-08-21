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

func TestCursor_HooksStatus_AlwaysErrors(t *testing.T) {
	if _, err := (cursorAgent{}).HooksStatus("repo"); err == nil {
		t.Fatal("expected error — cursor has no hook mechanism")
	}
}
