package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeY4Module writes a single-package server fixture exercising the Y.4
// response-type sinks: a local writeJSON wrapper (ResponseWriter-first),
// json.NewEncoder(w).Encode, a typed struct body, a slice body, and an untyped
// map body (which must be ledgered — no edge).
func writeY4Module(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()

	src := `package srv

import (
	"encoding/json"
	"net/http"
)

type Resp struct {
	ID string ` + "`json:\"id\"`" + `
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func handleTyped(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Resp{ID: "x"})
}

func handleSlice(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, []Resp{})
}

func handleUntyped(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleEncode(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(Resp{ID: "y"})
}
`
	files := map[string]string{
		"go.mod":     "module y4test\n\ngo 1.25.0\n",
		"srv/srv.go": src,
	}
	for name, content := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Function node IDs the pattern matcher would have emitted (service:file:
	// function:Name:line), so the semantic pass attributes returns edges.
	known = map[string]bool{
		"svc:srv/srv.go:function:writeJSON:12":     true,
		"svc:srv/srv.go:function:handleTyped:17":   true,
		"svc:srv/srv.go:function:handleSlice:21":   true,
		"svc:srv/srv.go:function:handleUntyped:25": true,
		"svc:srv/srv.go:function:handleEncode:29":  true,
	}
	return dir, known
}

func analyzeY4(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeY4Module(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

func returnsTo(edges []graph.Edge, fromSub string) []graph.Edge {
	var out []graph.Edge
	for _, e := range edges {
		if e.Type == graph.EdgeTypeReturns && strings.Contains(e.From, fromSub) {
			out = append(out, e)
		}
	}
	return out
}

// TestGoY4_Returns verifies each typed sink emits a returns edge to the Resp
// struct, that a []Resp body records container=slice, and that an untyped
// map body emits nothing (ledgered, #12).
func TestGoY4_Returns(t *testing.T) {
	res := analyzeY4(t)

	for _, tc := range []struct{ fn, container string }{
		{":handleTyped:", ""},
		{":handleSlice:", "slice"},
		{":handleEncode:", ""},
	} {
		got := returnsTo(res.Edges, tc.fn)
		if len(got) != 1 {
			t.Fatalf("%s: want 1 returns edge, got %d", tc.fn, len(got))
		}
		e := got[0]
		if !strings.Contains(e.To, ":struct:Resp:") {
			t.Errorf("%s: returns target = %s, want Resp struct", tc.fn, e.To)
		}
		if e.Meta["container"] != tc.container {
			t.Errorf("%s: container = %q, want %q", tc.fn, e.Meta["container"], tc.container)
		}
		if e.Confidence != graph.ConfidenceStatic {
			t.Errorf("%s: confidence = %q, want static", tc.fn, e.Confidence)
		}
	}

	if got := returnsTo(res.Edges, ":handleUntyped:"); len(got) != 0 {
		t.Errorf("untyped map body must be ledgered (no edge), got %d: %+v", len(got), got)
	}
}
