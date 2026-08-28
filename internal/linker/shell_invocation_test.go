package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// scriptNodeIDFor returns the synthetic (script) scope node ID for
// (service, file) — matching internal/parser/shell.go's own format.
func scriptNodeIDFor(service, file string) string {
	return service + ":" + file + ":function:(script):0"
}

// makeScriptNode builds a synthetic (script) scope node (as
// internal/parser/shell.go would mint for every indexed shell file).
func makeScriptNode(service, file string) graph.Node {
	return graph.Node{
		ID:      scriptNodeIDFor(service, file),
		Type:    graph.NodeTypeFunction,
		Label:   "(script)",
		Service: service,
		File:    file,
		Meta:    map[string]string{"scope": "script"},
	}
}

// TestLinkShellInvocationEdges_TwoInvocations is SH1's pinned fixture:
// deploy.sh sources lib.sh and runs `bash migrate.sh` — 2 calls edges with
// via=exec, resolved to the right file nodes.
func TestLinkShellInvocationEdges_TwoInvocations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	libFile := filepath.Join(dir, "lib.sh")
	mustWrite(t, libFile, "helper() {\n  echo helping\n}\n")

	migrateFile := filepath.Join(dir, "migrate.sh")
	mustWrite(t, migrateFile, "echo migrating\n")

	deployFile := filepath.Join(dir, "deploy.sh")
	mustWrite(t, deployFile, "source lib.sh\nbash migrate.sh\n")

	nodes := []graph.Node{
		makeScriptNode("svc", deployFile),
		makeScriptNode("svc", libFile),
		makeScriptNode("svc", migrateFile),
	}
	svcFiles := map[string][]string{"svc": {deployFile, libFile, migrateFile}}

	edges, unresolved := LinkShellInvocationEdges(nodes, svcFiles)

	if len(unresolved) != 0 {
		t.Errorf("expected no unresolved entries, got %+v", unresolved)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 calls edges, got %d: %+v", len(edges), edges)
	}

	wantTargets := map[string]bool{
		scriptNodeIDFor("svc", libFile):     false,
		scriptNodeIDFor("svc", migrateFile): false,
	}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeCalls {
			t.Errorf("edge type = %q, want calls", e.Type)
		}
		if e.From != scriptNodeIDFor("svc", deployFile) {
			t.Errorf("edge from = %q, want deploy.sh's (script) node", e.From)
		}
		if e.Meta["via"] != "exec" {
			t.Errorf("meta via = %q, want exec", e.Meta["via"])
		}
		if e.Confidence != graph.ConfidenceStatic {
			t.Errorf("confidence = %q, want static", e.Confidence)
		}
		if _, ok := wantTargets[e.To]; !ok {
			t.Errorf("unexpected edge target %q", e.To)
		}
		wantTargets[e.To] = true
	}
	for id, seen := range wantTargets {
		if !seen {
			t.Errorf("missing edge to %q", id)
		}
	}
}

// TestLinkShellInvocationEdges_DynamicPathNegative verifies a variable-built
// invocation target never resolves to a guessed edge — the linker skips it
// entirely (internal/parser/shell.go's own Parse pass is what ledgers it,
// per-file, as shell_invocation_dynamic; this test only proves the linker
// side never fabricates an edge for it).
func TestLinkShellInvocationEdges_DynamicPathNegative(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	seedFile := filepath.Join(dir, "seed.sh")
	mustWrite(t, seedFile, "echo seeding\n")

	deployFile := filepath.Join(dir, "deploy.sh")
	mustWrite(t, deployFile, "ENV=prod\n./scripts/$ENV/seed.sh\n")

	nodes := []graph.Node{
		makeScriptNode("svc", deployFile),
		makeScriptNode("svc", seedFile),
	}
	svcFiles := map[string][]string{"svc": {deployFile, seedFile}}

	edges, unresolved := LinkShellInvocationEdges(nodes, svcFiles)
	if len(edges) != 0 {
		t.Errorf("dynamic-path invocation must never guess an edge, got %+v", edges)
	}
	if len(unresolved) != 0 {
		t.Errorf("the linker pass must not re-ledger a dynamic target (shell.go already did), got %+v", unresolved)
	}
}

// TestLinkShellInvocationEdges_BinaryUnresolved: a literal invocation target
// that resolves to neither a relative-to-invoker nor a service-root file
// ledgers as shell_binary_unresolved (SH1's explicit descope for workspace
// binaries / genuinely external scripts) instead of being silently dropped.
func TestLinkShellInvocationEdges_BinaryUnresolved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	deployFile := filepath.Join(dir, "deploy.sh")
	mustWrite(t, deployFile, "./bin/myservice migrate\n")

	nodes := []graph.Node{makeScriptNode("svc", deployFile)}
	svcFiles := map[string][]string{"svc": {deployFile}}

	edges, unresolved := LinkShellInvocationEdges(nodes, svcFiles)
	if len(edges) != 0 {
		t.Errorf("unresolved binary invocation must not produce a guessed edge, got %+v", edges)
	}
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 shell_binary_unresolved entry, got %d: %+v", len(unresolved), unresolved)
	}
	if unresolved[0].Kind != "shell_binary_unresolved" {
		t.Errorf("unresolved.Kind = %q, want shell_binary_unresolved", unresolved[0].Kind)
	}
}

// TestLinkShellInvocationEdges_TwoRunDeterminism verifies byte-identical
// output across two runs on the same input (bug-class rule 2 — this pass
// touches cross-file resolution).
func TestLinkShellInvocationEdges_TwoRunDeterminism(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	libFile := filepath.Join(dir, "lib.sh")
	mustWrite(t, libFile, "helper() {\n  echo helping\n}\n")
	migrateFile := filepath.Join(dir, "migrate.sh")
	mustWrite(t, migrateFile, "echo migrating\n")
	deployFile := filepath.Join(dir, "deploy.sh")
	mustWrite(t, deployFile, "source lib.sh\nbash migrate.sh\n./bin/other\n")

	nodes := []graph.Node{
		makeScriptNode("svcA", deployFile),
		makeScriptNode("svcA", libFile),
		makeScriptNode("svcA", migrateFile),
		makeScriptNode("svcB", deployFile),
		makeScriptNode("svcB", libFile),
		makeScriptNode("svcB", migrateFile),
	}
	svcFiles := map[string][]string{
		"svcA": {deployFile, libFile, migrateFile},
		"svcB": {deployFile, libFile, migrateFile},
	}

	edges1, unresolved1 := LinkShellInvocationEdges(nodes, svcFiles)
	edges2, unresolved2 := LinkShellInvocationEdges(nodes, svcFiles)

	if len(edges1) != len(edges2) {
		t.Fatalf("edge count differs across runs: %d vs %d", len(edges1), len(edges2))
	}
	for i := range edges1 {
		if edges1[i].ID != edges2[i].ID {
			t.Errorf("edge order/content differs at index %d: %q vs %q", i, edges1[i].ID, edges2[i].ID)
		}
	}
	if len(unresolved1) != len(unresolved2) {
		t.Fatalf("unresolved count differs across runs: %d vs %d", len(unresolved1), len(unresolved2))
	}
	for i := range unresolved1 {
		if unresolved1[i] != unresolved2[i] {
			t.Errorf("unresolved entry differs at index %d: %+v vs %+v", i, unresolved1[i], unresolved2[i])
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
