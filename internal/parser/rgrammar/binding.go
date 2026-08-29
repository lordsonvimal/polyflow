// Package rgrammar vendors the tree-sitter-r grammar (github.com/r-lib/tree-sitter-r,
// MIT, see LICENSE in this directory) directly, rather than depending on its Go
// module: r-lib/tree-sitter-r's own bindings/go package targets the newer
// tree-sitter/go-tree-sitter API family, while this repo is pinned to
// smacker/go-tree-sitter (an older, cgo-compatible but distinct API) for every
// other language. Both expose the grammar as a bare `unsafe.Pointer` to the
// generated TSLanguage struct, so the two are interoperable at that seam —
// smacker's own per-language packages (ruby/, php/, ...) wrap their vendored
// parser.c the same way this file does: parser.c/scanner.c sit as ordinary
// *.c files in this package directory (Go's build tool auto-compiles every
// .c file it finds there), and this preamble only forward-declares the
// generated entry point — it must NOT #include parser.c/scanner.c here too,
// or the symbols get compiled twice (once via Go's automatic .c pickup, once
// via the textual include) and the final link fails with "duplicate symbol"
// for every non-static global the external scanner defines (SCOPE_*, etc).
// Confirmed compatible: r-lib's grammar reports LANGUAGE_VERSION 14, matching
// smacker's TREE_SITTER_LANGUAGE_VERSION exactly (api.h) — no ABI mismatch.
package rgrammar

//#include "tree_sitter/parser.h"
//TSLanguage *tree_sitter_r(void);
import "C"

import (
	"unsafe"

	sitter "github.com/smacker/go-tree-sitter"
)

// GetLanguage returns the tree-sitter Language for R, in the shape every
// other language package in this repo (ruby.GetLanguage, php.GetLanguage, …)
// already returns — a drop-in for parser.ForFile's grammar registry.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(unsafe.Pointer(C.tree_sitter_r()))
}
