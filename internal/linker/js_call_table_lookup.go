package linker

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// js_call_table_lookup.go closes a generic gap left by
// contract/keywalk_javascript_local.go's walkJSCallExpr: that inliner only
// follows a call to a SAME-FILE function whose body is a single return
// statement (jsSingleReturnExpr) — a call to an imported helper, or a
// same-file helper whose body indexes an object-literal table by one of its
// own parameters (`function generateURL(name) { return ROUTES[name]; }`),
// falls through to key_dynamic there because neither cross-file resolution
// nor argument-to-table-key substitution exists inside that single-expression
// walker.
//
// This is a post-parse linker pass instead, for the same structural reason
// LinkJSAPIWrapperCalls is one (see its own doc comment): the parser's
// per-file KeyWalker call runs before any other file exists to read, so
// resolving a cross-file callee's body — or a same-file table one hop behind
// an import — has to happen after every file is indexed, by re-parsing.
//
// The shape matched is purely structural, not tied to any framework or
// library name: a function whose entire body is `return TABLE[param]` (param
// one of its own parameters, TABLE a plain identifier bound to an object
// literal — locally, or one import hop away), called elsewhere with a literal
// string argument at that parameter's position. Any call site the parser
// already flagged key_dynamic at that exact (service, file, line) has its
// value patched in and the dynamic flag cleared; nothing is newly minted.
func LinkJSCallTableLookups(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	svcFileSet := make(map[string]map[string]bool, len(serviceFiles))
	for svc, files := range serviceFiles {
		s := make(map[string]bool, len(files))
		for _, f := range files {
			if isJSFile(f) {
				s[f] = true
			}
		}
		svcFileSet[svc] = s
	}

	// service -> funcName -> lookup fact
	fnTable := map[string]map[string]jsTableLookupFn{}
	for svc, files := range serviceFiles {
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			for name, fn := range discoverJSTableLookupFns(file, svcFileSet[svc]) {
				if fnTable[svc] == nil {
					fnTable[svc] = map[string]jsTableLookupFn{}
				}
				if _, exists := fnTable[svc][name]; !exists {
					fnTable[svc][name] = fn
				}
			}
		}
	}
	if len(fnTable) == 0 {
		return nil
	}

	type siteKey struct {
		svc, file string
		line      int
	}
	byLoc := make(map[siteKey]int, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		if n.Meta["key_dynamic"] != "true" {
			continue
		}
		byLoc[siteKey{n.Service, n.File, n.Line}] = i
	}

	var updated []graph.Node
	patchedID := map[string]bool{}
	for _, svc := range sortedStringKeys(fnTable) {
		fns := fnTable[svc]
		if len(fns) == 0 {
			continue
		}
		for _, file := range serviceFiles[svc] {
			if !isJSFile(file) {
				continue
			}
			for _, site := range scanJSTableLookupCallSites(file, fns) {
				idx, ok := byLoc[siteKey{svc, site.file, site.line}]
				if !ok {
					continue
				}
				orig := nodes[idx]
				if patchedID[orig.ID] {
					continue
				}
				field := jsDynamicValueField(orig.Meta)
				if field == "" {
					continue
				}
				patchedID[orig.ID] = true
				m := make(map[string]string, len(orig.Meta)+1)
				for k, v := range orig.Meta {
					m[k] = v
				}
				m[field] = site.value
				delete(m, "key_dynamic")
				delete(m, "key_dynamic_raw")
				m["key_via"] = "js_call_table_lookup"
				patched := orig
				patched.Meta = m
				if patched.Label == "" || patched.Label == "dynamic" {
					if mth := m["method"]; mth != "" {
						patched.Label = mth + " " + site.value
					} else {
						patched.Label = site.value
					}
				}
				updated = append(updated, patched)
			}
		}
	}
	return updated
}

// jsTableLookupFn is a discovered function whose whole body forwards one of
// its own parameters into a computed lookup on a known object-literal table.
type jsTableLookupFn struct {
	paramIndex int
	table      map[string]string
}

// jsTableLookupSite is one call site resolved against a jsTableLookupFn.
type jsTableLookupSite struct {
	file  string
	line  int
	value string
}

// jsDynamicValueField finds which key_dynamic node meta field this pass
// should patch: the producer-key field the matcher zeroed out (matcher.go's
// X.1a block) alongside key_dynamic=true. The candidate set mirrors that
// block's own doc comment ("these two patterns capture no path/url/channel
// field").
func jsDynamicValueField(meta map[string]string) string {
	for _, k := range []string{"path", "url", "channel"} {
		if v, ok := meta[k]; ok && v == "" {
			return k
		}
	}
	return ""
}

func sortedStringKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// discoverJSTableLookupFns re-parses file and returns every top-level
// function/arrow function it defines whose entire body is `return
// TABLE[param]` — param a bare identifier matching one of the function's own
// parameters, TABLE a plain identifier resolved to an object literal via
// jsResolveIdentifierTable.
func discoverJSTableLookupFns(file string, fileSet map[string]bool) map[string]jsTableLookupFn {
	src, root, _, ok := jsParse(file)
	if !ok {
		return nil
	}

	out := map[string]jsTableLookupFn{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		var params, name, body *sitter.Node
		switch n.Type() {
		case "function_declaration":
			params = n.ChildByFieldName("parameters")
			name = n.ChildByFieldName("name")
			body = n.ChildByFieldName("body")
		case "arrow_function", "function_expression":
			params = n.ChildByFieldName("parameters")
			body = n.ChildByFieldName("body")
			if decl := n.Parent(); decl != nil && decl.Type() == "variable_declarator" {
				name = decl.ChildByFieldName("name")
			}
		}
		if params != nil && name != nil && body != nil {
			if idx, tableIdent, ok := jsTableLookupBody(body, params, src); ok {
				if table := jsResolveIdentifierTable(file, tableIdent, fileSet, src, root); len(table) > 0 {
					out[name.Content(src)] = jsTableLookupFn{paramIndex: idx, table: table}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	if len(out) == 0 {
		return nil
	}
	return out
}

// jsTableLookupBody reports whether body is a single `return TABLE[param]`
// (braced or, for an arrow's concise body, the subscript expression itself),
// returning the position of the matching parameter and TABLE's name.
func jsTableLookupBody(body, params *sitter.Node, src []byte) (paramIndex int, tableIdent string, ok bool) {
	expr := body
	if body.Type() == "statement_block" {
		var stmt *sitter.Node
		for i := 0; i < int(body.NamedChildCount()); i++ {
			if stmt != nil {
				return 0, "", false // more than one statement — do not guess
			}
			stmt = body.NamedChild(i)
		}
		if stmt == nil || stmt.Type() != "return_statement" || stmt.NamedChildCount() == 0 {
			return 0, "", false
		}
		expr = stmt.NamedChild(0)
	}
	for expr != nil && expr.Type() == "parenthesized_expression" {
		expr = expr.NamedChild(0)
	}
	if expr == nil || expr.Type() != "subscript_expression" {
		return 0, "", false
	}
	obj := expr.ChildByFieldName("object")
	index := expr.ChildByFieldName("index")
	if obj == nil || obj.Type() != "identifier" || index == nil || index.Type() != "identifier" {
		return 0, "", false
	}
	paramName := index.Content(src)

	idx := 0
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p.Type() == "required_parameter" || p.Type() == "optional_parameter" {
			if inner := p.ChildByFieldName("pattern"); inner != nil {
				p = inner
			}
		}
		if p.Type() == "identifier" && p.Content(src) == paramName {
			return idx, obj.Content(src), true
		}
		idx++
	}
	return 0, "", false
}

// jsResolveIdentifierTable resolves name to a string-keyed object-literal
// table, either declared locally in file or one import hop away (a default
// or named export in the file the local import statement names).
func jsResolveIdentifierTable(file, name string, fileSet map[string]bool, src []byte, root *sitter.Node) map[string]string {
	if lit := jsFindTopLevelObjectLiteral(root, src, name); lit != nil {
		return jsObjectLiteralStringTable(lit, src)
	}

	spec, named := jsFindImportSpecifier(root, src, name)
	if spec == "" {
		return nil
	}
	target := resolveJSImportPath(file, spec, fileSet)
	if target == "" {
		return nil
	}
	tsrc, troot, _, ok := jsParse(target)
	if !ok {
		return nil
	}
	var exported *sitter.Node
	if named {
		exported = jsFindTopLevelObjectLiteral(troot, tsrc, name)
	} else {
		exported = jsFindDefaultExportObject(troot, tsrc)
	}
	if exported == nil {
		return nil
	}
	return jsObjectLiteralStringTable(exported, tsrc)
}

// jsFindTopLevelObjectLiteral finds `const/let/var name = {...}` anywhere in
// the file (not scoped to top level strictly — a route table declared inside
// a wrapping IIFE is still the same fact), returning its first hit.
func jsFindTopLevelObjectLiteral(root *sitter.Node, src []byte, name string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found != nil {
			return
		}
		if n.Type() == "variable_declarator" {
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" && nm.Content(src) == name {
				if v := n.ChildByFieldName("value"); v != nil && v.Type() == "object" {
					found = v
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return found
}

// jsFindDefaultExportObject finds `export default {...}` or `export default
// X` (X a same-file const object literal), returning the object literal.
func jsFindDefaultExportObject(root *sitter.Node, src []byte) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found != nil {
			return
		}
		if n.Type() == "export_statement" {
			for i := 0; i < int(n.ChildCount()); i++ {
				if c := n.Child(i); c != nil && c.Type() == "default" {
					if v := n.ChildByFieldName("value"); v != nil {
						switch v.Type() {
						case "object":
							found = v
						case "identifier":
							found = jsFindTopLevelObjectLiteral(root, src, v.Content(src))
						}
					}
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return found
}

// jsFindImportSpecifier finds the import statement in root binding name,
// returning its source specifier and whether it was a named (vs default)
// import.
func jsFindImportSpecifier(root *sitter.Node, src []byte, name string) (specifier string, named bool) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || specifier != "" {
			return
		}
		if n.Type() == "import_statement" {
			sourceNode := n.ChildByFieldName("source")
			if sourceNode == nil {
				return
			}
			raw := strings.Trim(sourceNode.Content(src), "\"'`")
			var clause *sitter.Node
			for i := 0; i < int(n.NamedChildCount()); i++ {
				if c := n.NamedChild(i); c.Type() == "import_clause" {
					clause = c
					break
				}
			}
			if clause == nil {
				return
			}
			for i := 0; i < int(clause.NamedChildCount()); i++ {
				c := clause.NamedChild(i)
				switch c.Type() {
				case "identifier": // default import
					if c.Content(src) == name {
						specifier, named = raw, false
						return
					}
				case "named_imports":
					for j := 0; j < int(c.NamedChildCount()); j++ {
						spec := c.NamedChild(j)
						if spec.Type() != "import_specifier" {
							continue
						}
						local := spec.ChildByFieldName("alias")
						if local == nil {
							local = spec.ChildByFieldName("name")
						}
						if local != nil && local.Content(src) == name {
							if orig := spec.ChildByFieldName("name"); orig != nil {
								name = orig.Content(src) // resolve alias to the exported name
							}
							specifier, named = raw, true
							return
						}
					}
				}
			}
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return specifier, named
}

// jsObjectLiteralStringTable extracts an object literal's own string/
// template (non-interpolated)-valued pairs. A key or value of any other
// shape is simply absent from the table rather than aborting the whole
// extraction — a table with one computed entry among fifty literal ones is
// still useful for every literal entry.
func jsObjectLiteralStringTable(obj *sitter.Node, src []byte) map[string]string {
	out := map[string]string{}
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		p := obj.NamedChild(i)
		if p.Type() != "pair" {
			continue
		}
		k := jsObjLitKeyName(p.ChildByFieldName("key"), src)
		v, ok := jsObjLitStringValue(p.ChildByFieldName("value"), src)
		if k == "" || !ok {
			continue
		}
		out[k] = v
	}
	return out
}

func jsObjLitKeyName(k *sitter.Node, src []byte) string {
	if k == nil {
		return ""
	}
	switch k.Type() {
	case "property_identifier", "identifier":
		return k.Content(src)
	case "string":
		s, _ := jsObjLitStringValue(k, src)
		return s
	}
	return ""
}

func jsObjLitStringValue(v *sitter.Node, src []byte) (string, bool) {
	if v == nil {
		return "", false
	}
	switch v.Type() {
	case "string":
		t := v.Content(src)
		if len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	case "template_string":
		t := v.Content(src)
		if strings.Contains(t, "${") || len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	}
	return "", false
}

// scanJSTableLookupCallSites re-parses file for call sites of a name in fns
// with a literal string argument at the matching parameter position.
func scanJSTableLookupCallSites(file string, fns map[string]jsTableLookupFn) []jsTableLookupSite {
	src, root, lang, ok := jsParse(file)
	if !ok {
		return nil
	}
	q, err := compiledQuery(`
		(call_expression
			function: (identifier) @callee
			arguments: (arguments) @args) @call`, lang)
	if err != nil {
		return nil
	}
	cur := sitter.NewQueryCursor()
	cur.Exec(q, root)

	relFile := patterns.RelativizeToCwd(file)
	var out []jsTableLookupSite
	for {
		m, ok := cur.NextMatch()
		if !ok {
			break
		}
		var calleeNode, argsNode, callNode *sitter.Node
		for _, c := range m.Captures {
			switch q.CaptureNameForId(c.Index) {
			case "callee":
				calleeNode = c.Node
			case "args":
				argsNode = c.Node
			case "call":
				callNode = c.Node
			}
		}
		if calleeNode == nil || argsNode == nil || callNode == nil {
			continue
		}
		fn, ok := fns[calleeNode.Content(src)]
		if !ok || fn.paramIndex >= int(argsNode.NamedChildCount()) {
			continue
		}
		arg := argsNode.NamedChild(fn.paramIndex)
		lit, ok := jsObjLitStringValue(arg, src)
		if !ok {
			continue
		}
		value, ok := fn.table[lit]
		if !ok {
			continue
		}
		out = append(out, jsTableLookupSite{
			file:  relFile,
			line:  int(callNode.StartPoint().Row) + 1,
			value: value,
		})
	}
	return out
}
