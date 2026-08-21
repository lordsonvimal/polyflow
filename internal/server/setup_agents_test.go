package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		MCPResult    string `json:"mcp_result"`
		HooksSkipped string `json:"hooks_skipped"`
	}
	decodeJSON(t, w.Body.Bytes(), &applyResp)
	if applyResp.MCPResult == "" {
		t.Fatal("expected a non-empty mcp_result")
	}
	if applyResp.HooksSkipped == "" {
		t.Fatal("expected hooks_skipped — cursor has no hook mechanism")
	}

	// Status should now reflect the write this same handler just made.
	statusReq := httptest.NewRequest("GET", "/api/setup/agents?scope=repo", nil)
	statusW := httptest.NewRecorder()
	srv.ServeHTTP(statusW, statusReq)

	var statusResp struct {
		Agents []struct {
			Name          string `json:"name"`
			MCPConfigured bool   `json:"mcp_configured"`
		} `json:"agents"`
	}
	decodeJSON(t, statusW.Body.Bytes(), &statusResp)
	for _, a := range statusResp.Agents {
		if a.Name == "cursor" && !a.MCPConfigured {
			t.Fatal("expected cursor to show mcp_configured=true after POST /api/setup/agent")
		}
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
