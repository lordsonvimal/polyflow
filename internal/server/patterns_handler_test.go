package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHandleListPatterns_IncludesEmbedded(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)

	req := httptest.NewRequest("GET", "/api/patterns", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Patterns []patternInfo `json:"patterns"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Patterns) == 0 {
		t.Fatalf("want at least one embedded pattern, got 0")
	}
	for _, p := range resp.Patterns {
		if p.Custom {
			t.Fatalf("no custom patterns registered yet, got custom=true for %q", p.Name)
		}
		if p.Source == "" {
			t.Fatalf("pattern %q missing source", p.Name)
		}
	}
}

func TestHandleListPatterns_LanguageFilter(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)

	req := httptest.NewRequest("GET", "/api/patterns?language=go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		Patterns []patternInfo `json:"patterns"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	for _, p := range resp.Patterns {
		if p.Language != "go" {
			t.Fatalf("want only language=go, got %q", p.Language)
		}
	}
}

func TestHandleAddPattern_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, configPath := writeTestConfig(t, dir, raw)

	body := addPatternBody{
		Name: "custom_test",
		Content: "language: go\n" +
			"patterns:\n" +
			"  - name: my_pattern\n" +
			"    query: \"(function_declaration) @fn\"\n" +
			"    extract:\n" +
			"      node_type: function\n",
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/patterns", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body)
	}

	// The added pattern now shows up as custom in the list.
	req2 := httptest.NewRequest("GET", "/api/patterns", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	var resp struct {
		Patterns []patternInfo `json:"patterns"`
	}
	decodeJSON(t, w2.Body.Bytes(), &resp)
	found := false
	for _, p := range resp.Patterns {
		if p.Name == "my_pattern" && p.Custom {
			found = true
		}
	}
	if !found {
		t.Fatalf("want custom pattern my_pattern in list, got %+v", resp.Patterns)
	}

	// polyflow.yml itself was updated with the new patterns entry — same
	// internals `patterns add` uses (workspace.Load/Save).
	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Contains(cfgBytes, []byte("custom_test.yaml")) {
		t.Fatalf("want polyflow.yml to reference custom_test.yaml, got:\n%s", cfgBytes)
	}
}

func TestHandleAddPattern_InvalidYAML_422(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)

	body := addPatternBody{Name: "broken", Content: "not: valid: yaml: at: all: ["}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/patterns", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]string
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Fatalf("want a verbatim error message, got %+v", resp)
	}
}
