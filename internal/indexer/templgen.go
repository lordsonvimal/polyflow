package indexer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// templStatus is the outcome of a templ-codegen preflight for one service.
type templStatus struct {
	templFiles int    // .templ source files found under the service
	needed     int    // those whose generated _templ.go is missing or stale
	ran        bool   // whether `templ generate` was actually invoked
	note       string // human-readable log line ("" when there is nothing to say)
}

// ensureTemplGenerated regenerates missing or stale templ code under svcDir so the
// Go semantic pass (go/packages) can compile the service and produce a real call
// graph, instead of failing on undefined `_templ.go` symbols and silently falling
// back to tree-sitter — the degradation that made impact/trace on templ-based web
// services (svc-c-mgr, svc-b) return partial blast radii.
//
// It is best-effort: `templ generate` runs only when (a) the service actually has
// .templ files whose generated sibling is missing or older than the source, and
// (b) the templ binary is on PATH. When codegen is needed but templ is not
// installed it returns an actionable note instead of running anything. Any note is
// meant to be logged by the caller; a service with no .templ files yields a zero
// templStatus with an empty note.
func ensureTemplGenerated(svcDir string) templStatus {
	var st templStatus
	_ = filepath.WalkDir(svcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipTemplWalkDir(d.Name()) && path != svcDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".templ") {
			return nil
		}
		st.templFiles++
		gen := strings.TrimSuffix(path, ".templ") + "_templ.go"
		if templGenStale(path, gen) {
			st.needed++
		}
		return nil
	})
	if st.templFiles == 0 || st.needed == 0 {
		return st // no templ sources, or all generated code already up to date
	}

	templBin, err := exec.LookPath("templ")
	if err != nil {
		st.note = fmt.Sprintf(
			"%d .templ file(s) need codegen but `templ` is not on PATH — Go call graph falls back to "+
				"tree-sitter; install it (go install github.com/a-h/templ/cmd/templ@latest) and re-index",
			st.needed)
		return st
	}

	cmd := exec.Command(templBin, "generate")
	cmd.Dir = svcDir
	out, err := cmd.CombinedOutput()
	st.ran = true
	if err != nil {
		st.note = fmt.Sprintf("templ generate failed in %s: %v — Go call graph falls back to tree-sitter\n%s",
			svcDir, err, strings.TrimSpace(string(out)))
		return st
	}
	st.note = fmt.Sprintf("templ generate: regenerated code for %d stale/missing .templ file(s)", st.needed)
	return st
}

// templGenStale reports whether the generated file for a .templ source is missing
// or older than the source (and therefore needs regeneration).
func templGenStale(templPath, genPath string) bool {
	gi, err := os.Stat(genPath)
	if err != nil {
		return true // generated sibling missing
	}
	ti, err := os.Stat(templPath)
	if err != nil {
		return false // source vanished mid-walk; nothing to regenerate
	}
	return ti.ModTime().After(gi.ModTime())
}

// skipTemplWalkDir reports whether a directory should be pruned from the templ
// scan — vendored/third-party trees never carry a project's .templ sources.
func skipTemplWalkDir(name string) bool {
	switch name {
	case "node_modules", "vendor", ".git", "testdata":
		return true
	}
	return false
}
