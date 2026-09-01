package railsview

import "strings"

// LayoutDecl is one class-level `layout ...` declaration in a controller.
//
// Rails resolves the layout wrapping an action's template from the nearest
// `layout` declaration up the controller ancestry, falling back to the
// convention name `layouts/application`. Only the string-literal form names a
// layout statically; `layout :method` / `layout Proc.new { }` pick one per
// request and are reported Dynamic, and `layout false` / `layout nil` turn
// layout rendering off.
type LayoutDecl struct {
	Name    string   // layout logical name, "" unless a string/symbol literal
	Dynamic bool     // layout :method or a proc — resolved per request
	None    bool     // layout false / nil — no layout
	Only    []string // only: [...] action filter (empty = all)
	Except  []string // except: [...] action filter
	Line    int
}

// Applies reports whether the declaration governs the named action.
func (d LayoutDecl) Applies(action string) bool {
	if len(d.Only) > 0 {
		return contains(d.Only, action)
	}
	if len(d.Except) > 0 {
		return !contains(d.Except, action)
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ScanLayouts returns every class-level `layout` declaration in a controller
// source. A call indented past the class body (column > 3) or not sitting at
// the start of its statement is skipped — that is a `layout` local, a hash key,
// or a `render layout:` inside a method, none of which set the class default.
func ScanLayouts(src []byte) []LayoutDecl {
	s := string(src)
	lines := newLineIndex(s)
	mask := codeMask(s)

	var out []LayoutDecl
	for _, pos := range callSites(s, "layout", mask) {
		start := pos - len("layout")
		lineStart := start
		for lineStart > 0 && s[lineStart-1] != '\n' {
			lineStart--
		}
		if strings.TrimSpace(s[lineStart:start]) != "" {
			continue // not the first token on the line
		}
		if start-lineStart > 3 {
			continue // indented into a method body
		}
		parts, lineOffset, ok := callArgs(s, pos)
		if !ok || len(parts) == 0 {
			continue
		}
		d := LayoutDecl{Line: lines.at(pos) + lineOffset}
		first := strings.TrimSpace(parts[0])
		switch first {
		case "false", "nil":
			d.None = true
		case "true":
			continue // `layout true` is not a thing; ignore
		default:
			// A string literal names a layout; a bare symbol (`layout :foo`) is
			// a *method* Rails calls per request, so it is dynamic, not a name.
			if lit, _, ok := LeadingLiteral(first); ok {
				d.Name = lit
			} else {
				d.Dynamic = true
			}
		}
		for _, p := range parts[1:] {
			p = strings.TrimSpace(p)
			if v, ok := keywordArg(p, "only"); ok {
				d.Only = parseActionList(v)
			}
			if v, ok := keywordArg(p, "except"); ok {
				d.Except = parseActionList(v)
			}
		}
		out = append(out, d)
	}
	return out
}

// parseActionList reads the action names from an `only:`/`except:` value: a
// bare symbol (`:new`), a string, `%i[a b]` / `%w[a b]`, or `[:a, :b]`.
func parseActionList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "%i") || strings.HasPrefix(v, "%w") {
		v = v[2:]
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			v = v[1 : len(v)-1] // strip the bracket pair
		}
		return splitWords(v)
	}
	if strings.HasPrefix(v, "[") {
		v = strings.Trim(v, "[] \t")
		var out []string
		for _, part := range SplitTopLevel(v) {
			if a := cleanAction(part); a != "" {
				out = append(out, a)
			}
		}
		return out
	}
	if a := cleanAction(v); a != "" {
		return []string{a}
	}
	return nil
}

func splitWords(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if a := cleanAction(f); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func cleanAction(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `:"'`)
	if s == "" {
		return ""
	}
	for i := 0; i < len(s); i++ {
		if !IsRubyNameByte(s[i]) {
			return ""
		}
	}
	return s
}
