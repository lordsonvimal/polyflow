package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// initGitRepoForDiff creates a one-commit git repo at dir with a single
// committed file (user.go), mirroring gitdiff's own test helper — kept
// local to avoid an internal/gitdiff/testing-only export.
func initGitRepoForDiff(t *testing.T, dir string, initialLines string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "user.go"), []byte(initialLines), 0o644); err != nil {
		t.Fatalf("write user.go: %v", err)
	}
	run("add", "user.go")
	run("commit", "-q", "-m", "init")
}

// buildTestServerForDiff writes a two-service workspace config (one a real
// git repo, one not) and wires a server whose index has a node whose File
// exactly matches the git-repo service's changed file, so the diff endpoint
// maps the hunk to a real node.
func buildTestServerForDiff(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	svcDir := filepath.Join(dir, "svc")
	if err := os.Mkdir(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	initGitRepoForDiff(t, svcDir, "package svc\n\nfunc Handler() {\n\told()\n}\n")

	noGitDir := filepath.Join(dir, "nogit")
	if err := os.Mkdir(noGitDir, 0o755); err != nil {
		t.Fatalf("mkdir nogit: %v", err)
	}

	// Modify the tracked file so `git diff` against HEAD reports a hunk.
	if err := os.WriteFile(filepath.Join(svcDir, "user.go"), []byte("package svc\n\nfunc Handler() {\n\tnewCode()\n}\n"), 0o644); err != nil {
		t.Fatalf("modify user.go: %v", err)
	}

	cfgYAML := "name: test-ws\nversion: \"1\"\nservices:\n  - name: svc\n    path: svc\n    language: go\n  - name: nogit\n    path: nogit\n    language: go\n"
	cfgPath := filepath.Join(dir, "polyflow.yml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// gitdiff.Root resolves the repo root via `git rev-parse --show-toplevel`,
	// which canonicalizes symlinks (e.g. macOS's /tmp -> /private/tmp) —
	// resolve svcDir the same way so the node's File matches exactly.
	resolvedSvcDir, err := filepath.EvalSymlinks(svcDir)
	if err != nil {
		t.Fatalf("resolve svcDir: %v", err)
	}
	nodeFile := filepath.Join(resolvedSvcDir, "user.go")
	nodes := []*graph.Node{
		{ID: "handler", Type: graph.NodeTypeFunction, Label: "Handler", Service: "svc", File: nodeFile, Line: 3, Language: "go"},
		{ID: "caller", Type: graph.NodeTypeFunction, Label: "Caller", Service: "svc", File: nodeFile, Line: 20, Language: "go"},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: "caller", To: "handler", Type: graph.EdgeTypeCalls},
	}

	srv := buildTestServer(t, nodes, edges)
	srv.SetConfigPath(cfgPath)
	return srv, dir
}

func TestHandleImpactDiff_MapsHunkAndUnmapped(t *testing.T) {
	srv, _ := buildTestServerForDiff(t)

	req := httptest.NewRequest("GET", "/api/impact/diff", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	var out impact.DiffResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.Mode != "worktree" {
		t.Errorf("mode = %q, want worktree", out.Mode)
	}
	if out.Unmapped == nil {
		t.Fatal("unmapped_hunks must never be nil (rule 12: exhaustive intake)")
	}
	found := false
	for _, u := range out.Unmapped {
		if u.Reason == "no_git_repo: service path is not inside a git repository" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a no_git_repo unmapped entry for the 'nogit' service, got: %+v", out.Unmapped)
	}

	if len(out.Targets) != 1 || out.Targets[0].Node.ID != "handler" {
		t.Fatalf("expected the changed hunk to map to node 'handler', got targets: %+v", out.Targets)
	}
	if len(out.Callers) != 1 || out.Callers[0].ID != "caller" {
		t.Fatalf("expected the union blast radius to include 'caller', got: %+v", out.Callers)
	}
}

func TestHandleImpactDiff_Staged(t *testing.T) {
	srv, _ := buildTestServerForDiff(t)

	req := httptest.NewRequest("GET", "/api/impact/diff?staged=true", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}
	var out impact.DiffResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Mode != "staged" {
		t.Errorf("mode = %q, want staged", out.Mode)
	}
	// Nothing is staged in this fixture (the change is only in the worktree),
	// so no target should map — but unmapped_hunks must still surface the
	// no_git_repo entry rather than coming back empty.
	if len(out.Targets) != 0 {
		t.Errorf("expected no staged targets, got: %+v", out.Targets)
	}
}
