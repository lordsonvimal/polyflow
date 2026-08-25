package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHandleSetupAgents_ListsRegisteredAgentsWithStatus(t *testing.T) {
	t.Chdir(t.TempDir())
	srv := buildTestServer(t, nil, nil)

	req := httptest.NewRequest("GET", "/api/setup/agents?scope=repo", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Scope  string `json:"scope"`
		Agents []struct {
			Name          string `json:"name"`
			MCPConfigured bool   `json:"mcp_configured"`
		} `json:"agents"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Scope != "repo" {
		t.Fatalf("want scope repo, got %q", resp.Scope)
	}
	found := map[string]bool{}
	for _, a := range resp.Agents {
		found[a.Name] = true
		if a.MCPConfigured {
			t.Errorf("agent %q: want not configured in a fresh temp dir", a.Name)
		}
	}
	for _, want := range []string{"claude", "cursor", "windsurf", "gemini-cli"} {
		if !found[want] {
			t.Errorf("expected agent %q in the list, got %+v", want, resp.Agents)
		}
	}
}

func TestHandleSetupAgents_InvalidScope400(t *testing.T) {
	srv := buildTestServer(t, nil, nil)

	req := httptest.NewRequest("GET", "/api/setup/agents?scope=bogus", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleSetupAgentApply_CursorWritesConfigAndStatusFlips(t *testing.T) {
	t.Chdir(t.TempDir())
	srv := buildTestServer(t, nil, nil)

	buf, _ := json.Marshal(map[string]string{"agent": "cursor", "scope": "repo"})
	req := httptest.NewRequest("POST", "/api/setup/agent", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var applyResp struct {
		MCPResult   string `json:"mcp_result"`
		HooksResult string `json:"hooks_result"`
	}
	decodeJSON(t, w.Body.Bytes(), &applyResp)
	if applyResp.MCPResult == "" {
		t.Fatal("expected a non-empty mcp_result")
	}
	if applyResp.HooksResult == "" {
		t.Fatal("expected a non-empty hooks_result — cursor now has a postToolUse hook mechanism")
	}

	// Status should now reflect the write this same handler just made.
	statusReq := httptest.NewRequest("GET", "/api/setup/agents?scope=repo", nil)
	statusW := httptest.NewRecorder()
	srv.ServeHTTP(statusW, statusReq)

	var statusResp struct {
		Agents []struct {
			Name            string `json:"name"`
			MCPConfigured   bool   `json:"mcp_configured"`
			HooksConfigured bool   `json:"hooks_configured"`
		} `json:"agents"`
	}
	decodeJSON(t, statusW.Body.Bytes(), &statusResp)
	for _, a := range statusResp.Agents {
		if a.Name == "cursor" && !a.MCPConfigured {
			t.Fatal("expected cursor to show mcp_configured=true after POST /api/setup/agent")
		}
		if a.Name == "cursor" && !a.HooksConfigured {
			t.Fatal("expected cursor to show hooks_configured=true after POST /api/setup/agent")
		}
	}
}

func TestHandleSetupAgentRemove_CursorReversesApplyAndStatusFlipsBack(t *testing.T) {
	t.Chdir(t.TempDir())
	srv := buildTestServer(t, nil, nil)

	applyBuf, _ := json.Marshal(map[string]string{"agent": "cursor", "scope": "repo"})
	applyReq := httptest.NewRequest("POST", "/api/setup/agent", bytes.NewReader(applyBuf))
	applyW := httptest.NewRecorder()
	srv.ServeHTTP(applyW, applyReq)
	if applyW.Code != http.StatusOK {
		t.Fatalf("setup apply: want 200, got %d: %s", applyW.Code, applyW.Body)
	}

	before, err := os.ReadFile("AGENTS.md")
	if err != nil || len(before) == 0 {
		t.Fatalf("expected AGENTS.md to be written by apply, got err=%v content=%q", err, before)
	}

	removeBuf, _ := json.Marshal(map[string]string{"agent": "cursor", "scope": "repo"})
	removeReq := httptest.NewRequest("DELETE", "/api/setup/agent", bytes.NewReader(removeBuf))
	removeW := httptest.NewRecorder()
	srv.ServeHTTP(removeW, removeReq)

	if removeW.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", removeW.Code, removeW.Body)
	}
	var removeResp struct {
		MCPResult   string `json:"mcp_result"`
		HooksResult string `json:"hooks_result"`
		NudgeResult string `json:"nudge_result"`
	}
	decodeJSON(t, removeW.Body.Bytes(), &removeResp)
	if removeResp.MCPResult == "" || removeResp.HooksResult == "" || removeResp.NudgeResult == "" {
		t.Fatalf("expected all three non-empty result lines, got %+v", removeResp)
	}

	statusReq := httptest.NewRequest("GET", "/api/setup/agents?scope=repo", nil)
	statusW := httptest.NewRecorder()
	srv.ServeHTTP(statusW, statusReq)

	var statusResp struct {
		Agents []struct {
			Name            string `json:"name"`
			MCPConfigured   bool   `json:"mcp_configured"`
			HooksConfigured bool   `json:"hooks_configured"`
			NudgeConfigured bool   `json:"nudge_configured"`
		} `json:"agents"`
	}
	decodeJSON(t, statusW.Body.Bytes(), &statusResp)
	for _, a := range statusResp.Agents {
		if a.Name != "cursor" {
			continue
		}
		if a.MCPConfigured {
			t.Error("expected cursor mcp_configured=false after DELETE /api/setup/agent")
		}
		if a.HooksConfigured {
			t.Error("expected cursor hooks_configured=false after DELETE /api/setup/agent")
		}
		if a.NudgeConfigured {
			t.Error("expected cursor nudge_configured=false after DELETE /api/setup/agent")
		}
	}

	// AGENTS.md itself must survive removal — only polyflow's marked block
	// is stripped, not the whole file.
	if _, err := os.Stat("AGENTS.md"); err != nil {
		t.Fatalf("expected AGENTS.md to still exist after nudge removal: %v", err)
	}
}

func TestHandleSetupAgentRemove_UnknownAgent404(t *testing.T) {
	srv := buildTestServer(t, nil, nil)

	buf, _ := json.Marshal(map[string]string{"agent": "no-such-agent", "scope": "repo"})
	req := httptest.NewRequest("DELETE", "/api/setup/agent", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleSetupAgentApply_UnknownAgent404(t *testing.T) {
	srv := buildTestServer(t, nil, nil)

	buf, _ := json.Marshal(map[string]string{"agent": "no-such-agent", "scope": "repo"})
	req := httptest.NewRequest("POST", "/api/setup/agent", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}
