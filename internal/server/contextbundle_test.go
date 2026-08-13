package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func postBundle(t *testing.T, srv *Server, body string) (*httptest.ResponseRecorder, bundleResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/context/bundle", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var resp bundleResponse
	if w.Code == http.StatusOK {
		decodeJSON(t, w.Body.Bytes(), &resp)
	}
	return w, resp
}

func TestHandleContextBundle_NodeViewed(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1"]}],"mode":"viewed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "createUser") {
		t.Fatalf("markdown missing target label: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "role: target") {
		t.Fatalf("markdown missing role line: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "auth/user.go:10") {
		t.Fatalf("markdown missing file:line: %s", resp.Markdown)
	}
	// viewed mode: no upstream/downstream neighbors pulled in.
	if strings.Contains(resp.Markdown, "handleLogin") || strings.Contains(resp.Markdown, "hashPassword") {
		t.Fatalf("viewed mode should not include neighbors: %s", resp.Markdown)
	}
	if resp.Truncated {
		t.Fatalf("want not truncated")
	}
	if len(resp.Omitted) != 0 {
		t.Fatalf("want empty omitted, got %v", resp.Omitted)
	}
}

func TestHandleContextBundle_NodeExpanded(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	// n2 --calls--> n1 --calls--> n3
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1"]}],"mode":"expanded","depth":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "handleLogin") {
		t.Fatalf("expanded mode should include upstream caller: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "hashPassword") {
		t.Fatalf("expanded mode should include downstream callee: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "upstream via calls") {
		t.Fatalf("missing upstream role: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "downstream via calls") {
		t.Fatalf("missing downstream role: %s", resp.Markdown)
	}
}

func TestHandleContextBundle_EdgeViewed(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"edge","ids":["e1"]}],"mode":"viewed"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "edge source (calls)") || !strings.Contains(resp.Markdown, "edge target (calls)") {
		t.Fatalf("missing edge endpoint roles: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "handleLogin") || !strings.Contains(resp.Markdown, "createUser") {
		t.Fatalf("missing both endpoints: %s", resp.Markdown)
	}
}

func TestHandleContextBundle_EdgeExpandedSeamFanOut(t *testing.T) {
	srv := buildTestServer(t, seamNodes(), seamEdges())
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"edge","ids":["e-p1-ch1"]}],"mode":"expanded"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "sub1") || !strings.Contains(resp.Markdown, "sub2") {
		t.Fatalf("expanded edge bundle should fan out to both consumers: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "## Flow") {
		t.Fatalf("expected a Flow section: %s", resp.Markdown)
	}
}

func TestHandleContextBundle_Flow(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	// n2 (http_handler, entrypoint) --calls--> n1 --calls--> n3; flow through n1.
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"flow","ids":["n1"]}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "## Flow") {
		t.Fatalf("expected a Flow section: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "handleLogin") {
		t.Fatalf("expected entrypoint handleLogin in flow chain: %s", resp.Markdown)
	}
}

func TestHandleContextBundle_UnknownID(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, _ := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["nope"]}]}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "nope") {
		t.Fatalf("error should name the unknown id: %s", w.Body)
	}
}

func TestHandleContextBundle_BadMode(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, _ := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1"]}],"mode":"bogus"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleContextBundle_EmptyElements(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, _ := postBundle(t, srv, `{"elements":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

// TestHandleContextBundle_Snippets exercises snippet inlining bounded by
// EndLine (UB.0), including the "snippets: false omits fences" contract.
func TestHandleContextBundle_Snippets(t *testing.T) {
	tmp := t.TempDir()
	src := "package auth\n\nfunc createUser() {\n\treturn\n}\n"
	if err := os.WriteFile(filepath.Join(tmp, "user.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	nodes := []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "createUser", Service: "auth", File: "user.go", Line: 3, EndLine: 5, Language: "go"},
	}
	srv := buildTestServer(t, nodes, nil)
	srv.SetConfigPath(filepath.Join(tmp, "polyflow.yml"))

	w, resp := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1"]}],"snippets":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(resp.Markdown, "func createUser") {
		t.Fatalf("expected inlined snippet: %s", resp.Markdown)
	}
	if !strings.Contains(resp.Markdown, "```go") {
		t.Fatalf("expected a go fence: %s", resp.Markdown)
	}

	w2, resp2 := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1"]}],"snippets":false}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w2.Code, w2.Body)
	}
	if strings.Contains(resp2.Markdown, "func createUser") {
		t.Fatalf("snippets:false should omit the fence: %s", resp2.Markdown)
	}
}

// TestHandleContextBundle_MaxTokensDropsElements verifies over-budget
// truncation drops whole blocks smallest-value-last and names them in both
// Omitted and the markdown footer.
func TestHandleContextBundle_MaxTokensDropsElements(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	w, resp := postBundle(t, srv, `{"elements":[{"kind":"node","ids":["n1","n2","n3"]}],"max_tokens":5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if !resp.Truncated {
		t.Fatalf("want truncated at a 5-token budget")
	}
	if len(resp.Omitted) == 0 {
		t.Fatalf("want at least one omitted id")
	}
	if !strings.Contains(resp.Markdown, "Truncated at 5 tokens") {
		t.Fatalf("want a truncation footer line: %s", resp.Markdown)
	}
}

// TestHandleContextBundle_Determinism runs the same expanded bundle twice
// and requires byte-identical markdown (rule 2).
func TestHandleContextBundle_Determinism(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	body := `{"elements":[{"kind":"node","ids":["n1"]}],"mode":"expanded","depth":3}`
	_, r1 := postBundle(t, srv, body)
	_, r2 := postBundle(t, srv, body)
	if r1.Markdown != r2.Markdown {
		t.Fatalf("non-deterministic markdown:\n--- run1 ---\n%s\n--- run2 ---\n%s", r1.Markdown, r2.Markdown)
	}
}
