package railsview

import "strings"

// BlankERBDelimiters replaces an ERB tag's own `<%=` / `-%>` markers with
// spaces, keeping every other byte at its offset so line numbers and the
// argument parser both see plain Ruby. Without this the trailing `%>` reads as
// trailing junk after the last string literal and the whole call looks dynamic.
func BlankERBDelimiters(body string) string {
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

// LiteralSources splits a helper's argument list on top-level commas and takes
// the leading run of bare string literals.
//
// Collection stops at the first argument that is not a bare literal, because
// that is where the options hash begins — and its *values* are strings too.
// `stylesheet_link_tag 'application', media: 'all'` names one asset, not two,
// and `javascript_include_tag 'application', 'data-turbolinks-track' => true`
// names one, not two.
func LiteralSources(args string) (names []string, dynamic bool) {
	for _, part := range SplitTopLevel(args) {
		part = strings.TrimSpace(part)
		if part == "" {
			break
		}
		lit, ok := BareStringLiteral(part)
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

// BareStringLiteral reports whether part is exactly one quoted string with no
// interpolation and nothing after it.
func BareStringLiteral(part string) (string, bool) {
	lit, rest, ok := LeadingLiteral(part)
	if !ok || strings.TrimSpace(rest) != "" {
		return "", false // `'x' => true`: a hash key, not a source
	}
	return lit, true
}

// LeadingLiteral returns the plain string literal an expression starts with,
// plus whatever follows it.
//
// Unlike BareStringLiteral it tolerates a trailing tail, because a Ruby call
// argument legitimately carries one: `render "shared/foo" if @user` names a
// partial, and rejecting it over the modifier would ledger a clue that is fully
// static. An interpolated literal is still refused — `"tab_#{kind}"` names a
// template this pass cannot know.
func LeadingLiteral(expr string) (lit, rest string, ok bool) {
	if len(expr) < 2 {
		return "", "", false
	}
	q := expr[0]
	if q != '\'' && q != '"' {
		return "", "", false
	}
	closing := strings.IndexByte(expr[1:], q)
	if closing < 0 {
		return "", "", false
	}
	inner := expr[1 : 1+closing]
	if inner == "" || strings.Contains(inner, "#{") {
		return "", "", false
	}
	return inner, expr[2+closing:], true
}

// SplitTopLevel splits on commas that are not inside quotes, brackets or
// braces, so a hash or array argument stays in one piece.
func SplitTopLevel(s string) []string {
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

// IsRubyNameByte reports whether c can appear inside a Ruby identifier, which
// is how a scanner tells `render` from `render_to_string` and
// `javascript_include_tag` from `custom_javascript_include_tag`.
func IsRubyNameByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// callSites yields the offset just past each standalone occurrence of name in
// s, skipping identifier-embedded matches (`render_to_string`), method calls on
// a receiver (`view.render`) and symbols (`:render`).
//
// A receiver match is skipped rather than resolved because `x.render` in a view
// is a component object's own method, not Rails' template render — resolving it
// against app/views would invent a template that is never loaded.
// Bytes flagged by mask (string-literal and comment interiors) are skipped.
func callSites(s, name string, mask []bool) []int {
	var out []int
	for i := 0; ; {
		rel := strings.Index(s[i:], name)
		if rel < 0 {
			return out
		}
		at := i + rel
		i = at + len(name)
		if mask[at] {
			continue
		}
		if at > 0 {
			prev := s[at-1]
			if IsRubyNameByte(prev) || prev == '.' || prev == ':' {
				continue
			}
		}
		if i < len(s) && (IsRubyNameByte(s[i]) || s[i] == '_') {
			continue
		}
		out = append(out, i)
	}
}

// callArgs reads the argument list of a Ruby call whose identifier ends at pos,
// split on top-level commas, and reports how many lines below the identifier
// the first argument starts.
//
// The window stops at the call's own closing paren when the call is
// parenthesised and at the end of the line when it is not. Both forms occur in
// orion's views — `react_component(` routinely puts its name on the next
// line, while `render "shared/foo"` is bare — and reading past either boundary
// picks up the *next* statement's literals.
func callArgs(s string, pos int) (parts []string, lineOffset int, ok bool) {
	skipSpace := func(i int) int {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			if s[i] == '\n' {
				lineOffset++
			}
			i++
		}
		return i
	}

	i := skipSpace(pos)
	paren := i < len(s) && s[i] == '('
	if paren {
		i = skipSpace(i + 1)
	}
	if i >= len(s) {
		return nil, 0, false
	}

	window := s[i:]
	if !paren {
		if nl := strings.IndexByte(window, '\n'); nl >= 0 {
			window = window[:nl]
		}
	}
	// SplitTopLevel returns at the first unmatched ')', which for a
	// parenthesised call is its own closing paren.
	parts = SplitTopLevel(window)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return nil, 0, false
	}
	return parts, lineOffset, true
}

// hasKeyword reports whether any argument is the named Ruby keyword argument,
// in either the modern (`collection:`) or hashrocket (`:collection =>`) spelling.
func hasKeyword(parts []string, name string) bool {
	for _, p := range parts {
		if _, ok := keywordArg(strings.TrimSpace(p), name); ok {
			return true
		}
	}
	return false
}

// keywordArg matches `name: <value>` or `:name => <value>` and returns the
// value expression.
func keywordArg(part, name string) (string, bool) {
	if rest, ok := strings.CutPrefix(part, name+":"); ok {
		return strings.TrimSpace(rest), true
	}
	if rest, ok := strings.CutPrefix(part, ":"+name); ok {
		if rest = strings.TrimSpace(rest); strings.HasPrefix(rest, "=>") {
			return strings.TrimSpace(rest[2:]), true
		}
	}
	return "", false
}
