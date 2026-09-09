package valuegraph

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// FileSource lets the engine cross file boundaries without knowing how the
// caller parses or caches. The linker adapter wraps its own per-phase parse
// cache, so the engine adds no parse of its own.
type FileSource interface {
	// Files lists the resolvable files in the scope the caller wants
	// searched (typically one service).
	Files() []string
	// Parse returns the cached parse for file. ok is false for unreadable
	// or unparseable files; the engine treats that as Opaque, not fatal.
	Parse(file string) (src []byte, root *sitter.Node, ok bool)
}

// Options bounds one Resolve call. Every bound has its own Origin reason, so a
// run that is slow or over-broad is diagnosable from the ledger rather than by
// bisection.
type Options struct {
	MaxDepth      int // binding hops; default 8
	MaxUnionWidth int // alternatives kept before collapsing to Opaque; default 32
	MaxFiles      int // files opened per Resolve call; default 64
}

const (
	defaultMaxDepth      = 8
	defaultMaxUnionWidth = 32
	defaultMaxFiles      = 64
)

func (o Options) withDefaults() Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultMaxDepth
	}
	if o.MaxUnionWidth <= 0 {
		o.MaxUnionWidth = defaultMaxUnionWidth
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = defaultMaxFiles
	}
	return o
}

// Query is one question: what string can Expr be?
//
// Src and Root may be omitted, in which case the engine asks the FileSource for
// File — that costs one of the call's file budget. Scope may be omitted, in
// which case the nearest enclosing scope node is inferred from Expr.
type Query struct {
	File  string
	Src   []byte
	Root  *sitter.Node
	Expr  *sitter.Node // the expression to resolve
	Scope *sitter.Node // enclosing function; nil means "infer from Expr"
}

// Engine resolves expressions against one language's binding spec.
//
// Safe for concurrent use: the memo cache is guarded, and everything else per
// call lives on the resolve context.
type Engine struct {
	ix   *index
	spec *Spec
	fs   FileSource
	xs   CrossSource // nil unless fs also knows which files define which owner
	opts Options

	xi crossIndex

	mu   sync.Mutex
	memo map[memoKey]Value
}

// memoKey identifies a resolved symbol. It carries the use offset as well as
// the scope, because binding selection is position-sensitive — only bindings
// written before the use are in play — and two reads of the same name at
// different offsets in one scope are genuinely different questions.
type memoKey struct {
	file   string
	scope  uint32
	symbol string
	use    uint32
}

// cycleKey is the coarser (file, scope, symbol) identity from which a
// self-referential binding is detected. It deliberately drops the use offset:
// `a = b; b = a` revisits the same name at a different offset each hop, and a
// key that distinguished them would recurse until the depth cap instead of
// reporting the cycle it is.
type cycleKey struct {
	file   string
	scope  uint32
	symbol string
}

// New builds an engine. A nil or invalid spec is not an error here — it yields
// an engine that resolves everything to Opaque, which is the correct behaviour
// for a caller that has no spec for the language in front of it. Call
// Spec.Validate if you want to know.
// A FileSource that also implements CrossSource enables the spec's crossing
// rules; one that does not leaves them inert, so an intraprocedural caller
// cannot reach a crossing by accident.
func New(spec *Spec, fs FileSource, opts Options) *Engine {
	e := &Engine{
		ix:   newIndex(spec),
		spec: spec,
		fs:   fs,
		opts: opts.withDefaults(),
		memo: make(map[memoKey]Value),
	}
	if xs, ok := fs.(CrossSource); ok {
		e.xs = xs
	}
	return e
}

// Resolve never returns a zero Value and never panics on a malformed tree.
func (e *Engine) Resolve(q Query) Value {
	if q.Expr == nil {
		return Opaque(Origin{Reason: ReasonUnsupported, File: q.File, Text: "no expression"})
	}
	c := &ctx{
		e: e, file: q.File, src: q.Src, root: q.Root,
		opened:   map[string]parsedFile{},
		visiting: map[cycleKey]bool{},
		crossed:  map[string]bool{},
	}

	if c.src == nil {
		pf, ok, capped := c.open(q.File)
		if capped {
			return Opaque(Origin{Reason: ReasonFiles, File: q.File, Text: q.File})
		}
		if !ok {
			return Opaque(Origin{Reason: ReasonUnsupported, File: q.File, Text: q.File})
		}
		c.src, c.root = pf.src, pf.root
	}
	if c.root == nil {
		c.root = topOf(q.Expr)
	}

	scope := q.Scope
	if scope == nil {
		scope = c.enclosingScope(q.Expr)
	}
	if scope == nil {
		scope = c.root
	}
	return c.resolveNode(q.Expr, scope, 0)
}

type parsedFile struct {
	src  []byte
	root *sitter.Node
	ok   bool
}

// ctx is the state of one Resolve call: the file budget and the set of symbols
// currently being resolved. Nothing here outlives the call, which is what keeps
// the caps per-call as Options documents them.
type ctx struct {
	e    *Engine
	file string
	src  []byte
	root *sitter.Node

	opened   map[string]parsedFile
	visiting map[cycleKey]bool
	crossed  map[string]bool
}

// inFile returns the same call, reading a different file. The budgets, the
// cycle set and the crossing set are shared: they bound one Resolve, and a
// resolution that has left its own file is still that one Resolve.
func (c *ctx) inFile(file string, src []byte, root *sitter.Node) *ctx {
	return &ctx{
		e: c.e, file: file, src: src, root: root,
		opened: c.opened, visiting: c.visiting, crossed: c.crossed,
	}
}

// open parses file through the FileSource, charging the call's file budget.
// A file already opened by this call is free — the budget counts distinct
// files, not lookups, so a wide fan-out is what trips it and a deep one is not.
func (c *ctx) open(file string) (pf parsedFile, ok bool, capped bool) {
	if got, seen := c.opened[file]; seen {
		return got, got.ok, false
	}
	if len(c.opened) >= c.e.opts.MaxFiles {
		return parsedFile{}, false, true
	}
	if c.e.fs == nil {
		c.opened[file] = parsedFile{}
		return parsedFile{}, false, false
	}
	src, root, good := c.e.fs.Parse(file)
	got := parsedFile{src: src, root: root, ok: good && root != nil}
	c.opened[file] = got
	return got, got.ok, false
}

// resolveNode reads one expression. The rule order is fixed: a node is a
// literal, then a concat, then a union, then a deliberate stop, then a name to
// look up, then a wrapper to see through — and anything left is Opaque with a
// reason naming the node type, never a silent empty result.
func (c *ctx) resolveNode(n *sitter.Node, scope *sitter.Node, depth int) Value {
	if n == nil {
		return Opaque(Origin{Reason: ReasonUnsupported, File: c.file, Text: "nil node"})
	}
	if depth > c.e.opts.MaxDepth {
		return Opaque(c.originOf(n, ReasonDepth))
	}
	ix := c.e.ix
	typ := n.Type()

	if r, ok := ix.literal[typ]; ok {
		return c.literalValue(n, r, scope, depth)
	}
	if r, ok := ix.concat[typ]; ok && c.operatorMatches(n, r.Operator) {
		return Concat(c.resolveParts(n, r.Parts, scope, depth)...)
	}
	if r, ok := ix.union[typ]; ok && c.operatorMatches(n, r.Operator) {
		return c.capUnion(n, Union(c.resolveParts(n, r.Parts, scope, depth)...))
	}
	if r, ok := ix.opaque[typ]; ok {
		// A read the spec stops at may still be a crossed binding written as a
		// qualified name: `this.props.createUrl` is bound by whoever renders
		// this component, and only a crossing can see that.
		if v, crossed := c.crossMember(n, depth); crossed {
			return v
		}
		return Opaque(c.originOf(n, r.Reason))
	}
	if n.NamedChildCount() == 0 {
		// A leaf the spec did not claim is a name: the one thing that needs
		// looking up rather than reading.
		return c.resolveSymbol(n.Content(c.src), n, scope, depth)
	}
	if n.NamedChildCount() == 1 {
		// A transparent wrapper — a parenthesised expression, a statement
		// holding one expression, an interpolation holding one expression.
		// Seeing through it is what keeps the spec free of one rule per
		// bracketing construct.
		return c.resolveNode(n.NamedChild(0), scope, depth)
	}
	return Opaque(c.originOf(n, ReasonUnsupported))
}

// resolveParts reads the fields named by a concat or union rule. An empty
// field list means every named child, in order.
func (c *ctx) resolveParts(n *sitter.Node, fields []string, scope *sitter.Node, depth int) []Value {
	var out []Value
	if len(fields) == 0 {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			out = append(out, c.resolveNode(n.NamedChild(i), scope, depth))
		}
		return out
	}
	for _, f := range fields {
		child := n.ChildByFieldName(f)
		if child == nil {
			out = append(out, Opaque(c.originOf(n, ReasonUnsupported)))
			continue
		}
		out = append(out, c.resolveNode(child, scope, depth))
	}
	return out
}

func (c *ctx) literalValue(n *sitter.Node, r LiteralRule, scope *sitter.Node, depth int) Value {
	switch r.Text {
	case TextRaw:
		return Literal(n.Content(c.src))
	case TextTemplate:
		var parts []Value
		lo, hi := innerChildRange(n)
		for i := lo; i < hi; i++ {
			child := n.Child(i)
			if child == nil || !child.IsNamed() {
				continue
			}
			if child.NamedChildCount() == 0 {
				// A fragment: text between the holes.
				parts = append(parts, Literal(child.Content(c.src)))
				continue
			}
			// A hole: its first named child is the expression.
			parts = append(parts, c.resolveNode(child.NamedChild(0), scope, depth))
		}
		return Concat(parts...)
	default: // TextInner
		return Literal(innerText(n, c.src))
	}
}

// capUnion collapses a union wider than the cap. The alternatives are dropped
// rather than truncated: half a branch set presented as a whole one is the
// failure mode this lattice exists to prevent.
func (c *ctx) capUnion(n *sitter.Node, v Value) Value {
	if v.Kind == KindUnion && len(v.Parts) > c.e.opts.MaxUnionWidth {
		return Opaque(c.originOf(n, ReasonWidth))
	}
	return v
}

// resolveSymbol looks a name up through the scope chain: the bindings of the
// nearest scope that has any, then outward. A name bound more than once in one
// scope is a Union, not an ambiguity — a URL local assigned a different literal
// on each arm of a branch is several real requests written at one site.
func (c *ctx) resolveSymbol(name string, at *sitter.Node, scope *sitter.Node, depth int) Value {
	if name == "" || scope == nil {
		return Opaque(c.originOf(at, ReasonNoBinding))
	}
	use := at.StartByte()
	mk := memoKey{file: c.file, scope: scope.StartByte(), symbol: name, use: use}
	c.e.mu.Lock()
	cached, hit := c.e.memo[mk]
	c.e.mu.Unlock()
	if hit {
		return cached
	}

	ck := cycleKey{file: c.file, scope: scope.StartByte(), symbol: name}
	if c.visiting == nil {
		c.visiting = map[cycleKey]bool{}
	}
	if c.visiting[ck] {
		return Opaque(c.originOf(at, ReasonCycle))
	}
	c.visiting[ck] = true
	defer delete(c.visiting, ck)

	out := c.lookup(name, at, scope, use, depth)

	c.e.mu.Lock()
	c.e.memo[mk] = out
	c.e.mu.Unlock()
	return out
}

func (c *ctx) lookup(name string, at *sitter.Node, scope *sitter.Node, use uint32, depth int) Value {
	for cur := scope; cur != nil; cur = c.outward(cur) {
		if rhs := c.bindingsIn(cur, name, use); len(rhs) > 0 {
			if len(rhs) > c.e.opts.MaxUnionWidth {
				return Opaque(c.originOf(at, ReasonWidth))
			}
			vals := make([]Value, 0, len(rhs))
			for _, r := range rhs {
				// The hop is what MaxDepth counts, and each bound value is read
				// as of its own position: `let a = b` may only see a `b` written
				// above it, not one written below.
				vals = append(vals, c.resolveNode(r, cur, depth+1))
			}
			return c.capUnion(at, Union(vals...))
		}
		if r, ok := c.e.ix.scope[cur.Type()]; ok && c.isParam(cur, r, name) {
			// The parameter shadows anything outside. Nothing in this file
			// binds it — but a crossing may: the name may be a prop the caller
			// destructured, or a parameter the other side of a prop fills in at
			// its own call site. Both are tried, and a name that is genuinely
			// both is genuinely both.
			return c.crossOrStop(name, at, cur, depth, ReasonParam)
		}
	}
	return c.crossOrStop(name, at, scope, depth, ReasonNoBinding)
}

// crossOrStop is the fallback every unbound name takes: try the crossings in both
// directions, and report the local reason when neither applies.
func (c *ctx) crossOrStop(name string, at, scope *sitter.Node, depth int, reason string) Value {
	var vals []Value
	if v, ok := c.crossForward(name, at, depth); ok {
		vals = append(vals, v)
	}
	if v, ok := c.crossReverse(name, scope, depth); ok {
		vals = append(vals, v)
	}
	if len(vals) == 0 {
		return Opaque(c.originOf(at, reason))
	}
	return c.capUnion(at, Union(vals...))
}

// outward returns the next scope out from cur: the nearest enclosing scope
// node, or the file root once, or nil at the root. Walking outward is ordinary
// lexical resolution — a module-level constant read inside a function is bound,
// not unbound — and the chain stops at the file because crossing a file is a
// crossing rule, not a scope hop.
func (c *ctx) outward(cur *sitter.Node) *sitter.Node {
	if cur == c.root || cur == nil {
		return nil
	}
	if s := c.enclosingScope(cur); s != nil {
		return s
	}
	if c.root != nil {
		return c.root
	}
	return nil
}

// enclosingScope returns the nearest scope-introducing ancestor of n, strictly
// above it, or nil.
func (c *ctx) enclosingScope(n *sitter.Node) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := c.e.ix.scope[p.Type()]; ok {
			return p
		}
	}
	return nil
}

// bindingsIn collects every value bound to name inside scope, in source order.
//
// Bindings inside a nested scope that does not contain the use site are skipped
// — a callback's own `url` is not this call's. Nested blocks are entered, and
// must be: the branch arms that make a site multi-valued live in them, and
// refusing to look would leave exactly one visible binding and a confidently
// wrong single path.
func (c *ctx) bindingsIn(scope *sitter.Node, name string, use uint32) []*sitter.Node {
	var out []*sitter.Node
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n != scope && c.isScope(n) && !spans(n, use) {
			return
		}
		for _, r := range c.e.ix.binding[n.Type()] {
			nameNode := fieldOrChild(n, r.Name, r.NameChild)
			if !nameMatches(nameNode, c.src, name) {
				continue
			}
			v := fieldOrChild(n, r.Value, r.ValueChild)
			if v != nil && v.StartByte() < use {
				out = append(out, v)
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(scope)
	return out
}

// nameMatches compares a binding's name node against the name being looked up,
// accepting the quoted form as well as the bare one: an object member written
// `{"url": …}` binds the same name as one written `{url: …}`, and which of the
// two a grammar produces is not a fact the caller should have to know.
func nameMatches(n *sitter.Node, src []byte, name string) bool {
	if n == nil {
		return false
	}
	if n.Content(src) == name {
		return true
	}
	return innerText(n, src) == name
}

func (c *ctx) isScope(n *sitter.Node) bool {
	_, ok := c.e.ix.scope[n.Type()]
	return ok
}

// isParam reports whether name is one of scope's parameters. Both declared
// parameter fields are tried: a grammar that shapes a lone parameter
// differently from a parameter list is a quirk the spec declares, not an error.
func (c *ctx) isParam(scope *sitter.Node, r ScopeRule, name string) bool {
	for _, field := range []string{r.Params, r.ParamsAlt} {
		if field == "" {
			continue
		}
		params := scope.ChildByFieldName(field)
		if params == nil {
			continue
		}
		if paramNamed(params, c.src, name) {
			return true
		}
	}
	return false
}

// paramNamed reports whether the parameter list (or the single bare parameter)
// binds name. Every named leaf counts: a destructured or defaulted parameter
// still binds its identifiers, and over-reporting here costs an Opaque with
// ReasonParam where under-reporting would silently resolve a parameter to an
// outer binding of the same name.
func paramNamed(params *sitter.Node, src []byte, name string) bool {
	if params.NamedChildCount() == 0 {
		return params.Content(src) == name
	}
	found := false
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found || n == nil {
			return
		}
		if n.NamedChildCount() == 0 {
			if n.IsNamed() && n.Content(src) == name {
				found = true
			}
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(params)
	return found
}

// operatorMatches checks a rule's operator against the node's operator field,
// falling back to its anonymous children — grammars disagree about whether an
// operator is a field.
func (c *ctx) operatorMatches(n *sitter.Node, op string) bool {
	if op == "" {
		return true
	}
	if f := n.ChildByFieldName("operator"); f != nil {
		return f.Content(c.src) == op
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		if child := n.Child(i); child != nil && !child.IsNamed() && child.Content(c.src) == op {
			return true
		}
	}
	return false
}

func (c *ctx) originOf(n *sitter.Node, reason string) Origin {
	o := Origin{Reason: reason, File: c.file}
	if n != nil {
		o.Line = int(n.StartPoint().Row) + 1
		o.Text = truncate(n.Content(c.src), 120)
	}
	return o
}

// fieldOrChild addresses a child by field name, or by named-child index when
// the grammar exposes no field. See BindingRule.
func fieldOrChild(n *sitter.Node, field string, idx *int) *sitter.Node {
	if field != "" {
		return n.ChildByFieldName(field)
	}
	if idx == nil || *idx < 0 || *idx >= int(n.NamedChildCount()) {
		return nil
	}
	return n.NamedChild(*idx)
}

// innerChildRange returns the half-open child index range that excludes a
// node's delimiter tokens — the quotes, the backticks, the `f"` opener.
//
// Delimiters are a leaf first child and a leaf last child. Testing for leaves
// rather than for anonymity is what makes this work on both kinds of grammar:
// some make the quotes anonymous tokens, some give them named nodes
// (string_start / string_end), and neither fact belongs in this package.
func innerChildRange(n *sitter.Node) (lo, hi int) {
	cc := int(n.ChildCount())
	if cc >= 2 && isLeaf(n.Child(0)) && isLeaf(n.Child(cc-1)) {
		return 1, cc - 1
	}
	return 0, cc
}

func isLeaf(n *sitter.Node) bool { return n != nil && n.ChildCount() == 0 }

// innerText strips a string node's delimiters: the span between them.
func innerText(n *sitter.Node, src []byte) string {
	lo, hi := innerChildRange(n)
	if lo > 0 {
		from, to := n.Child(lo-1).EndByte(), n.Child(hi).StartByte()
		if to >= from && int(to) <= len(src) {
			return string(src[from:to])
		}
	}
	return trimDelimiters(n.Content(src))
}

func trimDelimiters(s string) string {
	if len(s) >= 2 {
		switch s[0] {
		case '"', '\'', '`':
			if s[len(s)-1] == s[0] {
				return s[1 : len(s)-1]
			}
		}
	}
	return s
}

func spans(n *sitter.Node, pos uint32) bool {
	return n.StartByte() <= pos && pos < n.EndByte()
}

func topOf(n *sitter.Node) *sitter.Node {
	cur := n
	for cur.Parent() != nil {
		cur = cur.Parent()
	}
	return cur
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
