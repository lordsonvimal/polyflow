package patterns

import (
	"strconv"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/factpipe"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// ExtractContext carries what the `extract:` verbs need beyond the tree-sitter
// node a capture bound. Src / Grammar / File are always set by the matcher; the
// resolution hooks are the seam to the language-semantic layer (Tier VG) and
// are nil until FX.6 wires them — a verb that needs a nil hook returns its
// documented non-match value ("" / empty node).
type ExtractContext struct {
	Src     []byte
	Grammar string
	File    string

	ResolveType   func(n *sitter.Node, src []byte) string
	ResolveValue  func(n *sitter.Node, src []byte) string
	ResolveTarget func(n *sitter.Node, src []byte) (target string, depth int64)
}

// verbVal is an intermediate extraction result. A verb yields zero or more of
// these; chaining (`then:`) feeds Node forward to the next verb. Kind selects
// which of Str / Node / Int is meaningful when the value is finally lowered to
// a factpipe.Atom.
type verbVal struct {
	Node *sitter.Node
	Str  string
	Int  int64
	Kind factpipe.AtomKind
}

func (v verbVal) atom() factpipe.Atom {
	switch v.Kind {
	case factpipe.AtomNode:
		return factpipe.Node(v.Str)
	case factpipe.AtomInt:
		return factpipe.Int(v.Int)
	default:
		return factpipe.Str(v.Str)
	}
}

// parseVerb splits "name(arg)" into ("name", "arg"). A verb with no parens
// returns ("name", ""). The arg may itself contain parentheses (preceded_by
// takes a whole tree-sitter query), so the split is on the first "(" and the
// matching final ")".
func parseVerb(spec string) (name, arg string) {
	spec = strings.TrimSpace(spec)
	i := strings.IndexByte(spec, '(')
	if i < 0 || !strings.HasSuffix(spec, ")") {
		return spec, ""
	}
	return strings.TrimSpace(spec[:i]), spec[i+1 : len(spec)-1]
}

// runVerb executes one verb against node and returns its value(s).
func runVerb(spec string, node *sitter.Node, ec *ExtractContext) []verbVal {
	if node == nil {
		return nil
	}
	name, arg := parseVerb(spec)
	switch name {
	case "", "text":
		return []verbVal{{Str: node.Content(ec.Src), Kind: factpipe.AtomStr}}
	case "file":
		return []verbVal{{Str: ec.File, Kind: factpipe.AtomStr}}
	case "call_name":
		return []verbVal{{Str: callName(node.Content(ec.Src)), Kind: factpipe.AtomStr}}
	case "string_value":
		return []verbVal{{Str: stringValue(node, ec.Src), Kind: factpipe.AtomStr}}
	case "trailing_identifier":
		return []verbVal{{Str: trailingIdentifier(node, ec.Src), Kind: factpipe.AtomStr}}
	case "receiver":
		return []verbVal{{Str: receiverText(node, ec.Src), Kind: factpipe.AtomStr}}
	case "keyword_arg":
		if v := keywordArg(node, arg, ec.Src); v != nil {
			return []verbVal{{Node: v, Str: v.Content(ec.Src), Kind: factpipe.AtomStr}}
		}
		return []verbVal{{Str: "", Kind: factpipe.AtomStr}}
	case "has_keyword":
		// "true" if the call carries any of the comma-listed keyword args, else
		// "". rails_filters' `conditional` meta is "did this filter pass if:/
		// unless:" — a two-key presence test one keyword_arg cannot answer.
		present := ""
		for _, name := range strings.Split(arg, ",") {
			if keywordArg(node, strings.TrimSpace(name), ec.Src) != nil {
				present = "true"
				break
			}
		}
		return []verbVal{{Str: present, Kind: factpipe.AtomStr}}

	case "list_elements":
		return fanoutElements(node, ec.Src)
	case "list_csv":
		return []verbVal{{Str: listCSV(node, ec.Src), Kind: factpipe.AtomStr}}
	case "hash_pairs":
		return hashPairs(node, arg, ec.Src)

	case "position_in_parent":
		return []verbVal{{Int: int64(positionInParent(node)), Kind: factpipe.AtomInt}}
	case "preceded_by":
		return []verbVal{{Int: precededBy(node, arg, ec), Kind: factpipe.AtomInt}}
	case "nth_of_kind":
		return []verbVal{{Int: nthOfKind(node, arg), Kind: factpipe.AtomInt}}
	case "line":
		return []verbVal{{Int: int64(node.StartPoint().Row) + 1, Kind: factpipe.AtomInt}}
	case "end_line":
		return []verbVal{{Int: int64(node.EndPoint().Row) + 1, Kind: factpipe.AtomInt}}

	case "enclosing":
		if a := enclosing(node, arg, ec.Grammar); a != nil {
			return []verbVal{{Node: a, Str: nodeID(a, ec), Kind: factpipe.AtomNode}}
		}
		return []verbVal{{Str: "", Kind: factpipe.AtomNode}}
	case "enclosing_name":
		return []verbVal{{Str: enclosingName(node, arg, ec), Kind: factpipe.AtomStr}}
	case "synthesize_node":
		return []verbVal{{Str: synthesizeNodeID(node, arg, ec), Kind: factpipe.AtomNode}}

	case "resolved_type":
		s := ""
		if ec.ResolveType != nil {
			s = ec.ResolveType(node, ec.Src)
		}
		return []verbVal{{Str: s, Kind: factpipe.AtomStr}}
	case "resolved_value":
		s := ""
		if ec.ResolveValue != nil {
			s = ec.ResolveValue(node, ec.Src)
		}
		return []verbVal{{Str: s, Kind: factpipe.AtomStr}}
	case "resolved_target":
		var target string
		var depth int64
		if ec.ResolveTarget != nil {
			target, depth = ec.ResolveTarget(node, ec.Src)
		}
		if arg == "depth" {
			return []verbVal{{Int: depth, Kind: factpipe.AtomInt}}
		}
		return []verbVal{{Str: target, Kind: factpipe.AtomNode}}
	case "key_expr":
		return []verbVal{{Str: keyExpr(node, ec), Kind: factpipe.AtomStr}}
	case "inflect":
		return inflectVerb(node.Content(ec.Src), arg)
	case "call_ref":
		field, names, _ := strings.Cut(arg, ",")
		return callRefVals(node, strings.TrimSpace(field), names, ec.Src)
	case "header_directive":
		field, allow, _ := strings.Cut(arg, ",")
		return headerDirectiveVals(node, strings.TrimSpace(field), allow, ec.Src)

	default:
		// FX.9: a name with no in-tree case may be a registered plugin verb —
		// checked here, after every in-tree case, so a plugin can never shadow
		// a core verb name. Only a name with an actual registered provider
		// pays the ancestor-chain walk; everything else falls straight through
		// to the unknown-verb fallback below.
		if fn := lookupVerbProvider(name); fn != nil {
			vn := VerbNode{
				Type:      node.Type(),
				Text:      node.Content(ec.Src),
				StartLine: int64(node.StartPoint().Row) + 1,
				EndLine:   int64(node.EndPoint().Row) + 1,
				Ancestors: buildAncestorChain(node, ec.Src),
			}
			results, ok := fn(vn, arg, ec.File, ec.Grammar)
			if !ok {
				return []verbVal{{Str: "", Kind: factpipe.AtomStr}}
			}
			out := make([]verbVal, 0, len(results))
			for _, r := range results {
				if r.IsInt {
					out = append(out, verbVal{Int: r.Int, Kind: factpipe.AtomInt})
				} else {
					out = append(out, verbVal{Str: r.Str, Kind: factpipe.AtomStr})
				}
			}
			return out
		}
		// Unknown verb: fail soft to the source text so a typo in a YAML is a
		// wrong-value bug caught by the .dl diff test, not a panic mid-index.
		return []verbVal{{Str: node.Content(ec.Src), Kind: factpipe.AtomStr}}
	}
}

// evalArg produces the value(s) for one tuple position.
func evalArg(a ArgSpec, capNodes map[string]*sitter.Node, anchor *sitter.Node, ec *ExtractContext) []verbVal {
	if a.Literal != nil {
		return []verbVal{{Str: *a.Literal, Kind: factpipe.AtomStr}}
	}
	node := anchor
	if a.Capture != "" {
		node = capNodes[a.Capture]
	}
	if node == nil {
		return []verbVal{{Str: "", Kind: factpipe.AtomStr}}
	}
	if a.Field != "" {
		if f := node.ChildByFieldName(a.Field); f != nil {
			node = f
		} else {
			return []verbVal{{Str: "", Kind: factpipe.AtomStr}}
		}
	}
	if a.Extract == "path_transform" {
		// Structured config, not a string arg — see path_transform.go.
		return applyPathTransform(node.Content(ec.Src), a.PathTransform)
	}
	vals := runVerb(a.Extract, node, ec)
	if a.Then == "" {
		return vals
	}
	var out []verbVal
	for _, v := range vals {
		n := v.Node
		if n == nil {
			continue
		}
		out = append(out, runVerb(a.Then, n, ec)...)
	}
	return out
}

// --- individual verb implementations -------------------------------------

// callName extracts the resolvable identifier a call/reference expression
// names: everything from the first "(" is dropped, then the segment after the
// last "." is taken, and the result is returned only if it is a bare
// identifier. `authMiddleware.Authenticate()` → "Authenticate",
// `gin.Recovery()` → "Recovery", `LoggingMiddleware(log)` → "LoggingMiddleware",
// `cors.New(cors.Config{})` → "New". A non-identifier (e.g. an index
// expression) yields "". This is a generic AST-text operation — no framework
// knowledge — and is the verb form of what a hand-written "trailing call
// identifier" string helper does.
func callName(expr string) string {
	s := strings.TrimSpace(expr)
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return ""
		}
	}
	return s
}

// inflectVerb applies one Rails naming convention to a captured name node's
// text. It is a generic string transform — the same `underscore` / `pluralize`
// / `singularize` / `tableize` rules ActiveSupport ships, no framework logic
// beyond the convention name. `tableize` fans out: it yields every regular
// plural form (Knife → knives, knifes) so a `.dl` rule can validate each
// against the declared table set and pick the one the schema confirms.
func inflectVerb(text, rule string) []verbVal {
	text = strings.TrimSpace(strings.TrimPrefix(text, ":"))
	switch rule {
	case "underscore":
		return []verbVal{{Str: railsinflect.Underscore(text), Kind: factpipe.AtomStr}}
	case "pluralize":
		return []verbVal{{Str: railsinflect.Pluralize(text), Kind: factpipe.AtomStr}}
	case "singularize":
		return []verbVal{{Str: railsinflect.Singularize(text), Kind: factpipe.AtomStr}}
	case "classify":
		return []verbVal{{Str: railsinflect.Classify(text), Kind: factpipe.AtomStr}}
	case "singularize_classify":
		return []verbVal{{Str: railsinflect.Classify(railsinflect.Singularize(text)), Kind: factpipe.AtomStr}}
	case "tableize":
		cands := railsinflect.TableNameCandidates(text)
		out := make([]verbVal, 0, len(cands))
		for _, c := range cands {
			out = append(out, verbVal{Str: c, Kind: factpipe.AtomStr})
		}
		return out
	default:
		return []verbVal{{Str: text, Kind: factpipe.AtomStr}}
	}
}

// callRef is one (helper, spec) hit of call_ref's scan — spec is either a bare
// string-literal source or, when the call's arguments aren't a literal
// prefix, the raw remaining argument text (resolve_path then simply never
// resolves it, which is what should happen to a dynamic reference — no
// separate "dynamic" flag needed).
type callRef struct{ helper, spec string }

// scanCallRefs finds every standalone call to one of names' comma-listed
// identifiers in text and reads its leading string-literal arguments —
// `javascript_include_tag "application", "print"` in an ERB `<%= %>` code
// span reads the same as the JS/CSS asset-pipeline's `//= require` directive:
// a spec that resolve_path (Tier FX resolve_path) then turns into a real
// path. This is the ERB half of FX.8 step 3; internal/railsview's argument
// primitives already back render/react_component scanning the same way, so
// call_ref is a generic third consumer of them, not new framework logic.
func scanCallRefs(text, namesCSV string) []callRef {
	var out []callRef
	for _, helper := range strings.Split(namesCSV, ",") {
		helper = strings.TrimSpace(helper)
		if helper == "" {
			continue
		}
		for idx := 0; ; {
			rel := strings.Index(text[idx:], helper)
			if rel < 0 {
				break
			}
			at := idx + rel
			idx = at + len(helper)
			if at > 0 && railsview.IsRubyNameByte(text[at-1]) {
				continue // `custom_javascript_include_tag`
			}
			if idx < len(text) && railsview.IsRubyNameByte(text[idx]) {
				continue // a longer identifier sharing this prefix
			}
			args := strings.TrimSpace(text[idx:])
			args = strings.TrimPrefix(args, "(")
			names, dynamic := railsview.LiteralSources(args)
			if dynamic {
				out = append(out, callRef{helper: helper, spec: strings.TrimSpace(args)})
				continue
			}
			for _, n := range names {
				out = append(out, callRef{helper: helper, spec: n})
			}
		}
	}
	return out
}

// callRefVals projects scanCallRefs' (helper, spec) pairs onto one field —
// call_ref(helper, ...)/call_ref(spec, ...) applied to the same capture zip
// via matchtofacts.go's fan-out pairing, the same idiom hash_pairs(key)/
// hash_pairs(value) already established.
func callRefVals(n *sitter.Node, field, namesCSV string, src []byte) []verbVal {
	var out []verbVal
	for _, m := range scanCallRefs(n.Content(src), namesCSV) {
		v := m.helper
		if field == "spec" {
			v = m.spec
		}
		out = append(out, verbVal{Str: v, Kind: factpipe.AtomStr})
	}
	return out
}

// headerDirective is one `= <verb> <args>` line from a file's leading
// comment block — the Sprockets asset-directive header convention (`//=
// require "x"`, `/*= require_tree . */`). Line is the file's absolute line
// number, not an offset into the comment.
type headerDirective struct {
	verb, path, ext string
	line            int64
}

// scanHeaderDirectives reads a leading run of `comment` siblings and returns
// every `= verb path [ext]` line inside them whose verb is in the
// comma-listed allow set. Blank lines between comments aren't nodes, so they
// never break the run; the first non-comment named child does, which is
// Sprockets' own rule — a `//=` line in a file's body is not a directive,
// only one in the file's leading header comment(s) is. This is the
// tree-sitter-native reading of what internal/sprockets.ScanDirectives does
// by hand-walking source text a line at a time (FX.8 resolve_path step 3b,
// the JS/CSS half of step 3).
//
// n is either the file's root (program) node — the run starts at its first
// named child — or a comment node directly, which lets a pattern query
// anchor its capture on the first comment itself (`(program . (comment)
// @root)`), so a file with no leading comment produces zero query matches
// instead of one query match with zero directives. Both shapes are common:
// the whole-root form is what a bare `(program) @root` capture (and every
// existing test) hands in; the anchored form is what
// patterns/javascript/sprockets_directives.yaml's fixture-harness-compatible
// query uses (internal/patterns/fixtures_test.go requires a negative fixture
// to produce zero matches, which a query that always matches the file root
// cannot do).
func scanHeaderDirectives(n *sitter.Node, allowCSV string, src []byte) []headerDirective {
	allow := map[string]bool{}
	for _, v := range strings.Split(allowCSV, ",") {
		if v = strings.TrimSpace(v); v != "" {
			allow[v] = true
		}
	}
	var first *sitter.Node
	if n.Type() == "comment" {
		first = n
	} else if n.NamedChildCount() > 0 {
		first = n.NamedChild(0)
	}
	var out []headerDirective
	for c := first; c != nil && c.Type() == "comment"; c = c.NextNamedSibling() {
		text := c.Content(src)
		startLine := int(c.StartPoint().Row) + 1
		block := strings.HasPrefix(text, "/*")
		text = strings.TrimPrefix(text, "//")
		text = strings.TrimPrefix(text, "/*")
		text = strings.TrimSuffix(text, "*/")
		for j, raw := range strings.Split(text, "\n") {
			line := strings.TrimSpace(raw)
			if block {
				line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
			}
			if !strings.HasPrefix(line, "=") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, "="))
			if len(fields) == 0 || !allow[fields[0]] {
				continue
			}
			d := headerDirective{verb: fields[0], line: int64(startLine + j)}
			if len(fields) > 1 {
				d.path = strings.Trim(fields[1], `"'`)
			}
			if len(fields) > 2 && strings.HasPrefix(fields[2], ".") {
				d.ext = fields[2]
			}
			if d.path == "" {
				continue
			}
			out = append(out, d)
		}
	}
	return out
}

// headerDirectiveVals projects scanHeaderDirectives' rows onto one field —
// header_directive(verb,...)/header_directive(path,...)/header_directive(ext,...)/
// header_directive(line,...) applied to the same root capture zip via
// matchtofacts.go's fan-out pairing, the same idiom call_ref and
// hash_pairs(key)/hash_pairs(value) already established.
func headerDirectiveVals(root *sitter.Node, field, allowCSV string, src []byte) []verbVal {
	var out []verbVal
	for _, d := range scanHeaderDirectives(root, allowCSV, src) {
		switch field {
		case "line":
			out = append(out, verbVal{Int: d.line, Kind: factpipe.AtomInt})
		case "path":
			out = append(out, verbVal{Str: d.path, Kind: factpipe.AtomStr})
		case "ext":
			out = append(out, verbVal{Str: d.ext, Kind: factpipe.AtomStr})
		default: // "verb"
			out = append(out, verbVal{Str: d.verb, Kind: factpipe.AtomStr})
		}
	}
	return out
}

func stringValue(n *sitter.Node, src []byte) string {
	// Prefer a content child so escapes/quotes are excluded by the grammar.
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "string_content", "string_fragment":
			return c.Content(src)
		}
	}
	s := n.Content(src)
	s = strings.TrimPrefix(s, ":") // ruby symbol
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
			s = s[1 : len(s)-1]
		}
	}
	return s
}

func trailingIdentifier(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "identifier", "constant", "property_identifier", "field_identifier",
		"simple_symbol", "shorthand_property_identifier":
		return strings.TrimPrefix(n.Content(src), ":")
	case "call", "method_call", "function_call", "command", "call_expression":
		for _, f := range []string{"method", "function", "name"} {
			if c := n.ChildByFieldName(f); c != nil {
				return trailingIdentifier(c, src)
			}
		}
	case "selector_expression":
		if c := n.ChildByFieldName("field"); c != nil {
			return c.Content(src)
		}
	case "member_expression":
		if c := n.ChildByFieldName("property"); c != nil {
			return c.Content(src)
		}
	case "scope_resolution":
		if c := n.ChildByFieldName("name"); c != nil {
			return c.Content(src)
		}
	}
	return ""
}

func receiverText(n *sitter.Node, src []byte) string {
	for _, f := range []string{"receiver", "object", "operand"} {
		if c := n.ChildByFieldName(f); c != nil {
			return c.Content(src)
		}
	}
	switch n.Type() {
	case "call", "method_call", "call_expression":
		for _, f := range []string{"function", "method"} {
			if c := n.ChildByFieldName(f); c != nil {
				switch c.Type() {
				case "selector_expression":
					if o := c.ChildByFieldName("operand"); o != nil {
						return o.Content(src)
					}
				case "member_expression":
					if o := c.ChildByFieldName("object"); o != nil {
						return o.Content(src)
					}
				}
			}
		}
	}
	return ""
}

// keywordArg finds a `name:` / `name =` keyword/hash argument anywhere inside
// call and returns its value node.
func keywordArg(call *sitter.Node, name string, src []byte) *sitter.Node {
	var found *sitter.Node
	var walk func(*sitter.Node, int)
	walk = func(n *sitter.Node, depth int) {
		if n == nil || found != nil || depth > 6 {
			return
		}
		switch n.Type() {
		case "pair", "keyword_argument":
			k := n.ChildByFieldName("key")
			v := n.ChildByFieldName("value")
			if k != nil && v != nil {
				key := strings.TrimSuffix(strings.TrimPrefix(k.Content(src), ":"), ":")
				if key == name {
					found = v
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), depth+1)
		}
	}
	walk(call, 0)
	return found
}

// listCSV is list_elements joined with "," in source order — the flat-text
// sibling of list_elements, for a meta field that records a captured list
// verbatim (rails_filters' `only:` / `except:` meta is "a,b,c").
func listCSV(n *sitter.Node, src []byte) string {
	var parts []string
	for _, v := range fanoutElements(n, src) {
		if v.Str != "" {
			parts = append(parts, v.Str)
		}
	}
	return strings.Join(parts, ",")
}

// fanoutElements yields one value per element of an array/slice/list literal.
// A scalar node (a lone `only: :index` rather than `only: %i[...]`) is a
// one-element list and yields itself.
func fanoutElements(n *sitter.Node, src []byte) []verbVal {
	switch n.Type() {
	case "array", "list", "symbol_array", "string_array", "argument_list",
		"composite_literal", "expression_list":
	default:
		return []verbVal{{Node: n, Str: stringValue(n, src), Kind: factpipe.AtomStr}}
	}
	var out []verbVal
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == "comment" {
			continue
		}
		out = append(out, verbVal{Node: c, Str: stringValue(c, src), Kind: factpipe.AtomStr})
	}
	return out
}

// hashPairs yields one value per map entry — hash_pairs(key) the keys,
// hash_pairs(value) the values.
func hashPairs(n *sitter.Node, which string, src []byte) []verbVal {
	field := "value"
	if which == "key" {
		field = "key"
	}
	var out []verbVal
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() != "pair" && c.Type() != "keyword_argument" {
			continue
		}
		if v := c.ChildByFieldName(field); v != nil {
			out = append(out, verbVal{Node: v, Str: stringValue(v, src), Kind: factpipe.AtomStr})
		}
	}
	return out
}

func positionInParent(n *sitter.Node) int {
	p := n.Parent()
	if p == nil {
		return 0
	}
	if i := namedChildIndex(p, n); i >= 0 {
		return i
	}
	return 0
}

// precededBy counts prior named siblings whose subtree matches query.
// precededByQueryCache memoizes precededBy's compiled query, keyed by
// "grammar\x00query" — query is a static string baked into a pattern YAML's
// `extract:` spec, so every call with the same (grammar, query) pair
// compiles to an identical *sitter.Query. Without this, sitter.NewQuery ran
// from scratch on every single matched occurrence across every framework's
// extraction (profiled at ~42% of a whole-registry Run's wall time on a
// real-scale corpus) — the exact class of bug predicateRegexCache above
// already fixed once for regexp.MustCompile; precededBy's own ad-hoc query
// construction was missed. Cached queries are never Close()'d — they live
// for the process's lifetime, same as TreeSitterMatcher's own compiled
// cache.
var precededByQueryCache sync.Map // string -> *sitter.Query

func cachedPrecededByQuery(query string, lang *sitter.Language, grammar string) *sitter.Query {
	key := grammar + "\x00" + query
	if v, ok := precededByQueryCache.Load(key); ok {
		return v.(*sitter.Query)
	}
	// Wrap in an explicit pattern group so a trailing `(#eq? ...)` predicate
	// binds to the pattern rather than parsing as a second top-level pattern.
	q, err := sitter.NewQuery([]byte("("+query+")"), lang)
	if err != nil {
		if q, err = sitter.NewQuery([]byte(query), lang); err != nil {
			return nil
		}
	}
	v, _ := precededByQueryCache.LoadOrStore(key, q)
	return v.(*sitter.Query)
}

func precededBy(n *sitter.Node, query string, ec *ExtractContext) int64 {
	p := n.Parent()
	if p == nil || query == "" {
		return 0
	}
	lang := languageFor(ec.Grammar)
	if lang == nil {
		return 0
	}
	q := cachedPrecededByQuery(query, lang, ec.Grammar)
	if q == nil {
		return 0
	}
	var count int64
	for i := 0; i < int(p.NamedChildCount()); i++ {
		sib := p.NamedChild(i)
		if sib.StartByte() >= n.StartByte() {
			break
		}
		qc := sitter.NewQueryCursor()
		qc.Exec(q, sib)
		for {
			m, ok := qc.NextMatch()
			if !ok {
				break
			}
			m = filterPredicatesCached(q, m, ec.Src)
			if m != nil && len(m.Captures) > 0 {
				count++
				break
			}
		}
		qc.Close()
	}
	return count
}

// nthOfKind is the 0-based ordinal of n among its same-type siblings.
func nthOfKind(n *sitter.Node, kind string) int64 {
	p := n.Parent()
	if p == nil {
		return 0
	}
	want := kind
	if want == "" {
		want = n.Type()
	}
	var ord int64
	for i := 0; i < int(p.NamedChildCount()); i++ {
		sib := p.NamedChild(i)
		if sib.StartByte() >= n.StartByte() {
			break
		}
		if sib.Type() == want {
			ord++
		}
	}
	return ord
}

// enclosingKindTypes maps a generic scope kind to the grammar's node types.
func enclosingKindTypes(grammar, kind string) []string {
	js := grammar == "javascript" || grammar == "typescript" || grammar == "tsx" || grammar == "jsx"
	switch kind {
	case "function":
		switch {
		case grammar == "ruby":
			return []string{"method", "singleton_method"}
		case grammar == "go":
			return []string{"function_declaration", "method_declaration", "func_literal"}
		case js:
			return []string{"function_declaration", "function_expression", "arrow_function", "method_definition", "generator_function_declaration"}
		case grammar == "python":
			return []string{"function_definition"}
		}
	case "method":
		switch {
		case grammar == "ruby":
			return []string{"method", "singleton_method"}
		case grammar == "go":
			return []string{"method_declaration"}
		case js:
			return []string{"method_definition"}
		case grammar == "python":
			return []string{"function_definition"}
		}
	case "class":
		switch {
		case grammar == "ruby":
			return []string{"class"}
		case grammar == "go":
			return []string{"type_declaration"}
		case js:
			return []string{"class_declaration", "class"}
		case grammar == "python":
			return []string{"class_definition"}
		}
	case "module":
		if grammar == "ruby" {
			return []string{"module"}
		}
	case "block":
		switch {
		case grammar == "ruby":
			return []string{"block", "do_block"}
		case grammar == "go":
			return []string{"block", "func_literal"}
		case js:
			return []string{"statement_block", "arrow_function"}
		}
	}
	return nil
}

func enclosing(n *sitter.Node, kind, grammar string) *sitter.Node {
	types := enclosingKindTypes(grammar, kind)
	if len(types) == 0 {
		return nil
	}
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		for _, t := range types {
			if cur.Type() == t {
				return cur
			}
		}
	}
	return nil
}

func enclosingName(n *sitter.Node, kind string, ec *ExtractContext) string {
	if ec.Grammar == "ruby" && (kind == "class" || kind == "module") {
		if s := rubyEnclosingClassName(n, ec.Src); s != "" {
			return s
		}
	}
	e := enclosing(n, kind, ec.Grammar)
	if e == nil {
		return ""
	}
	if nm := e.ChildByFieldName("name"); nm != nil {
		if nm.Type() == "scope_resolution" {
			if last := nm.ChildByFieldName("name"); last != nil {
				return last.Content(ec.Src)
			}
		}
		return nm.Content(ec.Src)
	}
	return ""
}

// nodeID builds a stable id for an existing enclosing node: file + start line,
// matching internal/graph's "<file>:<line>" convention (service prefix is added
// downstream by the FX.1 bridge / emit stage).
func nodeID(n *sitter.Node, ec *ExtractContext) string {
	return ec.File + ":" + strconv.Itoa(int(n.StartPoint().Row)+1)
}

// synthesizeNodeID mints a stable id for an anonymous construct (block /
// lambda / arrow). Stable across runs for the same source span.
func synthesizeNodeID(n *sitter.Node, kind string, ec *ExtractContext) string {
	if kind == "" {
		kind = n.Type()
	}
	return ec.File + "\x00synth:" + kind + ":" +
		strconv.Itoa(int(n.StartByte())) + "-" + strconv.Itoa(int(n.EndByte()))
}

func keyExpr(n *sitter.Node, ec *ExtractContext) (out string) {
	w := contract.KeyWalkerFor(keyWalkerLangFor(ec.Grammar))
	if w == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	alts, ok := w.WalkKey(n, ec.Src, nil)
	if !ok || len(alts) == 0 {
		return ""
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return strings.Join(alts, "|")
}
