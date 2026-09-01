package railsview

import (
	"sort"
	"strings"
)

// Render kinds. Positional is the bare first-argument form, whose meaning
// depends on where the call sits: `render "foo"` names a *partial* in a
// template and a *template* in a controller. The scanner reports what it saw
// and leaves that choice to the resolver.
const (
	RenderPositional = "positional"
	RenderPartial    = "partial"
	RenderTemplate   = "template"
	RenderLayout     = "layout"
)

// Render is one `render` call that names a view.
type Render struct {
	Kind       string
	Spec       string // logical template path, or the raw expression when Dynamic
	Line       int
	Collection bool
	Dynamic    bool
}

// ReactComponent is one `react_component("Name", ...)` helper call.
type ReactComponent struct {
	Name    string // component name, or the raw expression when Dynamic
	Line    int
	Dynamic bool
	Props   []ReactProp // the props hash, keys with their raw Ruby value expression
}

// ReactProp is one `name: <value>` entry of a react_component props hash. Value
// is the verbatim Ruby expression — a string literal, a `*_url`/`*_path` route
// helper call, or anything else — left for the linker to resolve.
type ReactProp struct {
	Name  string
	Value string
}

// nonViewRenderKeys are the `render` spellings that produce a response body
// directly instead of naming a template.
//
// They are skipped in silence rather than ledgered: `render json: @user` is not
// an unresolvable clue about a view, it is a statement that there is no view —
// the same reason Tier K.3 drops `//= require_self` instead of ledgering it.
// `file:` is listed because it names an absolute path outside app/views.
var nonViewRenderKeys = []string{
	"json", "plain", "text", "html", "xml", "js", "inline", "body",
	"nothing", "status", "head", "file",
}

// ScanRenders returns every `render` call in a Ruby source view: a controller
// file directly, or the virtualRuby half of SplitERB for a template.
func ScanRenders(src []byte) []Render {
	s := string(src)
	lines := newLineIndex(s)
	mask := codeMask(s)

	var out []Render
	for _, pos := range callSites(s, "render", mask) {
		parts, lineOffset, ok := callArgs(s, pos)
		if !ok {
			continue
		}
		line := lines.at(pos) + lineOffset
		collection := hasKeyword(parts, "collection")

		emit := func(kind, expr string) {
			// `layout: false` and `render nothing: true` toggle rendering off.
			// They name no view, so there is nothing to ledger — the same call
			// as `render json:` and Tier K.3's `//= require_self`.
			switch expr {
			case "false", "true", "nil":
				return
			}
			r := Render{Kind: kind, Line: line, Collection: collection}
			if lit, ok := leadingSpec(expr); ok {
				r.Spec = lit
			} else {
				r.Spec, r.Dynamic = expr, true
			}
			out = append(out, r)
		}

		// The bare first argument, when it is not itself a keyword.
		if first := strings.TrimSpace(parts[0]); !isKeywordArg(first) {
			if !isNonViewRender(first) {
				emit(RenderPositional, first)
			}
		}
		// Every view-naming keyword. `render partial: "row", layout: "wrap"`
		// pulls in two views, and naming only the first would be the fan-out
		// bug (phases.md #1).
		for _, p := range parts {
			p = strings.TrimSpace(p)
			for _, k := range renderViewKeys {
				if v, ok := keywordArg(p, k.key); ok {
					emit(k.kind, v)
					break
				}
			}
		}
	}
	return out
}

// parseReactProps reconstructs the props hash from a react_component call's
// arguments after the component name — either an explicit `{ … }` literal or a
// bare trailing keyword list — and splits it into name/value pairs. The value
// is kept verbatim; a pair whose key is not a plain identifier (a splat, a
// computed key) is skipped rather than guessed.
func parseReactProps(rest []string) []ReactProp {
	var kept []string
	for _, p := range rest {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	blob := strings.Trim(strings.TrimSpace(strings.Join(kept, ",")), ", \t\n\r")
	if strings.HasPrefix(blob, "{") {
		blob = strings.TrimSpace(strings.TrimPrefix(blob, "{"))
		if i := strings.LastIndexByte(blob, '}'); i >= 0 {
			blob = strings.TrimSpace(blob[:i])
		}
	}
	blob = strings.Trim(blob, ", \t\n\r")
	if blob == "" {
		return nil
	}
	var out []ReactProp
	for _, part := range SplitTopLevel(blob) {
		if name, val, ok := splitKwPair(strings.TrimSpace(part)); ok {
			out = append(out, ReactProp{Name: name, Value: val})
		}
	}
	return out
}

// splitKwPair splits `name: <value>` or `:name => <value>` into its identifier
// key and verbatim value expression. The `:` scan skips `::` so a value like
// `pusher_config(channel: PusherClient::CHANNELS[:x])` keys on `pusherConfig`,
// not on the nested `channel:`.
func splitKwPair(part string) (name, val string, ok bool) {
	if strings.HasPrefix(part, ":") {
		if i := strings.Index(part, "=>"); i > 0 {
			n := strings.TrimSpace(part[1:i])
			if isPlainIdent(n) {
				return n, strings.TrimSpace(part[i+2:]), true
			}
		}
		return "", "", false
	}
	for i := 0; i < len(part); i++ {
		if part[i] != ':' {
			continue
		}
		if i+1 < len(part) && part[i+1] == ':' {
			i++
			continue
		}
		n := strings.TrimSpace(part[:i])
		if !isPlainIdent(n) {
			return "", "", false
		}
		return n, strings.TrimSpace(part[i+1:]), true
	}
	return "", "", false
}

func isPlainIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !IsRubyNameByte(s[i]) {
			return false
		}
	}
	return s[0] < '0' || s[0] > '9'
}

// renderViewKeys are the keyword arguments that name a view. `action:` is a
// template selector — `render action: :new` renders the current controller's
// new template — so it resolves exactly like `template:`.
var renderViewKeys = []struct{ key, kind string }{
	{"partial", RenderPartial},
	{"template", RenderTemplate},
	{"action", RenderTemplate},
	{"layout", RenderLayout},
}

func isKeywordArg(expr string) bool {
	for _, k := range renderViewKeys {
		if _, ok := keywordArg(expr, k.key); ok {
			return true
		}
	}
	return isNonViewRender(expr)
}

func isNonViewRender(expr string) bool {
	for _, k := range nonViewRenderKeys {
		if _, ok := keywordArg(expr, k); ok {
			return true
		}
	}
	return false
}

// leadingSpec reads a template name written either as a string or as a symbol.
// `render :index` and `render action: :fail` are as static as their quoted
// forms and account for a quarter of orion's controller renders.
func leadingSpec(expr string) (string, bool) {
	if lit, _, ok := LeadingLiteral(expr); ok {
		return lit, true
	}
	if !strings.HasPrefix(expr, ":") {
		return "", false
	}
	i := 1
	for i < len(expr) && IsRubyNameByte(expr[i]) {
		i++
	}
	if i == 1 {
		return "", false
	}
	return expr[1:i], true
}

// ScanReactComponents returns every `react_component` helper call.
//
// The helper's own body pins the resolution rule — it writes the component name
// into `data-react-class`, which application.js then looks up as
// `window[compName]` — so a literal name is a fully static binding to a JSX
// component. A non-literal one is ledgered (phases.md #12).
func ScanReactComponents(src []byte) []ReactComponent {
	s := string(src)
	lines := newLineIndex(s)
	mask := codeMask(s)

	var out []ReactComponent
	for _, pos := range callSites(s, "react_component", mask) {
		parts, lineOffset, ok := callArgs(s, pos)
		if !ok {
			continue
		}
		rc := ReactComponent{Line: lines.at(pos) + lineOffset}
		expr := strings.TrimSpace(parts[0])
		if lit, _, isLit := LeadingLiteral(expr); isLit {
			rc.Name = lit
		} else {
			rc.Name, rc.Dynamic = expr, true
		}
		if len(parts) > 1 {
			rc.Props = parseReactProps(parts[1:])
		}
		out = append(out, rc)
	}
	return out
}

// codeMask marks every byte that sits inside a Ruby string literal or a `#`
// comment, so a scanner does not read `# render :index` in a commented-out line
// or the word "render" inside a flash message as a call.
//
// Line-scoped: quote and comment state both reset at a newline. Ruby can carry
// a string across lines, but a view helper never does, and resetting keeps one
// stray apostrophe from masking the rest of the file.
func codeMask(s string) []bool {
	mask := make([]bool, len(s))
	var quote byte
	inComment := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\n':
			quote, inComment = 0, false
		case inComment:
			mask[i] = true
		case quote != 0:
			mask[i] = true
			if c == quote && s[i-1] != '\\' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote, mask[i] = c, true
		case c == '#':
			inComment, mask[i] = true, true
		}
	}
	return mask
}

// lineIndex converts a byte offset to a 1-based line number.
type lineIndex []int

func newLineIndex(s string) lineIndex {
	idx := lineIndex{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (l lineIndex) at(pos int) int {
	return sort.SearchInts(l, pos+1)
}
