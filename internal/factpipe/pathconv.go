package factpipe

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// pathconv.go: hubs (hub.go) receive `files []string` straight from the
// indexer's raw absolute file-walk list — the same raw-path source
// internal/linker/file_nodes.go's fileNodeIndex.ensure documents needing to
// relativize before minting a NodeTypeFile node, so a minted node's ID/File
// matches the cwd-relative convention internal/parser's own nodes (and
// containment's pre-minted file nodes) already use. internal/patterns owns
// the canonical RelativizeToCwd, but internal/patterns imports internal/
// factpipe (FX.2's extract verbs), so factpipe cannot import it back without
// a cycle — this is a small, deliberately duplicated mirror, not a shared
// helper moved to a common package (out of scope for this fix).
var indexCwd = sync.OnceValue(func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
})

// relativizeToCwd mirrors internal/patterns.RelativizeToCwd exactly (see that
// function's doc comment for the rationale). Falls back to the input
// unchanged if it's not absolute, or resolves outside cwd.
func relativizeToCwd(file string) string {
	cwd := indexCwd()
	if cwd == "" || !filepath.IsAbs(file) {
		return file
	}
	canon := file
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		canon = resolved
	}
	rel, err := filepath.Rel(cwd, canon)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return file
	}
	return filepath.ToSlash(rel)
}
