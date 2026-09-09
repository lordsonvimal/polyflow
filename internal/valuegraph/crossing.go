package valuegraph

import (
	"sort"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// Tier VG.4 — the crossings: bindings whose two halves live in different files.
//
// A local binding is found by walking outward from the use. A crossing cannot
// be: the two halves are joined by a name the parse does not contain — the
// component tag in `<CCreateModal createUrl={…} />` names a file the child does
// not import under that name, and the child's `this.props.createUrl` names no
// file at all. So a crossing needs an index over the whole scope the caller
// cares about, and it needs the caller to say which files define which owner.
//
// Two directions, mirror images of one another, and both are one rule shape:
//
//	forward   <Child createUrl={expr} />   binds createUrl inside Child
//	reverse   <Grid onSave={this.post} />  binds post's Nth parameter to the Nth
//	                                       argument of props.onSave(…) in Grid
//
// Everything here answers "what can this value be". Which of the two rules a
// resolved value came from is recorded on Value.Src so that the caller — which
// owns every mint, ledger and cap decision — can tell them apart.

// CrossSource is the knowledge a crossing needs and a parse cannot supply:
// which owners a file defines, and which files define an owner. "Owner" is
// whatever the crossing joins on — a component tag, a class, an exported name.
//
// It is an optional companion to FileSource: an engine whose FileSource does
// not implement it has no crossings at all, which is exactly right for an
// intraprocedural caller and is what keeps the shipped intraprocedural passes
// unable to reach a crossing by accident.
type CrossSource interface {
	// OwnersIn lists the owners defined in file.
	OwnersIn(file string) []string
	// FilesForOwner lists the files defining owner.
	FilesForOwner(owner string) []string
}

// crossSite is one indexed half of a crossing.
//
// Forward sites carry the producing expression and the source it was parsed
// from; reverse sites carry the symbol the producer handed over. Both carry the
// file and line of the *element*, which is the site a reader would go to.
type crossSite struct {
	kind  string
	owner string
	name  string
	file  string
	line  int

	src  []byte       // forward only
	expr *sitter.Node // forward only
	// text is the producing expression as written, with the grammar's wrappers
	// (the braces around an attribute value) removed. A caller whose existing
	// policy reads a literal differently from a value reached through a binding
	// needs to be able to tell which it was looking at, and the lattice
	// deliberately does not record that.
	text string

	qualified bool // reverse only: the value was written `<receiver>.<symbol>`
}

// crossIndex is the whole searched scope, indexed both ways. It is built once
// per engine, on the first crossing that needs it — an engine whose callers
// never leave a file never pays for it.
type crossIndex struct {
	once sync.Once
	fwd  map[string][]crossSite // owner \x00 name
	rev  map[string][]crossSite // symbol
}

func (e *Engine) crossings() *crossIndex {
	e.xi.once.Do(func() {
		e.xi.fwd = map[string][]crossSite{}
		e.xi.rev = map[string][]crossSite{}
		e.buildCrossIndex()
	})
	return &e.xi
}

// buildCrossIndex walks every file the FileSource offers, in sorted order, and
// records each producer site. Sorted because the index decides the order
// alternatives are discovered in, and a map-ordered index would make a
// multi-producer resolution non-deterministic across runs — the failure mode
// this repo has already paid for once.
//
// The file budget bounds the build, not just the traversal: a caller that wants
// its whole service indexed says so through Options.MaxFiles.
func (e *Engine) buildCrossIndex() {
	if e.fs == nil || len(e.ix.crossProducer) == 0 {
		return
	}
	files := append([]string(nil), e.fs.Files()...)
	sort.Strings(files)
	if len(files) > e.opts.MaxFiles {
		files = files[:e.opts.MaxFiles]
	}
	for _, file := range files {
		src, root, ok := e.fs.Parse(file)
		if !ok || root == nil {
			continue
		}
		e.indexFile(file, src, root)
	}
}

func (e *Engine) indexFile(file string, src []byte, root *sitter.Node) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		for _, r := range e.ix.crossProducer[n.Type()] {
			e.indexProducer(r, n, file, src)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

func (e *Engine) indexProducer(r CrossRule, n *sitter.Node, file string, src []byte) {
	nameNode := namedChild(n, r.Producer.NameChild)
	value := namedChild(n, r.Producer.ValueChild)
	if nameNode == nil || value == nil {
		return
	}
	name := nameNode.Content(src)
	owner, ownerNode := e.ownerOf(n, r.Producer.Owner, src)
	if name == "" || owner == "" {
		return
	}
	line := int(ownerNode.StartPoint().Row) + 1

	inner := e.ix.unwrap(value)
	if r.Direction != DirectionReverse {
		key := owner + "\x00" + name
		e.xi.fwd[key] = append(e.xi.fwd[key], crossSite{
			kind: r.Kind, owner: owner, name: name,
			file: file, line: line, src: src, expr: value,
			text: inner.Content(src),
		})
		return
	}

	sym, qualified, ok := symbolReference(inner, src)
	if !ok || (r.Producer.ValueIs == ValueIsSymbolReference && sym == "") {
		return
	}
	site := crossSite{
		kind: r.Kind, owner: owner, name: name,
		file: file, line: line, qualified: qualified,
	}
	for _, have := range e.xi.rev[sym] {
		// One component may be rendered many times in a file with the same
		// handler; they are one fact about the code, not several.
		if have.kind == site.kind && have.owner == site.owner &&
			have.name == site.name && have.file == site.file {
			return
		}
	}
	e.xi.rev[sym] = append(e.xi.rev[sym], site)
}

// ownerOf resolves the name identifying the other side of a crossing. The only
// mode is "read this field of the parent node" — the enclosing element's tag —
// and a qualified tag is reduced to its last segment, because that is the name
// the definition is known by.
func (e *Engine) ownerOf(n *sitter.Node, mode string, src []byte) (string, *sitter.Node) {
	if !strings.HasPrefix(mode, OwnerParentField) {
		return "", nil
	}
	parent := n.Parent()
	if parent == nil {
		return "", nil
	}
	f := parent.ChildByFieldName(strings.TrimPrefix(mode, OwnerParentField))
	if f == nil {
		return "", nil
	}
	name := f.Content(src)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name, parent
}

// unwrap sees through a node the spec claims no rule for and that holds exactly
// one named child — the braces around an attribute value, a parenthesised
// expression. It is the same transparency resolveNode applies, reused where a
// rule needs the *text* of the innermost expression rather than its value.
func (ix *index) unwrap(n *sitter.Node) *sitter.Node {
	for n != nil && n.NamedChildCount() == 1 && !ix.claims(n.Type()) {
		n = n.NamedChild(0)
	}
	return n
}

func (ix *index) claims(typ string) bool {
	if _, ok := ix.literal[typ]; ok {
		return true
	}
	if _, ok := ix.concat[typ]; ok {
		return true
	}
	if _, ok := ix.union[typ]; ok {
		return true
	}
	if _, ok := ix.opaque[typ]; ok {
		return true
	}
	return false
}

// symbolReference reads a node as a reference to a definition: a bare name, or
// a dotted path whose last segment names it. Anything with a bracket, a call or
// an operator in it is a value written inline, which has no definition to join
// to — and is what value_is: symbol_reference excludes.
func symbolReference(n *sitter.Node, src []byte) (sym string, qualified bool, ok bool) {
	if n == nil {
		return "", false, false
	}
	text := strings.TrimSpace(n.Content(src))
	if text == "" {
		return "", false, false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '$', c == '.':
		default:
			return "", false, false
		}
	}
	sym = text
	if i := strings.LastIndexByte(text, '.'); i >= 0 {
		sym, qualified = text[i+1:], true
	}
	if sym == "" {
		return "", false, false
	}
	return sym, qualified, true
}

// ── resolution ──────────────────────────────────────────────────────────────

// crossForward answers "who bound this name from outside". For every owner the
// consumer's file defines, every producer site that names it is resolved in its
// own file, and the alternatives are unioned: a component rendered from
// eighteen sites with eighteen create URLs genuinely has eighteen values, and
// picking one of them is the caller's decision, not this package's.
func (c *ctx) crossForward(name string, at *sitter.Node, depth int) (Value, bool) {
	if c.e.xs == nil || name == "" {
		return Value{}, false
	}
	ix := c.e.crossings()
	owners := append([]string(nil), c.e.xs.OwnersIn(c.file)...)
	sort.Strings(owners)

	var vals []Value
	for _, owner := range owners {
		for _, site := range ix.fwd[owner+"\x00"+name] {
			v, ok := c.resolveSite(site, depth)
			if !ok {
				continue
			}
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return Value{}, false
	}
	return c.capUnion(at, Union(vals...)), true
}

// resolveSite resolves one forward producer expression in the file that wrote
// it, stamped with where it came from.
func (c *ctx) resolveSite(site crossSite, depth int) (Value, bool) {
	if !c.enterCrossing(site) {
		return Value{}, false
	}
	defer c.leaveCrossing(site)

	src := Origin{Reason: site.kind, File: site.file, Line: site.line, Text: site.text}
	if _, _, capped := c.open(site.file); capped {
		return withSrc(Opaque(Origin{Reason: ReasonFiles, File: site.file, Line: site.line}), src), true
	}
	sub := c.inFile(site.file, site.src, topOf(site.expr))
	scope := sub.enclosingScope(site.expr)
	if scope == nil {
		scope = sub.root
	}
	return withSrc(sub.resolveNode(site.expr, scope, depth+1), src), true
}

// crossReverse answers "who calls the function this parameter belongs to, from
// the other side of a prop". The producer handed a named function to a
// component; that component calls it back with the value this parameter is
// missing, at the same position.
func (c *ctx) crossReverse(name string, scope *sitter.Node, depth int) (Value, bool) {
	if c.e.xs == nil || scope == nil {
		return Value{}, false
	}
	sym := c.scopeName(scope)
	if sym == "" {
		return Value{}, false
	}
	pos, ok := c.paramIndexOf(scope, name)
	if !ok {
		return Value{}, false
	}
	ix := c.e.crossings()

	var vals []Value
	for _, site := range ix.rev[sym] {
		rule, known := c.e.ix.crossByKind[site.kind]
		if !known {
			continue
		}
		if site.qualified && site.file != c.file {
			// `this.postToServer` names a method of the file that wrote it. A
			// same-named method elsewhere is a different function.
			continue
		}
		if !c.enterCrossing(site) {
			continue
		}
		vals = append(vals, c.reverseCallArgs(rule, site, pos, depth)...)
		c.leaveCrossing(site)
	}
	if len(vals) == 0 {
		return Value{}, false
	}
	return c.capUnion(scope, Union(vals...)), true
}

// callSiteArgs answers "who calls the function this parameter belongs to",
// within the query's own root. It is crossReverse with the crossing removed:
// the same argument→parameter relation, but the callee is named by the call
// itself, so there is no owner to join on, no index to build and no CrossSource
// to require.
//
// The whole root is searched, not just the sibling statements: a wrapper is
// commonly declared at module scope and called from inside three different
// components in the same file, and refusing to look would leave a parameter
// unresolved next to the literal that fills it.
//
// A call that supplies no argument at the position is Opaque(ReasonArity)
// rather than a skipped alternative: `load()` alongside `load("/api/x")` means
// the parameter really can be undefined, and a caller that mints on the strength
// of the second call without seeing the first is asserting more than the code
// says.
func (c *ctx) callSiteArgs(name string, scope *sitter.Node, depth int) (Value, bool) {
	r := c.e.ix.callSite
	if r == nil || scope == nil || c.root == nil {
		return Value{}, false
	}
	sym := c.scopeName(scope)
	if sym == "" {
		return Value{}, false
	}
	pos, ok := c.paramIndexOf(scope, name)
	if !ok {
		return Value{}, false
	}

	var vals []Value
	for _, call := range c.callsTo(sym, *r) {
		args := call.ChildByFieldName(r.Args)
		var arg *sitter.Node
		if args != nil {
			arg = c.nthValue(args, pos)
		}
		if arg == nil {
			vals = append(vals, Opaque(c.originOf(call, ReasonArity)))
			continue
		}
		argScope := c.enclosingScope(arg)
		if argScope == nil {
			argScope = c.root
		}
		vals = append(vals, c.resolveNode(arg, argScope, depth+1))
	}
	if len(vals) == 0 {
		return Value{}, false
	}
	return c.capUnion(scope, Union(vals...)), true
}

// callsTo finds every call in this file whose callee is written exactly sym.
// Only a bare callee matches: `obj.load(…)` is a different function that
// happens to share a last segment, and reading it as this one is the kind of
// name collision the crossing index already refuses.
func (c *ctx) callsTo(sym string, r CallSiteRule) []*sitter.Node {
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == r.Node {
			if fn := n.ChildByFieldName(r.Fn); fn != nil && strings.TrimSpace(fn.Content(c.src)) == sym {
				out = append(out, n)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(c.root)
	return out
}

// reverseCallArgs reads the pos'th argument of every call, in every file
// defining the owner, that reaches the handed-over symbol through the prop it
// was handed as.
func (c *ctx) reverseCallArgs(rule CrossRule, site crossSite, pos int, depth int) []Value {
	files := append([]string(nil), c.e.xs.FilesForOwner(site.owner)...)
	sort.Strings(files)

	var out []Value
	for _, file := range files {
		pf, ok, capped := c.open(file)
		if capped {
			out = append(out, withSrc(
				Opaque(Origin{Reason: ReasonFiles, File: file}),
				Origin{Reason: site.kind, File: file}))
			break
		}
		if !ok {
			continue
		}
		sub := c.inFile(file, pf.src, pf.root)
		for _, call := range sub.callsThrough(rule.Consumer, site.name) {
			line := int(call.StartPoint().Row) + 1
			src := Origin{Reason: site.kind, File: file, Line: line}
			arg := sub.positionalArg(call, rule.Consumer, pos)
			if arg != nil {
				src.Text = arg.Content(pf.src)
			}
			if arg == nil {
				out = append(out, withSrc(Opaque(Origin{
					Reason: ReasonArity, File: file, Line: line,
					Text: truncate(call.Content(pf.src), 120),
				}), src))
				continue
			}
			scope := sub.enclosingScope(arg)
			if scope == nil {
				scope = sub.root
			}
			out = append(out, withSrc(sub.resolveNode(arg, scope, depth+1), src))
		}
	}
	return out
}

// callsThrough finds every call whose callee reads name off one of the
// consumer's declared roots — `this.props.onSave(…)`, `props.onSave(…)` — or
// reads it bare, which is the destructured form.
func (c *ctx) callsThrough(cons CrossEndpoint, name string) []*sitter.Node {
	if cons.Call == "" || c.root == nil {
		return nil
	}
	want := make([]string, 0, len(cons.Roots)+1)
	for _, root := range cons.Roots {
		want = append(want, root+"."+name)
	}
	if cons.Destructure || len(cons.Roots) == 0 {
		want = append(want, name)
	}

	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == cons.Call {
			if fn := n.ChildByFieldName(cons.CallFn); fn != nil {
				text := strings.TrimSpace(fn.Content(c.src))
				for _, w := range want {
					if text == w {
						out = append(out, n)
						break
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(c.root)
	return out
}

// positionalArg returns the pos'th value-carrying argument of call, or nil when
// the call supplies none — too few arguments, or a spread that makes "the third
// argument" meaningless.
func (c *ctx) positionalArg(call *sitter.Node, cons CrossEndpoint, pos int) *sitter.Node {
	args := call.ChildByFieldName(cons.CallArgs)
	if args == nil {
		return nil
	}
	return c.nthValue(args, pos)
}

// nthValue is positional addressing shared by arguments and parameters: skip
// what carries no value, give up entirely on what destroys the numbering.
func (c *ctx) nthValue(list *sitter.Node, pos int) *sitter.Node {
	idx := 0
	for i := 0; i < int(list.NamedChildCount()); i++ {
		n := list.NamedChild(i)
		typ := n.Type()
		if c.e.ix.ignore[typ] {
			continue
		}
		if c.e.ix.stop[typ] {
			return nil
		}
		if idx == pos {
			return n
		}
		idx++
	}
	return nil
}

// enterCrossing / leaveCrossing bound mutual recursion between crossings: a
// component that passes a prop it was itself given, to a component that passes
// it back, would otherwise walk the index forever. Depth alone would stop it,
// but the reason it stopped would say "depth" when the truth is "cycle".
func (c *ctx) enterCrossing(site crossSite) bool {
	k := site.kind + "\x00" + site.owner + "\x00" + site.name + "\x00" + site.file
	if c.crossed[k] {
		return false
	}
	c.crossed[k] = true
	return true
}

func (c *ctx) leaveCrossing(site crossSite) {
	delete(c.crossed, site.kind+"\x00"+site.owner+"\x00"+site.name+"\x00"+site.file)
}

// crossMember tries a crossing for an expression the spec stops at, when its
// text is one of a crossing's declared roots followed by a name:
// `this.props.createUrl` is a crossed binding, not an unreadable member read.
func (c *ctx) crossMember(n *sitter.Node, depth int) (Value, bool) {
	if c.e.xs == nil {
		return Value{}, false
	}
	text := strings.TrimSpace(n.Content(c.src))
	for _, rules := range c.e.ix.crossProducer {
		for _, r := range rules {
			if r.Direction == DirectionReverse {
				continue
			}
			for _, root := range r.Consumer.Roots {
				name, ok := strings.CutPrefix(text, root+".")
				if !ok || name == "" || strings.ContainsAny(name, ".[(") {
					continue
				}
				if v, ok := c.crossForward(name, n, depth); ok {
					return v, true
				}
			}
		}
	}
	return Value{}, false
}

// scopeName is the name a scope is known by: its own, where the grammar gives
// it one, or the name of the binding that holds it — `postToServer = (…) => …`
// is as named as `function postToServer(…)`, and a reverse crossing joins on
// exactly that name.
func (c *ctx) scopeName(scope *sitter.Node) string {
	if r, ok := c.e.ix.scope[scope.Type()]; ok && r.Name != "" {
		if nm := scope.ChildByFieldName(r.Name); nm != nil {
			return nm.Content(c.src)
		}
	}
	child := scope
	for p := scope.Parent(); p != nil; p = p.Parent() {
		if _, isScope := c.e.ix.scope[p.Type()]; isScope {
			return ""
		}
		for _, r := range c.e.ix.binding[p.Type()] {
			v := fieldOrChild(p, r.Value, r.ValueChild)
			if v == nil || v.StartByte() != child.StartByte() || v.EndByte() != child.EndByte() {
				continue
			}
			if nm := fieldOrChild(p, r.Name, r.NameChild); nm != nil {
				return nm.Content(c.src)
			}
		}
		child = p
	}
	return ""
}

// paramIndexOf returns the position of the parameter binding name, counting the
// way a call site counts its arguments.
func (c *ctx) paramIndexOf(scope *sitter.Node, name string) (int, bool) {
	r, ok := c.e.ix.scope[scope.Type()]
	if !ok {
		return 0, false
	}
	for _, field := range []string{r.Params, r.ParamsAlt} {
		if field == "" {
			continue
		}
		params := scope.ChildByFieldName(field)
		if params == nil {
			continue
		}
		if params.NamedChildCount() == 0 {
			// The lone unparenthesised parameter: it is position zero or it is
			// not this scope's business.
			if params.Content(c.src) == name {
				return 0, true
			}
			continue
		}
		idx := 0
		for i := 0; i < int(params.NamedChildCount()); i++ {
			p := params.NamedChild(i)
			if c.e.ix.ignore[p.Type()] {
				continue
			}
			if paramNamed(p, c.src, name) {
				return idx, true
			}
			idx++
		}
	}
	return 0, false
}

func namedChild(n *sitter.Node, i int) *sitter.Node {
	if n == nil || i < 0 || i >= int(n.NamedChildCount()) {
		return nil
	}
	return n.NamedChild(i)
}
