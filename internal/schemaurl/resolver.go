package schemaurl

import (
	"path/filepath"
	"regexp"
	"sort"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
)

// Tier MS.1 + MS.2 — pin an entity from a discovered data asset's own
// vocabulary and resolve a URL that a JS/TS transport call site reads out of
// it, either directly (`<pinned>.<key>`) or through a learnt accessor
// function (`getCreateURL(schema, props)`).
//
// MS.0 (table.go) builds the per-service Table, learnt from a checked-in
// data file that names this service's real routes. This file consumes that
// table. It does NOT mint from the asset — the code call site stays the
// producer; the asset only answers "given this entity and this key, what
// path?". The receiver is pinned to an entity by the asset's vocabulary (no
// identifier, container path, or framework is hardcoded here — see the
// Genericity section of docs/schema-driven-url-resolution-plan.md), the key
// is read verbatim off the member expression or learnt from the accessor's
// body, and the verb comes from the call site, never the key name.

const (
	ledgerSchemaEntityUnresolved = "schema_entity_unresolved"
	ledgerSchemaEntityAmbiguous  = "schema_entity_ambiguous"
	ledgerSchemaKeyAmbiguous     = "schema_key_ambiguous"
	schemaURLOrigin              = "schema_asset"
	maxSchemaCopyUnwrap          = 1
	maxAccessorDepth             = 3
)

// LedgerSchemaEntityUnresolved etc. re-export the ledger-kind constants a
// caller (a hub provider, or internal/linker's ResolveJSLocalURLs) needs to
// name in an UnresolvedRef — kept unexported above and re-exported here so
// this file's own logic reads the short names.
const (
	LedgerSchemaEntityUnresolved = ledgerSchemaEntityUnresolved
	LedgerSchemaEntityAmbiguous  = ledgerSchemaEntityAmbiguous
	LedgerSchemaKeyAmbiguous     = ledgerSchemaKeyAmbiguous
	URLOrigin                    = schemaURLOrigin
)

var (
	phColon  = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
	phDollar = regexp.MustCompile(`^\$\{[^}]*\}$`)
	phBrace  = regexp.MustCompile(`^\{[^}]*\}$`)
	phAngle  = regexp.MustCompile(`^<[^>]*>$`)
)

// isPlaceholderLiteral reports whether s (a string literal's content, quotes
// stripped) is one of the four recognised path-placeholder spellings. MS.0b
// already wildcarded every placeholder in the stored path, so a `.replace`
// whose first argument is a placeholder does not change the resolved path.
func isPlaceholderLiteral(s string) bool {
	return phColon.MatchString(s) || phDollar.MatchString(s) ||
		phBrace.MatchString(s) || phAngle.MatchString(s)
}

// ── the resolver ─────────────────────────────────────────────────────────────

// schemaAccessor is a function whose body proves it returns one or more keys
// off a schema parameter. keys has more than one element for a conditional
// accessor (a fallback chain); it resolves at a call site only when every key
// yields the same path for the pinned entity.
type schemaAccessor struct {
	param int
	keys  []string
}

// Hit is a resolved schema URL read, with the provenance a reviewer needs to
// check the minted edge without re-deriving the pin.
type Hit struct {
	Path   string
	Entity string
	Key    string
	RawURL string
	File   string
}

// Resolver answers "does this URL expression read a discovered data asset,
// and if so what path?" for a JS/TS mint or patch site. It carries the
// per-service tables and the accessor functions it learnt from their bodies.
type Resolver struct {
	tables    map[string]*Table
	accessors map[string]map[string]schemaAccessor
}

// fnDef is a parsed function/arrow definition kept for accessor-learning.
type fnDef struct {
	node *sitter.Node
	src  []byte
}

// NewResolver discovers accessor functions in each service's JS/TS files by
// reading their bodies (MS.2a) and pairs them with the MS.0 tables. Returns
// nil when there are no tables.
func NewResolver(tables map[string]*Table, serviceFiles map[string][]string) *Resolver {
	if len(tables) == 0 {
		return nil
	}
	r := &Resolver{tables: tables, accessors: map[string]map[string]schemaAccessor{}}
	for svc := range tables {
		defs := map[string]fnDef{}
		for _, abs := range serviceFiles[svc] {
			if !jsast.IsJSFile(abs) {
				continue
			}
			if jsast.IsTestFile(filepath.ToSlash(abs)) {
				continue
			}
			src, root, _, ok := jsast.Parse(abs)
			if !ok {
				continue
			}
			indexFnDefs(root, src, func(name string, fn *sitter.Node) {
				if _, exists := defs[name]; !exists {
					defs[name] = fnDef{node: fn, src: src}
				}
			})
		}
		learnt := map[string]schemaAccessor{}
		names := make([]string, 0, len(defs))
		for n := range defs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if acc, ok := classifyAccessor(n, defs, learnt, map[string]bool{}, 0); ok {
				learnt[n] = acc
			}
		}
		if len(learnt) > 0 {
			r.accessors[svc] = learnt
		}
	}
	return r
}

// LearntAccessorCount reports how many accessor functions were learnt,
// summed across services — for the acceptance table.
func (r *Resolver) LearntAccessorCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, m := range r.accessors {
		n += len(m)
	}
	return n
}

// EntityPin is one (entity, key) -> path row from a service's discovered
// schema asset table — the same rows Lookup queries against, exposed so a
// caller (Tier RC.4, docs/js-declarative-composition-cluster-plan.md) can
// dump the whole table as facts instead of resolving one expression at a
// time.
type EntityPin struct {
	Entity string
	Key    string
	Path   string
	File   string // the discovered asset, relative to cwd
}

// EntityPins returns every (entity, key) -> path row for svc's discovered
// table, sorted by (Entity, Key) for determinism. Returns nil if r or svc's
// table is nil.
func (r *Resolver) EntityPins(svc string) []EntityPin {
	if r == nil {
		return nil
	}
	tbl := r.tables[svc]
	if tbl == nil {
		return nil
	}
	var out []EntityPin
	for entity, keys := range tbl.ByEntity {
		for key, e := range keys {
			out = append(out, EntityPin{Entity: entity, Key: key, Path: e.Path, File: tbl.File})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Entity != out[j].Entity {
			return out[i].Entity < out[j].Entity
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// PropsKeyRead reports whether expr — after the same options-object/local-
// binding unwrap and `.replace(placeholder, …)` strip ResolveURLExpr applies
// internally — is exactly `this.props.<Prop>.<Key>` (or the bracket-string
// equivalent): a read this resolver cannot pin on its own, because the
// entity lives on whichever component rendered <Prop> as a JSX attribute,
// not in svc's function-local pins. Tier RC.5 (MS.3 kind 2,
// docs/js-declarative-composition-cluster-plan.md) uses this to find sites
// that need a cross-component join before calling PinEntity on the
// PRODUCER's attribute value; every other shape ResolveURLExpr already
// covers reports ok=false here, so a caller cannot double-resolve a site.
//
// consumerNode is the `this.props.<Prop>` member expression itself, not the
// bare property name — internal/valuegraph/javascript.yaml's jsx_attribute
// crossing rule's consumer pattern (`roots: [this.props, props]`) matches
// against that whole shape, the same node a caller must hand
// valuegraphfacts.Resolve as the Site's Expr.
func (r *Resolver) PropsKeyRead(expr, fn *sitter.Node, src []byte) (prop, key string, consumerNode *sitter.Node, ok bool) {
	if r == nil || expr == nil {
		return "", "", nil, false
	}
	expr = schemaUnwrapValue(expr, fn, src, 0)
	if expr == nil {
		return "", "", nil, false
	}
	base, poisoned := schemaStripReplace(expr, src)
	if poisoned || base == nil {
		return "", "", nil, false
	}
	recv, k := schemaSplitKeyRead(base, src)
	if recv == nil || k == "" || recv.Type() != "member_expression" {
		return "", "", nil, false
	}
	obj := recv.ChildByFieldName("object")
	propNode := recv.ChildByFieldName("property")
	if obj == nil || propNode == nil || propNode.Type() != "property_identifier" {
		return "", "", nil, false
	}
	if obj.Type() != "member_expression" {
		return "", "", nil, false
	}
	innerObj := obj.ChildByFieldName("object")
	innerProp := obj.ChildByFieldName("property")
	if innerObj == nil || innerProp == nil || innerObj.Type() != "this" || innerProp.Content(src) != "props" {
		return "", "", nil, false
	}
	return propNode.Content(src), k, recv, true
}

// PinEntity pins expr to an entity name using svc's discovered table's own
// vocabulary — the same logic ResolveURLExpr uses internally on a
// same-function receiver, exposed so a caller (RC.5) can apply it to a
// PRODUCER's JSX attribute value instead. fn is the enclosing function of
// expr (nil is fine — schemaFunctionPins returns no pins, same as a bare
// literal-ending expression). ambiguous is true when expr is a bound name
// that resolved to two different entities in fn — same "lost track"
// semantics as ResolveURLExpr's ledgerSchemaEntityAmbiguous.
func (r *Resolver) PinEntity(svc string, expr, fn *sitter.Node, src []byte) (entity string, ambiguous bool) {
	if r == nil || expr == nil {
		return "", false
	}
	tbl := r.tables[svc]
	if tbl == nil {
		return "", false
	}
	pins, amb := schemaFunctionPins(fn, src, tbl)
	e := schemaPinExprEntity(expr, src, tbl, pins, 0)
	if e == "" {
		if nm := schemaIdentName(expr, src); nm != "" && amb[nm] {
			return "", true
		}
		return "", false
	}
	return e, false
}

// TableFile returns the discovered schema asset's path for svc — the same
// value EntityPins/Lookup's Hit already carry per row, exposed bare so a
// caller building its own Hit from a Lookup (RC.5) can stamp the same
// provenance without re-deriving it.
func (r *Resolver) TableFile(svc string) string {
	if r == nil {
		return ""
	}
	tbl := r.tables[svc]
	if tbl == nil {
		return ""
	}
	return tbl.File
}

// Lookup answers svc's table for (entity, key) -> path — the same lookup
// ResolveURLExpr performs internally, exposed so a caller (RC.5) that pinned
// an entity through a different path (a cross-component join, not a
// same-function receiver) can still resolve through the SAME table.
func (r *Resolver) Lookup(svc, entity, key string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	tbl := r.tables[svc]
	if tbl == nil {
		return Entry{}, false
	}
	return tbl.Lookup(entity, key)
}

// ResolveURLExpr tries to resolve expr — a URL argument, or the value of an
// options object's url key — at a call site in service svc, with fn the
// enclosing function. ok is true with a hit when it resolved; ledgerKind is
// non-empty when the site should be ledgered; both empty means "not a schema
// expression, carry on".
func (r *Resolver) ResolveURLExpr(expr, fn *sitter.Node, src []byte, svc string) (hit Hit, ok bool, ledgerKind string) {
	if r == nil || expr == nil {
		return Hit{}, false, ""
	}
	tbl := r.tables[svc]
	if tbl == nil {
		return Hit{}, false, ""
	}
	expr = schemaUnwrapValue(expr, fn, src, 0)
	if expr == nil {
		return Hit{}, false, ""
	}

	base, poisoned := schemaStripReplace(expr, src)
	if poisoned {
		return Hit{}, false, ledgerSchemaEntityUnresolved
	}

	pins, ambiguous := schemaFunctionPins(fn, src, tbl)

	// Direct key read: <recv>.<key> / <recv>["<key>"].
	if recv, key := schemaSplitKeyRead(base, src); recv != nil && key != "" {
		entity := schemaPinExprEntity(recv, src, tbl, pins, 0)
		if entity == "" {
			if nm := schemaIdentName(recv, src); nm != "" && ambiguous[nm] {
				return Hit{}, false, ledgerSchemaEntityAmbiguous
			}
			if schemaKeyInTable(tbl, key) {
				return Hit{}, false, ledgerSchemaEntityUnresolved
			}
			return Hit{}, false, ""
		}
		e, found := tbl.Lookup(entity, key)
		if !found {
			return Hit{}, false, ""
		}
		return Hit{Path: e.Path, Entity: entity, Key: key, RawURL: e.Raw, File: tbl.File}, true, ""
	}

	// Accessor call: getCreateURL(schema, props).
	if base != nil && base.Type() == "call_expression" {
		return r.resolveAccessorCall(base, fn, src, svc, tbl, pins, ambiguous)
	}
	return Hit{}, false, ""
}

func (r *Resolver) resolveAccessorCall(call, fn *sitter.Node, src []byte, svc string, tbl *Table, pins map[string]string, ambiguous map[string]bool) (Hit, bool, string) {
	name := calleeName(call, src)
	acc, isAcc := r.accessors[svc][name]
	if !isAcc {
		return Hit{}, false, ""
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || acc.param >= int(args.NamedChildCount()) {
		return Hit{}, false, ledgerSchemaEntityUnresolved
	}
	schemaArg := args.NamedChild(acc.param)
	entity := schemaPinExprEntity(schemaArg, src, tbl, pins, 0)
	if entity == "" {
		if nm := schemaIdentName(schemaArg, src); nm != "" && ambiguous[nm] {
			return Hit{}, false, ledgerSchemaEntityAmbiguous
		}
		return Hit{}, false, ledgerSchemaEntityUnresolved
	}
	var chosen Entry
	paths := map[string]bool{}
	for _, k := range acc.keys {
		if e, found := tbl.Lookup(entity, k); found {
			paths[e.Path] = true
			chosen = e
		}
	}
	switch len(paths) {
	case 0:
		return Hit{}, false, ""
	case 1:
		return Hit{Path: chosen.Path, Entity: entity, Key: chosen.Key, RawURL: chosen.Raw, File: tbl.File}, true, ""
	default:
		return Hit{}, false, ledgerSchemaKeyAmbiguous
	}
}

// schemaUnwrapValue peels an options-object wrapper and one level of local
// binding so `{ url: <expr> }` and `const u = <expr>` both reach <expr>.
func schemaUnwrapValue(n, fn *sitter.Node, src []byte, depth int) *sitter.Node {
	if n == nil || depth > 3 {
		return n
	}
	switch n.Type() {
	case "parenthesized_expression":
		if c := n.NamedChild(0); c != nil {
			return schemaUnwrapValue(c, fn, src, depth+1)
		}
	case "object":
		for _, k := range []string{"url", "path", "href"} {
			if v := jsast.ObjectKeyValue(n, src, k); v != nil {
				return schemaUnwrapValue(v, fn, src, depth+1)
			}
		}
		return nil
	case "identifier", "shorthand_property_identifier", "property_identifier":
		if fn == nil {
			return n
		}
		nm := n.Content(src)
		rhs := jsast.LocalAssignments(fn, n.StartByte(), src, nm)
		if len(rhs) == 1 {
			return schemaUnwrapValue(rhs[0], fn, src, depth+1)
		}
	}
	return n
}

// ── accessor learning (MS.2a) ────────────────────────────────────────────────

// classifyAccessor reads name's body and reports whether it is a schema
// accessor: every return expression reduces to a read of one parameter's
// key(s), directly / through a path-neutral wrapper call / through another
// accessor.
func classifyAccessor(name string, defs map[string]fnDef, learnt map[string]schemaAccessor, visiting map[string]bool, depth int) (schemaAccessor, bool) {
	if acc, ok := learnt[name]; ok {
		return acc, true
	}
	if depth >= maxAccessorDepth || visiting[name] {
		return schemaAccessor{}, false
	}
	def, ok := defs[name]
	if !ok {
		return schemaAccessor{}, false
	}
	visiting[name] = true
	defer delete(visiting, name)

	params := fnParamNames(def.node, def.src)
	if len(params) == 0 {
		return schemaAccessor{}, false
	}
	rets := fnReturnExprs(def.node)
	if len(rets) == 0 {
		return schemaAccessor{}, false
	}
	param := -1
	keyset := map[string]bool{}
	for _, ret := range rets {
		p, keys, ok := reduceAccessorExpr(ret, params, def.src, defs, learnt, visiting, depth)
		if !ok {
			return schemaAccessor{}, false
		}
		if p >= 0 {
			if param == -1 {
				param = p
			} else if p != param {
				return schemaAccessor{}, false
			}
		}
		for _, k := range keys {
			keyset[k] = true
		}
	}
	if param == -1 || len(keyset) == 0 {
		return schemaAccessor{}, false
	}
	keys := make([]string, 0, len(keyset))
	for k := range keyset {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return schemaAccessor{param: param, keys: keys}, true
}

// reduceAccessorExpr reduces one return expression. p is the parameter index
// the keys are read off (-1 = this branch contributes no path, e.g. `|| ""`).
func reduceAccessorExpr(expr *sitter.Node, params []string, src []byte, defs map[string]fnDef, learnt map[string]schemaAccessor, visiting map[string]bool, depth int) (p int, keys []string, ok bool) {
	expr = unwrapParens(expr)
	if expr == nil {
		return -1, nil, false
	}
	switch expr.Type() {
	case "string", "template_string", "number", "null", "undefined":
		return -1, nil, true // contributes no path (fallback default)
	case "member_expression", "subscript_expression":
		recv, key := schemaSplitKeyRead(expr, src)
		if recv == nil || recv.Type() != "identifier" || key == "" {
			return -1, nil, false
		}
		if idx := indexOf(params, recv.Content(src)); idx >= 0 {
			return idx, []string{key}, true
		}
		return -1, nil, false
	case "binary_expression":
		op := ""
		if o := expr.ChildByFieldName("operator"); o != nil {
			op = o.Content(src)
		}
		left := expr.ChildByFieldName("left")
		right := expr.ChildByFieldName("right")
		if op == "||" {
			return mergeAccessorBranches(
				[]*sitter.Node{left, right}, params, src, defs, learnt, visiting, depth)
		}
		if op == "&&" {
			return reduceAccessorExpr(right, params, src, defs, learnt, visiting, depth)
		}
		return -1, nil, false
	case "ternary_expression", "conditional_expression":
		return mergeAccessorBranches(
			[]*sitter.Node{expr.ChildByFieldName("consequence"), expr.ChildByFieldName("alternative")},
			params, src, defs, learnt, visiting, depth)
	case "call_expression":
		return reduceAccessorCall(expr, params, src, defs, learnt, visiting, depth)
	}
	return -1, nil, false
}

// mergeAccessorBranches reduces the arms of a `||` / ternary fallback chain.
// An arm it cannot read (an array-valued key selected by a runtime
// discriminator, a call it does not recognise) is *tolerated* — the chain is
// a fallback and a real accessor legitimately falls back to a shape this
// analysis will not resolve. The classification still fails if two arms read
// different parameters, or if no arm reduces at all.
func mergeAccessorBranches(branches []*sitter.Node, params []string, src []byte, defs map[string]fnDef, learnt map[string]schemaAccessor, visiting map[string]bool, depth int) (int, []string, bool) {
	param := -1
	var keys []string
	okAny := false
	for _, b := range branches {
		if b == nil {
			continue
		}
		p, ks, ok := reduceAccessorExpr(b, params, src, defs, learnt, visiting, depth)
		if !ok {
			continue
		}
		okAny = true
		if p >= 0 {
			if param == -1 {
				param = p
			} else if p != param {
				return -1, nil, false
			}
			keys = append(keys, ks...)
		}
	}
	if !okAny {
		return -1, nil, false
	}
	return param, keys, true
}

// reduceAccessorCall covers a call that returns a key: another accessor on
// the same param, or a path-neutral wrapper with exactly one key-read
// argument.
func reduceAccessorCall(call *sitter.Node, params []string, src []byte, defs map[string]fnDef, learnt map[string]schemaAccessor, visiting map[string]bool, depth int) (int, []string, bool) {
	name := calleeName(call, src)
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return -1, nil, false
	}
	var argNodes []*sitter.Node
	for i := 0; i < int(args.NamedChildCount()); i++ {
		argNodes = append(argNodes, args.NamedChild(i))
	}

	// A call of another accessor on one of our params.
	if acc, ok := classifyAccessor(name, defs, learnt, visiting, depth+1); ok {
		if acc.param < len(argNodes) {
			a := argNodes[acc.param]
			if a.Type() == "identifier" {
				if idx := indexOf(params, a.Content(src)); idx >= 0 {
					return idx, acc.keys, true
				}
			}
		}
		return -1, nil, false
	}

	// A path-neutral wrapper: exactly one argument is a key read on a param,
	// and every other argument is a param identifier, an object, or a
	// literal — so it cannot change which path is returned.
	keyArgParam, keyArgKeys := -1, []string(nil)
	for _, a := range argNodes {
		p, ks, ok := reduceAccessorExpr(a, params, src, defs, learnt, visiting, depth)
		if ok && p >= 0 {
			if keyArgParam != -1 {
				return -1, nil, false // two key-read arguments: ambiguous
			}
			keyArgParam, keyArgKeys = p, ks
			continue
		}
		switch a.Type() {
		case "identifier":
			if indexOf(params, a.Content(src)) < 0 {
				return -1, nil, false
			}
		case "object", "string", "number", "null", "undefined", "member_expression":
			// inert
		default:
			return -1, nil, false
		}
	}
	if keyArgParam < 0 {
		return -1, nil, false
	}
	return keyArgParam, keyArgKeys, true
}

// ── shared helpers ───────────────────────────────────────────────────────────

func unwrapParens(n *sitter.Node) *sitter.Node {
	for n != nil && n.Type() == "parenthesized_expression" {
		n = n.NamedChild(0)
	}
	return n
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// calleeName returns the name a call is made through: a bare identifier, or
// the trailing property of a member expression
// (`Validation.getCreateURL` -> "getCreateURL").
func calleeName(call *sitter.Node, src []byte) string {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		return fn.Content(src)
	case "member_expression":
		if p := fn.ChildByFieldName("property"); p != nil {
			return p.Content(src)
		}
	}
	return ""
}

// fnParamNames returns the parameter identifiers of a function/arrow node, in
// order. A destructured or rest parameter contributes an empty string so
// index positions stay aligned.
func fnParamNames(fn *sitter.Node, src []byte) []string {
	var out []string
	ps := fn.ChildByFieldName("parameters")
	if ps == nil {
		// arrow with a single unparenthesised param
		if p := fn.ChildByFieldName("parameter"); p != nil && p.Type() == "identifier" {
			return []string{p.Content(src)}
		}
		return nil
	}
	for i := 0; i < int(ps.NamedChildCount()); i++ {
		c := ps.NamedChild(i)
		switch c.Type() {
		case "identifier":
			out = append(out, c.Content(src))
		case "required_parameter", "optional_parameter":
			if pat := c.ChildByFieldName("pattern"); pat != nil && pat.Type() == "identifier" {
				out = append(out, pat.Content(src))
			} else {
				out = append(out, "")
			}
		case "assignment_pattern":
			if l := c.ChildByFieldName("left"); l != nil && l.Type() == "identifier" {
				out = append(out, l.Content(src))
			} else {
				out = append(out, "")
			}
		default:
			out = append(out, "")
		}
	}
	return out
}

// fnReturnExprs collects every returned expression in a function body (and
// the expression body of an arrow), not descending into nested functions.
func fnReturnExprs(fn *sitter.Node) []*sitter.Node {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	if body.Type() != "statement_block" {
		return []*sitter.Node{body}
	}
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration", "function_expression", "arrow_function", "function", "generator_function", "generator_function_declaration":
			return
		case "return_statement":
			if n.NamedChildCount() > 0 {
				out = append(out, n.NamedChild(0))
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

// indexFnDefs emits every named function definition in a file:
// `function f(){}`, `const f = () => {}`, and class-field arrows.
func indexFnDefs(root *sitter.Node, src []byte, emit func(name string, fn *sitter.Node)) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration", "generator_function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(src), n)
			}
		case "variable_declarator", "public_field_definition", "field_definition":
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression", "function":
					if nm := n.ChildByFieldName("name"); nm != nil {
						emit(nm.Content(src), v)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

// ── existing-node mutation helpers ──────────────────────────────────────────

// ApplyURL writes a resolved hit onto an existing node and retires the
// dynamic markers.
func ApplyURL(n *graph.Node, hit Hit, verb string) {
	if n.Meta == nil {
		n.Meta = map[string]string{}
	}
	n.Meta["url"] = hit.Path
	n.Meta["url_origin"] = schemaURLOrigin
	n.Meta["schema_file"] = hit.File
	n.Meta["schema_entity"] = hit.Entity
	n.Meta["schema_key"] = hit.Key
	n.Meta["schema_url_raw"] = hit.RawURL
	delete(n.Meta, "key_dynamic")
	delete(n.Meta, "key_dynamic_raw")
	delete(n.Meta, "key_candidates")
	n.Label = verb + " " + hit.Path
}

// MintMeta returns the provenance Meta a fresh mint site should carry for a
// schema hit.
func MintMeta(hit Hit) map[string]string {
	return map[string]string{
		"url_origin":     schemaURLOrigin,
		"schema_file":    hit.File,
		"schema_entity":  hit.Entity,
		"schema_key":     hit.Key,
		"schema_url_raw": hit.RawURL,
	}
}

// ── entity pinning ───────────────────────────────────────────────────────────

// schemaStripReplace peels trailing `.replace(<placeholder>, x)` calls off an
// expression (MS.1c: parameter substitution on an already-wildcarded path).
// poisoned is true when a `.replace` first argument is not a recognised
// placeholder literal.
func schemaStripReplace(expr *sitter.Node, src []byte) (base *sitter.Node, poisoned bool) {
	cur := expr
	for cur != nil && cur.Type() == "call_expression" {
		fnNode := cur.ChildByFieldName("function")
		if fnNode == nil || fnNode.Type() != "member_expression" {
			return cur, false
		}
		prop := fnNode.ChildByFieldName("property")
		if prop == nil || prop.Content(src) != "replace" {
			return cur, false
		}
		args := cur.ChildByFieldName("arguments")
		var first *sitter.Node
		if args != nil && args.NamedChildCount() > 0 {
			first = args.NamedChild(0)
		}
		if first == nil || first.Type() != "string" || !isPlaceholderLiteral(schemaStringContent(first, src)) {
			return nil, true
		}
		cur = fnNode.ChildByFieldName("object")
	}
	return cur, false
}

// schemaSplitKeyRead splits `<recv>.<key>` (member) or `<recv>["<key>"]`
// (string subscript) into the receiver node and the literal key.
func schemaSplitKeyRead(n *sitter.Node, src []byte) (recv *sitter.Node, key string) {
	if n == nil {
		return nil, ""
	}
	switch n.Type() {
	case "member_expression":
		prop := n.ChildByFieldName("property")
		if prop == nil || prop.Type() != "property_identifier" {
			return nil, ""
		}
		return n.ChildByFieldName("object"), prop.Content(src)
	case "subscript_expression":
		idx := n.ChildByFieldName("index")
		if idx == nil || idx.Type() != "string" {
			return nil, ""
		}
		return n.ChildByFieldName("object"), schemaStringContent(idx, src)
	}
	return nil, ""
}

// schemaPinExprEntity pins an expression to an entity name using the asset's
// vocabulary (table.Entities()): a member chain / string subscript /
// single-string-literal call ending in an entity literal, a bare identifier
// resolved through the function's pins, or one level of copy-wrapper unwrap.
func schemaPinExprEntity(n *sitter.Node, src []byte, tbl *Table, pins map[string]string, depth int) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier":
		return pins[n.Content(src)]
	case "member_expression":
		prop := n.ChildByFieldName("property")
		if prop != nil && prop.Type() == "property_identifier" && tbl.hasEntity(prop.Content(src)) {
			return prop.Content(src)
		}
	case "subscript_expression":
		idx := n.ChildByFieldName("index")
		if idx != nil && idx.Type() == "string" {
			if s := schemaStringContent(idx, src); tbl.hasEntity(s) {
				return s
			}
		}
	case "call_expression":
		args := n.ChildByFieldName("arguments")
		if args == nil {
			return ""
		}
		if args.NamedChildCount() == 1 && args.NamedChild(0).Type() == "string" {
			if s := schemaStringContent(args.NamedChild(0), src); tbl.hasEntity(s) {
				return s
			}
		}
		if depth < maxSchemaCopyUnwrap && args.NamedChildCount() == 1 {
			return schemaPinExprEntity(args.NamedChild(0), src, tbl, pins, depth+1)
		}
	}
	return ""
}

// schemaFunctionPins records, per binding name in fn, the entity it was
// pinned to (MS.1a). A name pinned to two different entities in one function
// is not a branch — the analysis lost track; it is returned in ambiguous and
// resolves to nothing.
func schemaFunctionPins(fn *sitter.Node, src []byte, tbl *Table) (pins map[string]string, ambiguous map[string]bool) {
	pins = map[string]string{}
	ambiguous = map[string]bool{}
	if fn == nil {
		return pins, ambiguous
	}
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n != fn && jsast.FnTypes[n.Type()] {
			return
		}
		var name, val *sitter.Node
		switch n.Type() {
		case "variable_declarator":
			name, val = n.ChildByFieldName("name"), n.ChildByFieldName("value")
		case "assignment_expression":
			name, val = n.ChildByFieldName("left"), n.ChildByFieldName("right")
		}
		if name != nil && name.Type() == "identifier" && val != nil {
			nm := name.Content(src)
			e := schemaPinExprEntity(val, src, tbl, nil, 0)
			switch {
			case e == "":
				if _, isPin := pins[nm]; isPin {
					ambiguous[nm] = true
				}
			default:
				if prev, ok := pins[nm]; ok && prev != e {
					ambiguous[nm] = true
				}
				pins[nm] = e
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(fn)
	for nm := range ambiguous {
		delete(pins, nm)
	}
	return pins, ambiguous
}

func schemaKeyInTable(t *Table, key string) bool {
	for _, keys := range t.ByEntity {
		if _, ok := keys[key]; ok {
			return true
		}
	}
	return false
}

func schemaIdentName(n *sitter.Node, src []byte) string {
	if n != nil && n.Type() == "identifier" {
		return n.Content(src)
	}
	return ""
}

func schemaStringContent(n *sitter.Node, src []byte) string {
	s := n.Content(src)
	if len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}
