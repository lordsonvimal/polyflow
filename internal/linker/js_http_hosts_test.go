package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func writeJSFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func dynamicJSHTTPNode(file string, line int) graph.Node {
	return graph.Node{
		ID:       fmt.Sprintf("svc:%s:http_client:fetch_call:%d", file, line),
		Type:     graph.NodeTypeHTTPClient,
		Service:  "svc",
		File:     file,
		Line:     line,
		Language: "javascript",
		Meta:     map[string]string{"url": "*/api/graph"},
	}
}

// TestResolveJSHTTPHosts_EnvRead covers case 1: `${x}/path` where x is a
// module-level const read directly from process.env.
func TestResolveJSHTTPHosts_EnvRead(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"client.ts": `const _backendUrl = process.env.BACKEND_URL;

function fetchGraph() {
  return fetch(` + "`${_backendUrl}/api/graph`" + `);
}
`,
	})
	svcFiles := map[string][]string{"svc": paths}
	n := dynamicJSHTTPNode(filepath.Join(dir, "client.ts"), 4)
	nodes := []graph.Node{n}

	changed := ResolveJSHTTPHosts(nodes, svcFiles)
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed node, got %d", len(changed))
	}
	if got := nodes[0].Meta["env_var"]; got != "BACKEND_URL" {
		t.Errorf("env_var = %q, want BACKEND_URL", got)
	}
	if nodes[0].Meta["host_default_literal"] != "" {
		t.Errorf("host_default_literal should be empty for an env-derived host")
	}
	if nodes[0].Meta["confidence_ceiling"] != "" {
		t.Errorf("env-derived host must not carry a confidence ceiling")
	}
}

// TestResolveJSHTTPHosts_ModuleLevelDefault covers case 2: the GitNexus
// shape — a module-level `let` with a literal default, reassigned through an
// exported setter (a function-scoped reassignment, which must NOT disqualify
// the case).
func TestResolveJSHTTPHosts_ModuleLevelDefault(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"backend-client.ts": `let _backendUrl = 'http://localhost:4747';

export const setBackendUrl = (url: string): void => {
  _backendUrl = url.trim();
};

export function fetchGraph(params: string) {
  return fetch(` + "`${_backendUrl}/api/graph${params}`" + `);
}
`,
	})
	svcFiles := map[string][]string{"svc": paths}
	n := dynamicJSHTTPNode(filepath.Join(dir, "backend-client.ts"), 8)
	nodes := []graph.Node{n}

	changed := ResolveJSHTTPHosts(nodes, svcFiles)
	if len(changed) != 1 {
		t.Fatalf("expected 1 changed node, got %d", len(changed))
	}
	if got := nodes[0].Meta["host_default_literal"]; got != "http://localhost:4747" {
		t.Errorf("host_default_literal = %q, want http://localhost:4747", got)
	}
	if nodes[0].Meta["env_var"] != "" {
		t.Errorf("env_var should be empty for a literal-default host")
	}
	if got := nodes[0].Meta["confidence_ceiling"]; got != graph.ConfidencePartial {
		t.Errorf("confidence_ceiling = %q, want %q", got, graph.ConfidencePartial)
	}
}

// TestResolveJSHTTPHosts_ModuleScopeReassignAbstains covers the negative
// case: an unconditional, non-literal, module-scope reassignment (not inside
// any function) makes the declared default untrustworthy, so the pass must
// abstain rather than guess.
func TestResolveJSHTTPHosts_ModuleScopeReassignAbstains(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"backend-client.ts": `let _backendUrl = 'http://localhost:4747';

_backendUrl = computeRuntimeHost();

export function fetchGraph() {
  return fetch(` + "`${_backendUrl}/api/graph`" + `);
}
`,
	})
	svcFiles := map[string][]string{"svc": paths}
	n := dynamicJSHTTPNode(filepath.Join(dir, "backend-client.ts"), 6)
	nodes := []graph.Node{n}

	changed := ResolveJSHTTPHosts(nodes, svcFiles)
	if len(changed) != 0 {
		t.Fatalf("expected no changed nodes, got %d: %+v", len(changed), nodes[0].Meta)
	}
	if nodes[0].Meta["env_var"] != "" || nodes[0].Meta["host_default_literal"] != "" {
		t.Errorf("node must remain untouched, got meta %+v", nodes[0].Meta)
	}
}
