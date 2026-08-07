// Package sprockets implements the two text scanners the Rails asset pipeline
// needs: the `//= require` directive header of an asset file, and the
// `javascript_include_tag` / `stylesheet_link_tag` calls in an ERB template
// (Tier K.3).
//
// Both are scanners rather than grammar passes. Sprockets directives live in
// *comments*, which no JS grammar surfaces, and the ERB helpers are read from
// the same hand-rolled tag split internal/parser/erb.go uses — duplicated here
// in a leaf package because internal/linker must not import internal/parser
// (parser's in-package tests already import linker).
package sprockets

import (
	"strings"
)

// Directive is one `= <verb> <args>` line from an asset file's header comment
// block. Ext is the optional extension filter of `link_directory <dir> .css`.
type Directive struct {
	Verb string
	Path string
	Ext  string
	Line int
}

// IncludeTag is one asset-helper call in an ERB template. Helper is
// "javascript_include_tag" or "stylesheet_link_tag"; Name is a single literal
// source argument (a call with several sources yields several IncludeTags).
// Dynamic reports a call whose first argument is not a plain string literal —
// those are ledgered, never guessed.
type IncludeTag struct {
	Helper  string
	Name    string
	Line    int
	Dynamic bool
}

// directiveVerbs are the ones that name another asset. `require_self` and
// `stub` are recognised but deliberately absent: neither adds a dependency
// (require_self reorders the file's own body, stub *removes* an asset), so
// treating them as an edge would assert a link that does not exist.
var directiveVerbs = map[string]bool{
	"require":           true,
	"require_tree":      true,
	"require_directory": true,
	"link":              true,
	"link_tree":         true,
	"link_directory":    true,
	"depend_on":         true,
	"depend_on_asset":   true,
}

// FanoutVerbs name a directory rather than a single asset.
func FanoutVerbs(verb string) (recursive, isDir bool) {
	switch verb {
	case "require_tree", "link_tree":
		return true, true
	case "require_directory", "link_directory":
		return false, true
	}
	return false, false
}

// ScanDirectives reads the leading comment block of an asset file and returns
// its directives.
//
// Only the header block is scanned, which is Sprockets' own rule and the reason
// this is safe to run over minified vendor bundles: a `//=` sequence in the
// body of a file is not a directive, and one deep inside a 200KB bundle would
// otherwise mint a phantom dependency. Blank lines do not end the header (asset
// manifests routinely group requires with blank lines); the first line of real
// code does.
func ScanDirectives(src []byte) []Directive {
	var out []Directive
	inBlockComment := false

	for i, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}

		body, comment := commentBody(line, &inBlockComment)
		if !comment {
			break // first line of code: the header block is over
		}
		body = strings.TrimSpace(body)
		if !strings.HasPrefix(body, "=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(body, "="))
		if len(fields) == 0 || !directiveVerbs[fields[0]] {
			continue
		}
		d := Directive{Verb: fields[0], Line: i + 1}
		if len(fields) > 1 {
			d.Path = strings.Trim(fields[1], `"'`)
		}
		if len(fields) > 2 && strings.HasPrefix(fields[2], ".") {
			d.Ext = fields[2]
		}
		if d.Path == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

// commentBody strips the comment markers from a line and reports whether the
// line was a comment at all. It tracks `/* ... */` across lines so the CSS
// directive spelling (`/*= require x */`, and the multi-line `*= require x`
// form Rails scaffolds) reads the same as the JS one.
func commentBody(line string, inBlock *bool) (string, bool) {
	if *inBlock {
		if idx := strings.Index(line, "*/"); idx >= 0 {
			*inBlock = false
			line = line[:idx]
		}
		return strings.TrimPrefix(strings.TrimSpace(line), "*"), true
	}
	switch {
	case strings.HasPrefix(line, "//"):
		return line[2:], true
	case strings.HasPrefix(line, "/*"):
		rest := line[2:]
		if idx := strings.Index(rest, "*/"); idx >= 0 {
			return rest[:idx], true
		}
		*inBlock = true
		return rest, true
	}
	return "", false
}

// assetHelpers are the Rails view helpers that bind a page to a compiled asset.
var assetHelpers = []string{"javascript_include_tag", "stylesheet_link_tag"}

// ScanIncludeTags returns every asset-helper call in an ERB template.
//
// Only live ERB tags are read. `<%# javascript_include_tag 'application' %>` is
// a *commented-out* helper — orion has three of them, all naming assets the
// page no longer loads — and reading one would assert a page→asset binding that
// production does not have.
func ScanIncludeTags(src []byte) []IncludeTag {
	var out []IncludeTag
	s := string(src)
	line := 1

	for i := 0; i < len(s); {
		if !strings.HasPrefix(s[i:], "<%") {
			if s[i] == '\n' {
				line++
			}
			i++
			continue
		}
		end := strings.Index(s[i:], "%>")
		if end < 0 {
			end = len(s) - i
		} else {
			end += 2
		}
		body := s[i : i+end]
		// `<%#` and `<%#=` are comment tags.
		if len(body) > 2 && body[2] == '#' {
			line += strings.Count(body, "\n")
			i += end
			continue
		}
		for n, tagLine := range strings.Split(blankDelimiters(body), "\n") {
			out = append(out, scanIncludeLine(tagLine, line+n)...)
		}
		line += strings.Count(body, "\n")
		i += end
	}
	return out
}

// blankDelimiters replaces an ERB tag's own `<%=`/`-%>` markers with spaces,
// keeping every other byte at its offset so line numbers and the argument
// parser both see plain Ruby. Without this the trailing `%>` reads as trailing
// junk after the last string literal and the whole call looks dynamic.
func blankDelimiters(body string) string {
	b := []byte(body)
	for i := 0; i < len(b) && i < 3; i++ {
		if b[i] == '<' || b[i] == '%' || b[i] == '=' || b[i] == '-' {
			b[i] = ' '
			continue
		}
		break
	}
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if b[i] == '>' || b[i] == '%' || b[i] == '-' {
			b[i] = ' '
			continue
		}
		break
	}
	return string(b)
}

// scanIncludeLine pulls the literal sources out of one line of Ruby. A helper
// call is read a line at a time: Rails' asset helpers take their sources first
// and their HTML options last, and every real call fits on its line even when
// the surrounding `<%= if ... end %>` does not.
func scanIncludeLine(line string, lineNo int) []IncludeTag {
	var out []IncludeTag
	for _, helper := range assetHelpers {
		idx := 0
		for {
			rel := strings.Index(line[idx:], helper)
			if rel < 0 {
				break
			}
			at := idx + rel
			idx = at + len(helper)
			if at > 0 && isRubyNameByte(line[at-1]) {
				continue // `custom_javascript_include_tag`
			}
			args := strings.TrimSpace(line[idx:])
			args = strings.TrimPrefix(args, "(")
			names, dynamic := literalSources(args)
			if dynamic {
				out = append(out, IncludeTag{Helper: helper, Name: strings.TrimSpace(args), Line: lineNo, Dynamic: true})
				continue
			}
			for _, n := range names {
				out = append(out, IncludeTag{Helper: helper, Name: n, Line: lineNo})
			}
		}
	}
	return out
}

// literalSources splits a helper's argument list on top-level commas and takes
// the leading run of bare string literals.
//
// Collection stops at the first argument that is not a bare literal, because
// that is where the options hash begins — and its *values* are strings too.
// `stylesheet_link_tag 'application', media: 'all'` names one asset, not two,
// and `javascript_include_tag 'application', 'data-turbolinks-track' => true`
// names one, not two.
func literalSources(args string) (names []string, dynamic bool) {
	for _, part := range splitTopLevel(args) {
		part = strings.TrimSpace(part)
		if part == "" {
			break
		}
		lit, ok := bareStringLiteral(part)
		if !ok {
			if len(names) == 0 {
				// Nothing literal at all: `javascript_include_tag @asset` or an
				// interpolated path. Ledger it (phases.md #12).
				return nil, true
			}
			break
		}
		names = append(names, lit)
	}
	return names, false
}

// bareStringLiteral reports whether part is exactly one quoted string with no
// interpolation and nothing after it.
func bareStringLiteral(part string) (string, bool) {
	if len(part) < 2 {
		return "", false
	}
	q := part[0]
	if q != '\'' && q != '"' {
		return "", false
	}
	closing := strings.IndexByte(part[1:], q)
	if closing < 0 {
		return "", false
	}
	inner := part[1 : 1+closing]
	if strings.TrimSpace(part[2+closing:]) != "" {
		return "", false // `'x' => true`: a hash key, not a source
	}
	if strings.Contains(inner, "#{") || inner == "" {
		return "", false
	}
	return inner, true
}

// splitTopLevel splits on commas that are not inside quotes, brackets or
// braces, so a hash or array argument stays in one piece.
func splitTopLevel(s string) []string {
	var parts []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote && (i == 0 || s[i-1] != '\\') {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return append(parts, s[start:i]) // call's own closing paren
			}
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func isRubyNameByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
