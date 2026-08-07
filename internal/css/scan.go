// Package css implements a deliberately small SCSS/CSS scanner.
//
// It reads exactly three things — `@import`/`@use`/`@forward` specifiers,
// top-level class/id selectors, and `@font-face` sources — and nothing else.
// That narrowness is the point (Tier K.5, docs/rails-asset-erb-coverage-plan.md):
// nextGen ships 145 stylesheets, and a scanner that also minted nodes for
// variables, mixins, functions and nested descendant selectors would push real
// results out of search ranking, exactly as the generated-templ `variable` nodes
// did before commit d07e911.
//
// A hand-written scanner rather than tree-sitter: the vendored grammar set has
// `css` but no `scss`, and SCSS's `@use`, `$vars`, `#{}` interpolation and
// nested rules all parse to ERROR nodes under the CSS grammar.
package css

import "strings"

// Import is one specifier of an `@import`/`@use`/`@forward` rule. A single rule
// may list several comma-separated specifiers; each becomes its own Import.
type Import struct {
	Rule string // "import", "use" or "forward"
	Spec string // the raw specifier, e.g. "settings/colors" or "modules/*"
	Line int
}

// Selector is a class or id selector that is the subject of a top-level rule.
type Selector struct {
	Kind string // "class" or "id"
	Name string // without the leading . or #
	Line int
}

// Text renders the selector back to its CSS form (".btn", "#study-submit").
func (s Selector) Text() string {
	if s.Kind == "id" {
		return "#" + s.Name
	}
	return "." + s.Name
}

// FontSource is one `url(...)`/`font-url(...)` argument of a `@font-face`
// `src:` declaration. Dynamic marks a specifier containing SCSS interpolation,
// whose value cannot be known statically — the caller ledgers those rather than
// inventing a target (phases.md #12).
type FontSource struct {
	Family  string
	URL     string
	Line    int
	Dynamic bool
}

// Result is everything one scan of a stylesheet yields.
type Result struct {
	Imports     []Import
	Selectors   []Selector
	FontSources []FontSource
}

// interpolationMark replaces a `#{...}` span so the walker never mistakes the
// interpolation's braces for a block, while keeping the span visible as unknown.
const interpolationMark = "*"

// Scan walks src once and returns the three extractions. Output order follows
// source order, so repeated scans of the same bytes are byte-identical
// (bug-class #2).
func Scan(src []byte) Result {
	s := &scanner{src: string(src), line: 1}
	s.run()
	return s.out
}

type scanner struct {
	src  string
	i    int
	line int

	buf     strings.Builder // the prelude/declaration text accumulated so far
	bufLine int             // line on which buf's first non-space character sat

	ruleDepth int      // enclosing *style rule* blocks (at-rules don't count)
	blocks    []string // stack of block kinds: "rule", "font-face", "at"

	family string // font-family of the innermost @font-face block

	out Result
}

func (s *scanner) run() {
	for s.i < len(s.src) {
		c := s.src[s.i]
		switch {
		case c == '\n':
			s.line++
			s.push(' ')
			s.i++
		case c == '"' || c == '\'':
			s.readString()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '#' && s.peek(1) == '{':
			s.skipInterpolation()
		case c == '{':
			s.openBlock()
		case c == '}':
			s.closeBlock()
		case c == ';':
			s.endDeclaration()
			s.i++
		default:
			s.push(c)
			s.i++
		}
	}
	// A trailing declaration with no `;` (legal at end of a block/file).
	s.endDeclaration()
}

func (s *scanner) peek(n int) byte {
	if s.i+n >= len(s.src) {
		return 0
	}
	return s.src[s.i+n]
}

func (s *scanner) push(c byte) {
	if s.buf.Len() == 0 {
		if c == ' ' || c == '\t' {
			return // don't let leading whitespace claim bufLine
		}
		s.bufLine = s.line
	}
	s.buf.WriteByte(c)
}

func (s *scanner) pushString(str string) {
	for i := 0; i < len(str); i++ {
		s.push(str[i])
	}
}

func (s *scanner) takeBuf() (string, int) {
	text := strings.TrimSpace(s.buf.String())
	line := s.bufLine
	s.buf.Reset()
	s.bufLine = 0
	return text, line
}

// readString copies a quoted string into buf verbatim, quotes included, so that
// a `{`, `}`, `;` or `//` inside a URL never reaches the block walker.
func (s *scanner) readString() {
	quote := s.src[s.i]
	s.push(quote)
	s.i++
	for s.i < len(s.src) {
		c := s.src[s.i]
		if c == '\\' && s.i+1 < len(s.src) {
			s.push(c)
			s.push(s.src[s.i+1])
			s.i += 2
			continue
		}
		if c == '\n' {
			s.line++
		}
		s.push(c)
		s.i++
		if c == quote {
			return
		}
	}
}

func (s *scanner) skipBlockComment() {
	end := strings.Index(s.src[s.i+2:], "*/")
	stop := len(s.src)
	if end >= 0 {
		stop = s.i + 2 + end + 2
	}
	s.line += strings.Count(s.src[s.i:stop], "\n")
	s.i = stop
}

// skipLineComment drops a `//` comment. `//` is not a CSS comment, but plain
// `.css` files in this corpus never use protocol-relative URLs outside a
// string, and readString has already consumed anything quoted.
func (s *scanner) skipLineComment() {
	end := strings.IndexByte(s.src[s.i:], '\n')
	if end < 0 {
		s.i = len(s.src)
		return
	}
	s.i += end // leave the newline for run() to count
}

// skipInterpolation consumes `#{...}` (brace-balanced) and leaves a wildcard in
// its place, so `.icon-#{$name}` neither opens a block nor reads as a literal.
func (s *scanner) skipInterpolation() {
	depth := 0
	j := s.i + 1
	for ; j < len(s.src); j++ {
		switch s.src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				j++
				goto done
			}
		case '\n':
			s.line++
		}
	}
done:
	s.i = j
	s.pushString(interpolationMark)
}

func (s *scanner) openBlock() {
	prelude, line := s.takeBuf()
	s.i++

	kind := "rule"
	if strings.HasPrefix(prelude, "@") {
		name := atRuleName(prelude)
		if name == "font-face" {
			kind = "font-face"
			s.family = ""
		} else {
			kind = "at"
		}
	} else if s.ruleDepth == 0 && prelude != "" {
		// Only top-level rules mint selectors. `@media`/`@supports` wrappers
		// don't nest a rule, so a selector inside one is still top-level.
		for _, sel := range subjectSelectors(prelude, line) {
			s.out.Selectors = append(s.out.Selectors, sel)
		}
	}
	if kind == "rule" {
		s.ruleDepth++
	}
	s.blocks = append(s.blocks, kind)
}

func (s *scanner) closeBlock() {
	s.endDeclaration()
	s.i++
	if len(s.blocks) == 0 {
		return
	}
	kind := s.blocks[len(s.blocks)-1]
	s.blocks = s.blocks[:len(s.blocks)-1]
	if kind == "rule" && s.ruleDepth > 0 {
		s.ruleDepth--
	}
	if kind == "font-face" {
		s.family = ""
	}
}

// endDeclaration handles the text terminated by `;` (or by a block close):
// at-rule imports anywhere, and `font-family`/`src` inside a @font-face block.
func (s *scanner) endDeclaration() {
	decl, line := s.takeBuf()
	if decl == "" {
		return
	}
	if strings.HasPrefix(decl, "@") {
		switch rule := atRuleName(decl); rule {
		case "import", "use", "forward":
			for _, spec := range importSpecs(decl) {
				s.out.Imports = append(s.out.Imports, Import{Rule: rule, Spec: spec, Line: line})
			}
		}
		return
	}
	if len(s.blocks) == 0 || s.blocks[len(s.blocks)-1] != "font-face" {
		return
	}
	prop, value, ok := splitDeclaration(decl)
	if !ok {
		return
	}
	switch prop {
	case "font-family":
		s.family = strings.Trim(strings.TrimSpace(value), `"'`)
	case "src":
		for _, u := range urlArgs(value) {
			s.out.FontSources = append(s.out.FontSources, FontSource{
				Family: s.family,
				URL:    u,
				Line:   line,
				// `#{...}` inside a quoted url() survives verbatim: readString
				// consumes the quote first, so the interpolation walker never
				// sees it. Both spellings mean "not knowable statically".
				Dynamic: strings.Contains(u, interpolationMark) || strings.Contains(u, "#{") ||
					strings.HasPrefix(u, "$"),
			})
		}
	}
}

// atRuleName returns the lowercase at-rule keyword of "@font-face {" / "@import
// 'x'" — the run of name characters after the '@'.
func atRuleName(text string) string {
	if !strings.HasPrefix(text, "@") {
		return ""
	}
	end := 1
	for end < len(text) && (isNameByte(text[end])) {
		end++
	}
	return strings.ToLower(text[1:end])
}

func isNameByte(c byte) bool {
	return c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// splitDeclaration splits "prop: value" on the first colon.
func splitDeclaration(decl string) (prop, value string, ok bool) {
	i := strings.IndexByte(decl, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(decl[:i])), strings.TrimSpace(decl[i+1:]), true
}

// importSpecs pulls every quoted specifier out of an @import/@use/@forward
// rule, ignoring `as`/`with` clauses and media-query suffixes. Unquoted
// `url(...)` forms are read too; anything else (a bare variable) yields nothing.
func importSpecs(decl string) []string {
	var out []string
	for _, q := range quotedStrings(decl) {
		out = append(out, q)
	}
	if len(out) == 0 {
		out = append(out, urlArgs(decl)...)
	}
	return out
}

// quotedStrings returns the contents of every quoted run in text, in order.
func quotedStrings(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c != '"' && c != '\'' {
			continue
		}
		j := i + 1
		for j < len(text) && text[j] != c {
			if text[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(text) {
			break
		}
		out = append(out, text[i+1:j])
		i = j
	}
	return out
}

// urlArgs returns the argument of every url(...)/font-url(...)/image-url(...)
// call in value, with surrounding quotes stripped.
func urlArgs(value string) []string {
	var out []string
	lower := strings.ToLower(value)
	for i := 0; i+4 <= len(lower); i++ {
		if lower[i:i+4] != "url(" {
			continue
		}
		// Require a call boundary: `font-url(`/`image-url(` count, `blurl(` doesn't.
		if i > 0 && isNameByte(lower[i-1]) && lower[i-1] != '-' {
			continue
		}
		close := strings.IndexByte(value[i+4:], ')')
		if close < 0 {
			break
		}
		arg := strings.TrimSpace(value[i+4 : i+4+close])
		arg = strings.Trim(arg, `"'`)
		if arg != "" {
			out = append(out, arg)
		}
		i += 4 + close
	}
	return out
}

// subjectSelectors returns the subject (rightmost simple class/id) of each
// comma-separated part of a rule prelude.
//
// The subject is what a `$(".btn")` lookup resolves to, and taking only the
// subject rather than every simple selector in the compound keeps the node
// count roughly a third lower without losing a join target.
func subjectSelectors(prelude string, line int) []Selector {
	var out []Selector
	seen := map[string]bool{}
	for _, part := range splitTopLevel(prelude, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kind, name, ok := lastSimpleSelector(part)
		if !ok {
			continue
		}
		key := kind + name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Selector{Kind: kind, Name: name, Line: line})
	}
	return out
}

// lastSimpleSelector scans part for `.name`/`#name` runs and returns the last
// one. A `.` or `#` inside brackets or parens (`[href=".x"]`, `:not(.y)`) is
// skipped: those qualify the subject, they aren't the subject.
func lastSimpleSelector(part string) (kind, name string, ok bool) {
	depth := 0
	for i := 0; i < len(part); i++ {
		switch c := part[i]; c {
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		case '.', '#':
			if depth > 0 {
				continue
			}
			// A preceding digit means this is the fractional part of a number
			// (`.size-1.5x`), not a new simple selector. A preceding letter is
			// a real chained selector (`.tab.is-active`).
			if i > 0 && part[i-1] >= '0' && part[i-1] <= '9' {
				continue
			}
			j := i + 1
			for j < len(part) && isNameByte(part[j]) {
				j++
			}
			if j == i+1 || !isSelectorStart(part[i+1]) {
				continue
			}
			// `.icon-#{$name}`: the name is only known at compile time, so it
			// can never match a lookup. It still counts as the subject, which
			// makes the whole part unusable rather than promoting its qualifier.
			if j < len(part) && part[j] == interpolationMark[0] {
				kind, name, ok = "", "", false
				i = j
				continue
			}
			kind, name, ok = "class", part[i+1:j], true
			if c == '#' {
				kind = "id"
			}
			i = j - 1
		}
	}
	return kind, name, ok
}

// isSelectorStart: a CSS identifier may not begin with a digit.
func isSelectorStart(c byte) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// splitTopLevel splits text on sep, ignoring separators inside (), [] or {}.
func splitTopLevel(text string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, text[start:i])
				start = i + 1
			}
		}
	}
	return append(out, text[start:])
}
