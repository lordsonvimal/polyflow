package linker

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier MS.1 — pin an entity from a discovered data asset's own vocabulary and
// resolve a direct `<pinned>.<key>` read at a JS/TS transport call site.
//
// MS.0 built the per-service SchemaURLTable: entity -> key -> path, learnt from a
// checked-in data file that names this service's real routes. This pass consumes
// that table. It does NOT mint from the asset — the code call site stays the
// producer; the asset only answers "given this entity and this key, what path?".
// The receiver is pinned to an entity by the asset's vocabulary (no identifier,
// container path, or framework is hardcoded here — see the Genericity section of
// docs/schema-driven-url-resolution-plan.md), the key is read verbatim off the
// member expression, and the verb comes from the call site, never the key name.

const (
	ledgerSchemaEntityUnresolved = "schema_entity_unresolved"
	ledgerSchemaEntityAmbiguous  = "schema_entity_ambiguous"
	schemaURLOrigin              = "schema_asset"
	maxSchemaCopyUnwrap          = 1
)

var (
	phColon  = regexp.MustCompile(`^:[A-Za-z_][A-Za-z0-9_]*$`)
	phDollar = regexp.MustCompile(`^\$\{[^}]*\}$`)
	phBrace  = regexp.MustCompile(`^\{[^}]*\}$`)
	phAngle  = regexp.MustCompile(`^<[^>]*>$`)
)

// isPlaceholderLiteral reports whether s (a string literal's content, quotes
// stripped) is one of the four recognised path-placeholder spellings. MS.0b
// already wildcarded every placeholder in the stored path, so a `.replace` whose
// first argument is a placeholder does not change the resolved path.
func isPlaceholderLiteral(s string) bool {
	return phColon.MatchString(s) || phDollar.MatchString(s) ||
		phBrace.MatchString(s) || phAngle.MatchString(s)
}

// ResolveSchemaURLs reads the URL of every JS/TS http_client the matcher left
// dynamic whose URL expression is a direct read of a discovered asset
// (`schema.<entity>.<key>`, `r.<key>` with `r` pinned in the enclosing
// function). A resolved site has its existing node rewritten in place, carrying
// the provenance Meta keys a reviewer needs to check the edge without
// re-deriving the pin. Must run after the schema_url_tables pass and before the
// contract engine.
func ResolveSchemaURLs(nodes []graph.Node, tables map[string]*SchemaURLTable) (changed []graph.Node, ledger []graph.UnresolvedRef) {
	if len(tables) == 0 {
		return nil, nil
	}
	fileCache := make(map[string]*jsHostFile)
	for i := range nodes {
		n := &nodes[i]
		raw, ok := schemaURLCandidate(n)
		if !ok {
			continue
		}
		tbl := tables[n.Service]
		if tbl == nil {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			jf = parseJSHostFile(n.File)
			fileCache[n.File] = jf
		}
		if jf == nil {
			continue
		}
		expr := jf.exprAtLine(n.Line, raw)
		if expr == nil {
			continue
		}
		fn := enclosingJSFunction(expr)
		entry, entity, key, kind := schemaResolveExpr(expr, fn, jf.src, tbl)
		if kind != "" {
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: key, Kind: kind,
			})
			continue
		}
		if entity == "" {
			continue // not a schema read, or a key this asset does not declare
		}
		verb := strings.ToUpper(n.Meta["method"])
		if verb == "" {
			// The verb comes from the call site, never the key name. Naming is
			// not semantics (see the Core model). No readable verb -> ledger.
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: key, Kind: ledgerSchemaEntityUnresolved,
			})
			continue
		}
		applySchemaURL(n, entry, tbl.File, entity, key, verb)
		changed = append(changed, *n)
	}
	return changed, ledger
}

// schemaURLCandidate reports whether n is a JS/TS http_client whose URL the
// matcher could not read, and returns the raw source text it gave up on.
func schemaURLCandidate(n *graph.Node) (raw string, ok bool) {
	if n.Type != graph.NodeTypeHTTPClient || n.File == "" {
		return "", false
	}
	if n.Language != "javascript" && n.Language != "typescript" {
		return "", false
	}
	if n.Meta["url"] != "" || n.Meta["path"] != "" {
		return "", false
	}
	if n.Meta["url_origin"] == schemaURLOrigin {
		return "", false // idempotent re-run
	}
	if n.Meta["key_dynamic"] != "true" {
		return "", false
	}
	raw = n.Meta["key_dynamic_raw"]
	if raw == "" || raw == "(attached)" {
		return "", false
	}
	return raw, true
}

// schemaResolveExpr resolves a URL expression to a table entry. It returns a
// non-empty kind when the site must be ledgered, an empty entity when the
// expression is not a schema read at all (discard silently), or the entry with
// its entity and key when it resolves.
func schemaResolveExpr(expr, fn *sitter.Node, src []byte, tbl *SchemaURLTable) (entry schemaURLEntry, entity, key, kind string) {
	base, poisoned := schemaStripReplace(expr, src)
	if poisoned {
		return schemaURLEntry{}, "", "", ledgerSchemaEntityUnresolved
	}
	recv, key := schemaSplitKeyRead(base, src)
	if recv == nil || key == "" {
		return schemaURLEntry{}, "", "", ""
	}

	pins, ambiguous := schemaFunctionPins(fn, src, tbl)
	entity = schemaPinExprEntity(recv, src, tbl, pins, 0)
	if entity == "" {
		if name := schemaIdentName(recv, src); name != "" && ambiguous[name] {
			return schemaURLEntry{}, "", key, ledgerSchemaEntityAmbiguous
		}
		if schemaKeyInTable(tbl, key) {
			// An unpinned receiver read with a key this asset declares: the
			// tier's own blind-spot report, and how rung 4 stays visible.
			return schemaURLEntry{}, "", key, ledgerSchemaEntityUnresolved
		}
		return schemaURLEntry{}, "", "", ""
	}

	e, ok := tbl.Lookup(entity, key)
	if !ok {
		return schemaURLEntry{}, "", "", ""
	}
	return e, entity, key, ""
}

// schemaStripReplace peels trailing `.replace(<placeholder>, x)` calls off an
// expression (MS.1c: parameter substitution on an already-wildcarded path).
// poisoned is true when a `.replace` has a first argument that is not a
// recognised placeholder literal — a regex or an arbitrary string that could
// change the path.
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
// vocabulary (table.Entities()). The three literal shapes — member chain ending
// in an entity literal, string subscript, single-string-literal call — plus a
// bare identifier resolved through the function's pins, plus one level of
// copy-wrapper unwrap.
func schemaPinExprEntity(n *sitter.Node, src []byte, tbl *SchemaURLTable, pins map[string]string, depth int) string {
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
			s := schemaStringContent(idx, src)
			if tbl.hasEntity(s) {
				return s
			}
		}
	case "call_expression":
		args := n.ChildByFieldName("arguments")
		if args == nil {
			return ""
		}
		// getSchema("widget") — a call with a single string-literal argument.
		if args.NamedChildCount() == 1 && args.NamedChild(0).Type() == "string" {
			s := schemaStringContent(args.NamedChild(0), src)
			if tbl.hasEntity(s) {
				return s
			}
		}
		// clone(x.widget) — one level of copy-wrapper unwrap: exactly one argument,
		// itself a pin expression, and nothing else that could change which
		// entity is returned.
		if depth < maxSchemaCopyUnwrap && args.NamedChildCount() == 1 {
			return schemaPinExprEntity(args.NamedChild(0), src, tbl, pins, depth+1)
		}
	}
	return ""
}

// schemaFunctionPins records, per binding name in fn, the entity it was pinned
// to (MS.1a). A name pinned to two different entities in one function is not a
// branch — it means the analysis lost track; it is returned in ambiguous and
// resolves to nothing.
func schemaFunctionPins(fn *sitter.Node, src []byte, tbl *SchemaURLTable) (pins map[string]string, ambiguous map[string]bool) {
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
		if n != fn && localURLFnTypes[n.Type()] {
			return // a sibling/nested function's binding is not this one's
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
				// A write that is not a pin. If the name was ever a pin, the
				// binding no longer reliably names one entity.
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

func (t *SchemaURLTable) hasEntity(name string) bool {
	if _, ok := t.ByEntity[name]; ok {
		return true
	}
	_, ok := t.Aliases[name]
	return ok
}

func schemaKeyInTable(t *SchemaURLTable, key string) bool {
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

// schemaStringContent returns a string literal's content with surrounding
// quotes removed.
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

// applySchemaURL writes the resolved path and its provenance onto a node and
// retires the dynamic markers, so the contract engine sees an ordinary readable
// client and the ledger check does not double-count a site since resolved.
func applySchemaURL(n *graph.Node, entry schemaURLEntry, file, entity, key, verb string) {
	n.Meta = ensureMeta(n.Meta)
	n.Meta["url"] = entry.Path
	n.Meta["url_origin"] = schemaURLOrigin
	n.Meta["schema_file"] = file
	n.Meta["schema_entity"] = entity
	n.Meta["schema_key"] = key
	n.Meta["schema_url_raw"] = entry.Raw
	delete(n.Meta, "key_dynamic")
	delete(n.Meta, "key_dynamic_raw")
	delete(n.Meta, "key_candidates")
	n.Label = verb + " " + entry.Path
}
