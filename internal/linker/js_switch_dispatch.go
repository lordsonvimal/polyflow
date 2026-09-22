package linker

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// js_switch_dispatch.go closes a second same-file/cross-file gap the JS
// KeyWalker's same-file call inlining declines by construction: a call to a
// function or object-literal method whose whole body is a single `switch`
// on one of its own parameters, each case returning a further-walkable
// expression (a template literal, a literal, a ternary — anything
// contract.KeyWalker already knows how to read). Resolves for any call site
// passing a literal argument at that parameter's position that matches a
// case label (or falls to `default` when no case matches).
//
// The shape is purely structural: no framework/library name is referenced.
// A dispatcher whose discriminant is reassigned before the switch, or whose
// body has any statement besides the switch itself, is out of scope — same
// "don't guess which control-flow path runs" posture as
// contract/keywalk_javascript_local.go's jsSingleReturnExpr. A case whose own
// body is not a single return is left dynamic for that case only, so one
// messy branch among many clean ones does not sink the whole dispatcher.
func LinkJSSwitchDispatchCalls(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	fnTable := map[string]map[string]jsSwitchDispatchFn{}
	for svc, files := range serviceFiles {
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			for name, fn := range discoverJSSwitchDispatchFns(file) {
				if fnTable[svc] == nil {
					fnTable[svc] = map[string]jsSwitchDispatchFn{}
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
			for _, site := range scanJSSwitchDispatchCallSites(file, fns) {
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
				// scanJSSwitchDispatchCallSites never emits a site with zero
				// candidates, so exactly one of the two cases below always
				// applies.
				patchedID[orig.ID] = true
				m := make(map[string]string, len(orig.Meta)+2)
				for k, v := range orig.Meta {
					m[k] = v
				}
				delete(m, "key_dynamic")
				delete(m, "key_dynamic_raw")
				m["key_via"] = "js_switch_dispatch"
				patched := orig
				if len(site.candidates) == 1 {
					m[field] = site.candidates[0]
					if patched.Label == "" || patched.Label == "dynamic" {
						if mth := m["method"]; mth != "" {
							patched.Label = mth + " " + site.candidates[0]
						} else {
							patched.Label = site.candidates[0]
						}
					}
				} else {
					m["key_candidates"] = contract.MarshalKeyCandidates(site.candidates)
					if patched.Label == "" || patched.Label == "dynamic" {
						patched.Label = "branch_enum"
					}
				}
				patched.Meta = m
				updated = append(updated, patched)
			}
		}
	}
	return updated
}

// jsSwitchOutcome is one switch case's (or default's) resolved candidates —
// mirrors contract.KeyWalker.WalkKey's own (candidates, dynamic) shape.
type jsSwitchOutcome struct {
	dynamic    bool
	candidates []string
}

// jsSwitchDispatchFn is a discovered function/method whose whole body is a
// switch on one of its own parameters.
type jsSwitchDispatchFn struct {
	paramIndex int
	cases      map[string]jsSwitchOutcome
	defaultOut *jsSwitchOutcome
}

// jsSwitchDispatchSite is one call site resolved against a jsSwitchDispatchFn.
type jsSwitchDispatchSite struct {
	file       string
	line       int
	candidates []string
}

// discoverJSSwitchDispatchFns re-parses file and returns every
// function_declaration, arrow/function_expression bound via a
// variable_declarator, or object-literal method_definition it defines whose
// entire body is a single switch statement on a bare identifier matching one
// of its own parameters. Object-literal methods are registered under
// "objName.methodName" (mirroring jsWrapperCalleeName's qualified-name
// convention) so a call like `linkTo.type(...)` resolves.
func discoverJSSwitchDispatchFns(file string) map[string]jsSwitchDispatchFn {
	src, root, _, ok := jsParse(file)
	if !ok {
		return nil
	}

	out := map[string]jsSwitchDispatchFn{}
	register := func(qname string, params, body *sitter.Node) {
		if qname == "" || params == nil || body == nil {
			return
		}
		sw := jsSoleSwitchStatement(body)
		if sw == nil {
			return
		}
		paramIdx, ok := jsSwitchDiscriminantParamIndex(sw, params, src)
		if !ok {
			return
		}
		cases, def := jsSwitchCaseOutcomes(sw, src)
		if len(cases) == 0 && def == nil {
			return
		}
		if _, exists := out[qname]; !exists {
			out[qname] = jsSwitchDispatchFn{paramIndex: paramIdx, cases: cases, defaultOut: def}
		}
	}

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				register(nm.Content(src), n.ChildByFieldName("parameters"), n.ChildByFieldName("body"))
			}
		case "variable_declarator":
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression":
					if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
						register(nm.Content(src), v.ChildByFieldName("parameters"), v.ChildByFieldName("body"))
					}
				}
			}
		case "method_definition":
			if nm := n.ChildByFieldName("name"); nm != nil {
				if objName := jsEnclosingObjectLiteralVarName(n, src); objName != "" {
					register(objName+"."+nm.Content(src), n.ChildByFieldName("parameters"), n.ChildByFieldName("body"))
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

// jsEnclosingObjectLiteralVarName returns the variable name an object
// literal containing n (a method_definition) is assigned to — `var linkTo =
// { type(...) {...} }` -> "linkTo" — or "" when the object literal is not a
// direct variable_declarator value (nested, returned, passed as an argument
// — those have no stable name a caller could reference).
func jsEnclosingObjectLiteralVarName(n *sitter.Node, src []byte) string {
	obj := n.Parent()
	if obj == nil || obj.Type() != "object" {
		return ""
	}
	decl := obj.Parent()
	if decl == nil || decl.Type() != "variable_declarator" {
		return ""
	}
	nm := decl.ChildByFieldName("name")
	if nm == nil || nm.Type() != "identifier" {
		return ""
	}
	return nm.Content(src)
}

// jsSoleSwitchStatement returns body's switch statement when the body's only
// statement is that switch, or nil otherwise.
func jsSoleSwitchStatement(body *sitter.Node) *sitter.Node {
	if body.Type() != "statement_block" {
		return nil
	}
	var stmt *sitter.Node
	for i := 0; i < int(body.NamedChildCount()); i++ {
		if stmt != nil {
			return nil // more than one statement — do not guess
		}
		stmt = body.NamedChild(i)
	}
	if stmt == nil || stmt.Type() != "switch_statement" {
		return nil
	}
	return stmt
}

// jsSwitchDiscriminantParamIndex reports whether sw switches on a bare
// identifier that is one of params' own identifiers, returning its position.
func jsSwitchDiscriminantParamIndex(sw, params *sitter.Node, src []byte) (int, bool) {
	v := sw.ChildByFieldName("value")
	for v != nil && v.Type() == "parenthesized_expression" {
		v = v.NamedChild(0)
	}
	if v == nil || v.Type() != "identifier" {
		return 0, false
	}
	name := v.Content(src)

	idx := 0
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p.Type() == "required_parameter" || p.Type() == "optional_parameter" {
			if inner := p.ChildByFieldName("pattern"); inner != nil {
				p = inner
			}
		}
		if p.Type() == "identifier" && p.Content(src) == name {
			return idx, true
		}
		idx++
	}
	return 0, false
}

// jsSwitchCaseOutcomes reads sw's case/default bodies into resolved
// outcomes. A case label that is not itself a plain string/template literal
// is skipped entirely (it names no literal a caller could pass). Fallthrough
// labels (a `case` with no statements of its own) share the next
// non-empty case's outcome, matching JS's own fallthrough semantics for the
// common "several labels, one body" shape.
func jsSwitchCaseOutcomes(sw *sitter.Node, src []byte) (cases map[string]jsSwitchOutcome, def *jsSwitchOutcome) {
	body := sw.ChildByFieldName("body")
	if body == nil {
		return nil, nil
	}
	cases = map[string]jsSwitchOutcome{}
	var pending []string
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		switch c.Type() {
		case "switch_case":
			valNode := c.ChildByFieldName("value")
			var stmts []*sitter.Node
			for j := 0; j < int(c.NamedChildCount()); j++ {
				if n := c.NamedChild(j); n != valNode {
					stmts = append(stmts, n)
				}
			}
			label, ok := jsObjLitStringValue(valNode, src)
			if !ok {
				pending = nil // a non-literal label in the run invalidates any pending fallthrough group
				continue
			}
			if len(stmts) == 0 {
				pending = append(pending, label)
				continue
			}
			outcome := jsSwitchBodyOutcome(stmts, src)
			for _, lbl := range append(pending, label) {
				cases[lbl] = outcome
			}
			pending = nil
		case "switch_default":
			var stmts []*sitter.Node
			for j := 0; j < int(c.NamedChildCount()); j++ {
				stmts = append(stmts, c.NamedChild(j))
			}
			if len(stmts) > 0 {
				o := jsSwitchBodyOutcome(stmts, src)
				def = &o
			}
		}
	}
	return cases, def
}

// jsSwitchBodyOutcome resolves a case/default's statement list: exactly one
// `return <expr>` resolves via the same contract.KeyWalker every other
// producer key field goes through (so template reconstruction, ternaries and
// literal extraction are identical, not reimplemented); anything else —
// zero statements, more than one, a non-return — is dynamic.
func jsSwitchBodyOutcome(stmts []*sitter.Node, src []byte) jsSwitchOutcome {
	if len(stmts) != 1 || stmts[0].Type() != "return_statement" || stmts[0].NamedChildCount() == 0 {
		return jsSwitchOutcome{dynamic: true}
	}
	expr := stmts[0].NamedChild(0)
	walker := contract.KeyWalkerFor("javascript")
	if walker == nil {
		return jsSwitchOutcome{dynamic: true}
	}
	noConsts := func(string) (string, bool) { return "", false }
	cands, dynamic := walker.WalkKey(expr, src, noConsts)
	if dynamic || len(cands) == 0 {
		return jsSwitchOutcome{dynamic: true}
	}
	return jsSwitchOutcome{candidates: cands}
}

// scanJSSwitchDispatchCallSites re-parses file for call sites of a name in
// fns (bare identifier or "obj.method" qualified, mirroring
// jsWrapperCalleeName) with a literal string argument at the matching
// parameter position.
func scanJSSwitchDispatchCallSites(file string, fns map[string]jsSwitchDispatchFn) []jsSwitchDispatchSite {
	src, root, lang, ok := jsParse(file)
	if !ok {
		return nil
	}
	q, err := compiledQuery(`
		(call_expression
			function: [(identifier) (member_expression)] @callee
			arguments: (arguments) @args) @call`, lang)
	if err != nil {
		return nil
	}
	cur := sitter.NewQueryCursor()
	cur.Exec(q, root)

	relFile := patterns.RelativizeToCwd(file)
	var out []jsSwitchDispatchSite
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
		callee, ok := jsWrapperCalleeName(calleeNode, src)
		if !ok {
			continue
		}
		fn, ok := fns[callee]
		if !ok || fn.paramIndex >= int(argsNode.NamedChildCount()) {
			continue
		}
		arg := argsNode.NamedChild(fn.paramIndex)
		lit, ok := jsObjLitStringValue(arg, src)
		if !ok {
			continue
		}
		outcome, matched := fn.cases[lit]
		if !matched {
			if fn.defaultOut == nil {
				continue
			}
			outcome = *fn.defaultOut
		}
		if outcome.dynamic || len(outcome.candidates) == 0 {
			continue
		}
		out = append(out, jsSwitchDispatchSite{
			file:       relFile,
			line:       int(callNode.StartPoint().Row) + 1,
			candidates: outcome.candidates,
		})
	}
	return out
}
