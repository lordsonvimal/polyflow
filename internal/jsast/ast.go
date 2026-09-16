package jsast

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// FnTypes are the node types that introduce a function scope. Backtracking a
// binding is bounded to one of these — "the enclosing function, nothing
// wider" is what keeps a read intraprocedural.
var FnTypes = map[string]bool{
	"function_declaration":           true,
	"function_expression":            true,
	"function":                       true,
	"generator_function":             true,
	"generator_function_declaration": true,
	"arrow_function":                 true,
	"method_definition":              true,
	"class_static_block":             true,
}

// EnclosingFunction returns the nearest function-introducing ancestor of n,
// strictly above it, or nil at module scope.
func EnclosingFunction(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if FnTypes[cur.Type()] {
			return cur
		}
	}
	return nil
}

// ObjectKeyValue returns the value node for key `key` in an object literal
// (`{ url: x }` or `{ url }` shorthand), or nil.
func ObjectKeyValue(n *sitter.Node, src []byte, key string) *sitter.Node {
	if n == nil || n.Type() != "object" {
		return nil
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "pair":
			k := c.ChildByFieldName("key")
			if k != nil && trimQuotes(k.Content(src)) == key {
				return c.ChildByFieldName("value")
			}
		case "shorthand_property_identifier":
			if c.Content(src) == key {
				return c
			}
		}
	}
	return nil
}

func trimQuotes(s string) string {
	if len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// LocalIdentName returns the name of a bare identifier reference, including
// the shorthand form an options object contributes. Member expressions
// (`this.x`) are deliberately not handled here: they need class-member
// dataflow, out of scope for a bare-identifier backtrack.
func LocalIdentName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "shorthand_property_identifier", "property_identifier":
		return n.Content(src)
	}
	return ""
}

// LocalAssignments collects every value bound to name inside fn's subtree
// before useStart, in source order: `let url = …`, `url = …`, and the
// declarator form in each arm of a branch.
//
// Bindings inside a nested function that does not contain the use site are
// skipped — a callback's own binding is not this call's. Nested *blocks* are
// entered, and must be: if/else and switch arms that make a site
// multi-valued live in them, and refusing to look would leave exactly one
// visible binding and a confidently wrong single path.
func LocalAssignments(fn *sitter.Node, useStart uint32, src []byte, name string) []*sitter.Node {
	var out []*sitter.Node
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n != fn && FnTypes[n.Type()] &&
			!(n.StartByte() <= useStart && useStart < n.EndByte()) {
			return
		}
		switch n.Type() {
		case "variable_declarator":
			if fieldIdentIs(n, "name", src, name) {
				if v := n.ChildByFieldName("value"); v != nil && v.StartByte() < useStart {
					out = append(out, v)
				}
			}
		case "assignment_expression":
			if fieldIdentIs(n, "left", src, name) {
				if v := n.ChildByFieldName("right"); v != nil && v.StartByte() < useStart {
					out = append(out, v)
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(fn)
	return out
}

func fieldIdentIs(n *sitter.Node, field string, src []byte, name string) bool {
	f := n.ChildByFieldName(field)
	if f == nil || f.Type() != "identifier" {
		return false
	}
	return f.Content(src) == name
}

// ModuleScopeReassign reports whether name is assigned by a top-level
// statement anywhere in the file containing fn. A module-scope write means
// the name a call site read is not owned by the enclosing function, so the
// assignments found inside it are not the whole story and the site must
// abstain.
func ModuleScopeReassign(fn *sitter.Node, src []byte, name string) bool {
	root := fn
	for root.Parent() != nil {
		root = root.Parent()
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		stmt := root.NamedChild(i)
		if stmt == nil || stmt.Type() != "expression_statement" || stmt.NamedChildCount() == 0 {
			continue
		}
		expr := stmt.NamedChild(0)
		if expr.Type() != "assignment_expression" {
			continue
		}
		if fieldIdentIs(expr, "left", src, name) {
			return true
		}
	}
	return false
}
