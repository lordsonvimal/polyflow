// Package railsview reads the Rails view layer as text: the ERB tag split, the
// Ruby argument-list primitives the view helpers need, and the `render` /
// `react_component` scanners Tier K.2 resolves into graph edges.
//
// It is a leaf package on purpose. internal/linker must never import
// internal/parser (parser's in-package tests already import linker), so every
// pass that has to *read source* during linking needs its scanners to live
// below both — the same constraint that produced internal/css (K.5) and
// internal/sprockets (K.3).
package railsview

import "bytes"

// SplitERB produces two same-length views of an ERB template:
//
//   - blankedHTML: src with every ERB tag (delimiters included) replaced by
//     spaces, so HTML patterns see only static markup.
//   - virtualRuby: src with everything *outside* ERB tags replaced by spaces,
//     so Ruby patterns see only embedded code.
//
// Newlines are preserved in both, and every surviving byte keeps its original
// offset, so line numbers from either view refer to the real file.
//
// Comment tags (`<%# ... %>`) are blanked out of virtualRuby entirely. Their
// body is dead text — orion has commented-out `render` and asset helpers that
// name templates the page no longer loads — and leaving it live would mint
// edges production does not have.
func SplitERB(src []byte) (blankedHTML, virtualRuby []byte) {
	blankedHTML = bytes.Clone(src)
	virtualRuby = bytes.Clone(src)

	i := 0
	for i < len(src) {
		if i+1 < len(src) && src[i] == '<' && src[i+1] == '%' {
			tagStart := i
			// Scan for closing %>
			j := i + 2
			for j+1 < len(src) && !(src[j] == '%' && src[j+1] == '>') {
				j++
			}
			var tagEnd int
			if j+1 < len(src) {
				tagEnd = j + 2
			} else {
				tagEnd = len(src) // unclosed tag: consume to end
			}

			// blankedHTML: blank the entire tag but preserve newlines.
			for k := tagStart; k < tagEnd; k++ {
				if blankedHTML[k] != '\n' {
					blankedHTML[k] = ' '
				}
			}

			// virtualRuby: blank delimiters (<%, %>) and any modifier char
			// (=, -) immediately after <%; keep the inner Ruby content.
			if tagStart < len(virtualRuby) {
				virtualRuby[tagStart] = ' ' // <
			}
			if tagStart+1 < len(virtualRuby) {
				virtualRuby[tagStart+1] = ' ' // %
			}
			inner := tagStart + 2
			if inner < len(src) {
				switch src[inner] {
				case '#':
					for k := inner; k < tagEnd; k++ {
						if virtualRuby[k] != '\n' {
							virtualRuby[k] = ' '
						}
					}
				case '=', '-':
					virtualRuby[inner] = ' '
				}
			}
			// Blank %> (and optional leading - before it)
			if j > tagStart+2 && src[j-1] == '-' {
				virtualRuby[j-1] = ' '
			}
			if j < len(virtualRuby) {
				virtualRuby[j] = ' ' // %
			}
			if j+1 < len(virtualRuby) {
				virtualRuby[j+1] = ' ' // >
			}

			i = tagEnd
		} else {
			// Non-ERB byte: blank in virtualRuby but keep newlines.
			if src[i] != '\n' {
				virtualRuby[i] = ' '
			}
			i++
		}
	}
	return blankedHTML, virtualRuby
}
