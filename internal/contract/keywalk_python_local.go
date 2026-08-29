package contract

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// pythonSearchScopes are the containers pythonResolveLocalBinding starts in
// and widens out through. Unlike JS, Python has no block scoping — an
// assignment inside an `if`/`for`/`with` body is visible to the whole
// enclosing function (or module, at top level) — so only the two real
// binding scopes are listed, not every compound-statement body.
var pythonSearchScopes = map[string]bool{
	"function_definition": true,
	"module":              true,
}

// pythonMaxScopeHops bounds how far out the search widens: enough to climb
// out of a nested function into an enclosing one (a closure) and then to
// module scope, without turning a miss into a whole-file scan.
const pythonMaxScopeHops = 6

// pythonResolveLocalBinding returns the value expression bound to name, as
// seen from use — mirrors jsResolveLocalBinding's contract exactly: the
// nearest enclosing scope that binds name at all must bind it exactly once
// before use, or the binding is treated as unknowable rather than guessed.
func pythonResolveLocalBinding(use *sitter.Node, src []byte, name string) *sitter.Node {
	if use == nil || name == "" {
		return nil
	}
	scope := pythonEnclosingScope(use)
	for hops := 0; scope != nil && hops < pythonMaxScopeHops; hops++ {
		found, count := pythonFindBinding(scope, use, src, name)
		if count == 1 {
			return found
		}
		if count > 1 {
			return nil // reassigned in the scope that owns it — not knowable
		}
		if scope.Type() == "module" {
			return nil
		}
		scope = pythonEnclosingScope(scope)
	}
	return nil
}

// pythonEnclosingScope returns the nearest scope-introducing ancestor of n,
// strictly above it.
func pythonEnclosingScope(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if pythonSearchScopes[cur.Type()] {
			return cur
		}
	}
	return nil
}

// pythonFindBinding scans scope for assignments to name that precede use,
// returning the sole value expression and the total count seen. Nested
// function_definitions that do not contain the use site are opaque (their
// locals are invisible from here); the scope handed in is exempt since the
// walk starts inside it.
func pythonFindBinding(scope, use *sitter.Node, src []byte, name string) (*sitter.Node, int) {
	var (
		found *sitter.Node
		count int
	)
	useStart := use.StartByte()

	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n != scope && n.Type() == "function_definition" && !pythonContains(n, useStart) {
			return
		}
		if n.Type() == "assignment" {
			if left := n.ChildByFieldName("left"); left != nil && left.Type() == "identifier" &&
				string(src[left.StartByte():left.EndByte()]) == name {
				if v := n.ChildByFieldName("right"); v != nil && v.StartByte() < useStart {
					found, count = v, count+1
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(scope)
	return found, count
}

func pythonContains(n *sitter.Node, offset uint32) bool {
	return n.StartByte() <= offset && offset < n.EndByte()
}
