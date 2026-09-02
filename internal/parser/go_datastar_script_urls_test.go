package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// datastarScriptModule models a Datastar handler that ships a client-side
// `fetch(...)` to the browser via sse.ExecuteScript, with the request URL built
// by a sibling fmt.Sprintf — the maple-manager ExecConfigHandler.SaveConfig shape.
func datastarScriptModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/dstest\n\ngo 1.25.0\n",
		"datastar/sse.go": `package datastar

type SSE struct{}

func NewSSE() *SSE { return &SSE{} }

type ExecuteScriptOption func()

func (s *SSE) ExecuteScript(script string, opts ...ExecuteScriptOption) error { return nil }
`,
		"handler.go": `package handler

import (
	"fmt"

	"example.com/dstest/datastar"
)

type Config struct {
	ID      string
	Version int
}

func SaveConfig(cfg Config) {
	sse := datastar.NewSSE()

	detailPageURL := fmt.Sprintf("/maple/exec-configs/%s", cfg.ID)
	buildURL := fmt.Sprintf("/maple/exec-configs/%s/v/%d/do-build", cfg.ID, cfg.Version)
	buildScript := fmt.Sprintf(` + "`" + `
		fetch('%s', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			credentials: 'include'
		}).then(function(response) {
			setTimeout(function() { window.location.href = '%s'; }, 1500);
		});
	` + "`" + `, buildURL, detailPageURL, detailPageURL)
	sse.ExecuteScript(buildScript)
}

func RevertConfig(id string) {
	sse := datastar.NewSSE()
	// Literal URL, no Sprintf indirection.
	sse.ExecuteScript(` + "`" + `fetch('/maple/exec-configs/revert', { method: 'DELETE' })` + "`" + `)
}

func JustReload() {
	sse := datastar.NewSSE()
	// No fetch: a plain reload script must not mint an http_client.
	sse.ExecuteScript(` + "`" + `setTimeout(function(){window.location.reload()},1000)` + "`" + `)
}
`,
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDatastarScriptURL_ResolvesEmbeddedFetch(t *testing.T) {
	dir := datastarScriptModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:handler.go:function:SaveConfig:15":   true,
		"svc:handler.go:function:RevertConfig:40": true,
		"svc:handler.go:function:JustReload:50":   true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	got := map[string]graph.Node{} // path -> node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["synthesized"] == "datastar_script_fetch" {
			got[n.Meta["path"]] = n
		}
	}

	want := map[string]string{
		"/maple/exec-configs/*/v/*/do-build": "POST",
		"/maple/exec-configs/revert":         "DELETE",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d producers, got %d: %+v", len(want), len(got), got)
	}
	for path, method := range want {
		n, ok := got[path]
		if !ok {
			t.Fatalf("missing producer %q; got %+v", path, got)
		}
		if n.Meta["method"] != method {
			t.Errorf("%q: method=%q, want %q", path, n.Meta["method"], method)
		}
		if n.Meta["datastar"] != "true" {
			t.Errorf("%q: missing datastar marker", path)
		}
	}

	// The reload-only script must not have produced anything.
	for path, n := range got {
		if n.Meta["via_wrapper"] == "JustReload" {
			t.Errorf("reload-only script wrongly resolved: %q -> %+v", path, n)
		}
	}

	var callsEdges int
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "datastar_script_fetch" {
			callsEdges++
			if !known[e.From] {
				t.Errorf("edge from unknown node %q", e.From)
			}
		}
	}
	if callsEdges != len(want) {
		t.Errorf("expected %d calls edges, got %d", len(want), callsEdges)
	}
}

func TestDatastarScriptURL_Determinism(t *testing.T) {
	dir := datastarScriptModule(t)
	t.Chdir(dir)
	known := map[string]bool{
		"svc:handler.go:function:SaveConfig:15":   true,
		"svc:handler.go:function:RevertConfig:40": true,
		"svc:handler.go:function:JustReload:50":   true,
	}
	ids := func() []string {
		a := &GoSemanticAnalyzer{}
		res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
		var out []string
		for _, n := range res.Nodes {
			if n.Meta["synthesized"] == "datastar_script_fetch" {
				out = append(out, n.ID)
			}
		}
		return out
	}
	first, second := ids(), ids()
	if len(first) != len(second) {
		t.Fatalf("run count differs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("nondeterministic IDs at %d: %q vs %q", i, first[i], second[i])
		}
	}
}

func TestCountFormatVerbs(t *testing.T) {
	cases := map[string]int{
		"":                     0,
		"no verbs here":        0,
		"%s":                   1,
		"a %s b %d c":          2,
		"100%% off %s":         1,
		"%02d and %-10s":       2,
		"trailing bare %":      0,
		"fetch('%s') then %s;": 2,
	}
	for in, want := range cases {
		if got := countFormatVerbs(in); got != want {
			t.Errorf("countFormatVerbs(%q) = %d, want %d", in, got, want)
		}
	}
}
