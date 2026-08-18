package indexer

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTemplGenStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "page.templ")
	gen := filepath.Join(dir, "page_templ.go")

	if err := os.WriteFile(src, []byte("templ Page() {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Generated file missing → stale.
	if !templGenStale(src, gen) {
		t.Fatal("missing generated file should be stale")
	}

	// Generated newer than source → fresh.
	if err := os.WriteFile(gen, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(gen, future, future); err != nil {
		t.Fatal(err)
	}
	if templGenStale(src, gen) {
		t.Fatal("generated file newer than source should be fresh")
	}

	// Source edited after generation → stale again.
	newer := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(src, newer, newer); err != nil {
		t.Fatal(err)
	}
	if !templGenStale(src, gen) {
		t.Fatal("source newer than generated should be stale")
	}
}

// TestEnsureTemplGenerated_NoTemplFiles: a service with no .templ sources is a
// no-op with an empty note (no spurious warnings on non-templ Go services).
func TestEnsureTemplGenerated_NoTemplFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := ensureTemplGenerated(dir)
	if st.templFiles != 0 || st.needed != 0 || st.ran || st.note != "" {
		t.Fatalf("expected zero status for non-templ service, got %+v", st)
	}
}

// TestEnsureTemplGenerated_UpToDate: .templ files whose generated siblings are
// fresh need no codegen and produce no note.
func TestEnsureTemplGenerated_UpToDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "page.templ")
	gen := filepath.Join(dir, "page_templ.go")
	if err := os.WriteFile(src, []byte("templ Page() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gen, []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(gen, future, future); err != nil {
		t.Fatal(err)
	}
	st := ensureTemplGenerated(dir)
	if st.templFiles != 1 {
		t.Fatalf("expected 1 templ file, got %d", st.templFiles)
	}
	if st.needed != 0 || st.ran || st.note != "" {
		t.Fatalf("expected no codegen needed, got %+v", st)
	}
}

// TestEnsureTemplGenerated_MissingBinaryActionableNote: when codegen is needed but
// templ is unavailable, the caller gets an actionable note rather than a silent
// fallback. Only meaningful when `templ` is not installed on the test machine.
func TestEnsureTemplGenerated_MissingBinaryActionableNote(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("templ"); err == nil {
		t.Skip("templ is installed; missing-binary path not exercised")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "page.templ"), []byte("templ Page() {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := ensureTemplGenerated(dir)
	if st.needed != 1 {
		t.Fatalf("expected 1 file needing codegen, got %d", st.needed)
	}
	if st.ran {
		t.Fatal("should not have run templ when binary is absent")
	}
	if st.note == "" {
		t.Fatal("expected an actionable note when templ is missing")
	}
}
