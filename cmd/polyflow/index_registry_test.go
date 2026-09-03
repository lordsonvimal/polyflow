package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// TestRunIndex_MultiServiceWorkspace_RegistersUnderWorkspaceName is GR.1's
// self-registration fix: a standalone workspace with more than one internal
// service (e.g. a monorepo's own polyflow.yml listing several
// sub-components) still registers as one fleet-resolvable member, keyed by
// the workspace's own top-level `name:` — not cfg.Services[0].Name, which
// was only ever a correct proxy for that when a workspace happened to
// declare exactly one service.
func TestRunIndex_MultiServiceWorkspace_RegistersUnderWorkspaceName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)

	wsRoot := t.TempDir()
	polyflowYML := `
name: myrepo
version: "1"
services:
  - name: svc-a
    path: ./svc-a
    language: go
  - name: svc-b
    path: ./svc-b
    language: go
`
	if err := os.WriteFile(filepath.Join(wsRoot, meta.ConfigFile), []byte(polyflowYML), 0o644); err != nil {
		t.Fatalf("write polyflow.yml: %v", err)
	}
	for _, sub := range []string{"svc-a", "svc-b"} {
		dir := filepath.Join(wsRoot, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
	}

	t.Chdir(wsRoot)
	indexWorkspace = "polyflow.yml"
	indexNoEmbed = true
	indexFull = false
	indexWorkers = 2
	t.Cleanup(func() {
		indexWorkspace = "polyflow.yml"
		indexNoEmbed = false
		indexFull = false
		indexWorkers = 0
	})

	if err := runIndex(indexCmd, nil); err != nil {
		t.Fatalf("runIndex: %v", err)
	}

	regPath, err := registry.DefaultPath()
	if err != nil {
		t.Fatalf("registry.DefaultPath: %v", err)
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	if _, ok := reg.Lookup("myrepo"); !ok {
		t.Fatalf("expected registry entry for workspace name %q, got entries: %+v", "myrepo", reg.Entries)
	}
	if _, ok := reg.Lookup("svc-a"); ok {
		t.Fatalf("did not expect a registry entry for internal service name %q", "svc-a")
	}
}

// TestRunIndex_AllRejectsServiceArg guards the `polyflow index --all` +
// positional-service combination — the two are mutually exclusive since
// --all fans out over the whole machine registry, not one workspace.
func TestRunIndex_AllRejectsServiceArg(t *testing.T) {
	indexAll = true
	t.Cleanup(func() { indexAll = false })

	err := runIndex(indexCmd, []string{"some-service"})
	if err == nil {
		t.Fatal("expected error when --all is combined with a service argument")
	}
}
