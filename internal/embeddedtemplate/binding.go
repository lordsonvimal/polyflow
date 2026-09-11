// Package embeddedtemplate adapts the upstream tree-sitter-embedded-template
// grammar (ERB/EJS-style <% %> templates) to smacker/go-tree-sitter's
// *sitter.Language, the type every other grammar in internal/patterns'
// languageFor dispatch already returns. Used to parse Rails `.erb` view files
// (Tier FX resolve_path step 3 — sprockets_assets' javascript_include_tag /
// stylesheet_link_tag half).
//
// No smacker/go-tree-sitter subpackage ships this grammar (see its language
// list), so this is a real module dependency —
// github.com/tree-sitter/tree-sitter-embedded-template, the grammar's own
// repo — rather than vendored parser.c/parser.h; `go get -u` picks up
// upstream grammar updates the normal way. Its bindings/go package exposes
// Language() unsafe.Pointer with no opinion on which Go sitter wrapper calls
// it, which is exactly the seam sitter.NewLanguage needs.
package embeddedtemplate

import (
	sitter "github.com/smacker/go-tree-sitter"
	tsembeddedtemplate "github.com/tree-sitter/tree-sitter-embedded-template/bindings/go"
)

// GetLanguage returns the embedded_template tree-sitter Language.
func GetLanguage() *sitter.Language {
	return sitter.NewLanguage(tsembeddedtemplate.Language())
}
