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
// returning the sole value expression and the total count seen.
func pythonFindBinding(scope, use *sitter.Node, src []byte, name string) (*sitter.Node, int) {
	assigns := pythonCollectAssignments(scope, use, src, name)
	if len(assigns) == 1 {
		return assigns[0].ChildByFieldName("right"), 1
	}
	return nil, len(assigns)
}

// pythonCollectAssignments returns every `assignment` node in scope binding
// name that precedes use (source order), i.e. the raw material pythonFindBinding
// and pythonResolveBranchBindings both group. Nested function_definitions that
// do not contain the use site are opaque (their locals are invisible from
// here); the scope handed in is exempt since the walk starts inside it.
func pythonCollectAssignments(scope, use *sitter.Node, src []byte, name string) []*sitter.Node {
	var out []*sitter.Node
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
					out = append(out, n)
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			visit(n.Child(i))
		}
	}
	visit(scope)
	return out
}

func pythonContains(n *sitter.Node, offset uint32) bool {
	return n.StartByte() <= offset && offset < n.EndByte()
}

// pythonResolveBranchBindings implements Tier PK.2: when a single-binding
// resolve fails because name is assigned more than once in its owning
// scope, check whether every one of those assignments is exactly the
// mutually-exclusive branches of one if/elif/else statement — the
// if-consequence, every elif_clause's consequence, and the mandatory
// else_clause's body, each contributing exactly one assignment and nothing
// else assigning name anywhere else in scope. That shape is jointly
// exhaustive the same way a ternary's body/alternative are (pinned fact:
// grammar has no nested-if-as-elif representation here — elif_clause and
// else_clause are direct `alternative`-field children of if_statement), so
// each branch's value expression is a genuine enumerable alternative rather
// than an ambiguous reassignment. A missing else, a branch that doesn't
// assign name, or assignments spread across more than one if_statement all
// make the binding unknowable — same as pythonFindBinding's count>1
// bailout — and return nil.
func pythonResolveBranchBindings(use *sitter.Node, src []byte, name string) []*sitter.Node {
	scope := pythonEnclosingScope(use)
	for hops := 0; scope != nil && hops < pythonMaxScopeHops; hops++ {
		assigns := pythonCollectAssignments(scope, use, src, name)
		switch {
		case len(assigns) > 1:
			return pythonGroupIfElseBranches(assigns)
		case len(assigns) == 1:
			return nil // resolvable as a single binding — not this function's shape
		}
		if scope.Type() == "module" {
			return nil
		}
		scope = pythonEnclosingScope(scope)
	}
	return nil
}

// pythonGroupIfElseBranches checks whether assigns (all bindings of one
// name, already scoped and use-ordered by the caller) are exactly the
// branch set of a single if/elif/else statement, and if so returns each
// branch's value expression in source order (if, then each elif, then
// else).
func pythonGroupIfElseBranches(assigns []*sitter.Node) []*sitter.Node {
	var rootIf *sitter.Node
	branchValue := map[*sitter.Node]*sitter.Node{} // branch block -> assigned value

	for _, assign := range assigns {
		block := pythonAssignmentBranchBlock(assign)
		if block == nil {
			return nil
		}
		parent := block.Parent()
		if parent == nil {
			return nil
		}
		var thisIf *sitter.Node
		switch parent.Type() {
		case "if_statement":
			if parent.ChildByFieldName("consequence") != block {
				return nil
			}
			thisIf = parent
		case "elif_clause":
			if parent.ChildByFieldName("consequence") != block {
				return nil
			}
			thisIf = parent.Parent()
		case "else_clause":
			if parent.ChildByFieldName("body") != block {
				return nil
			}
			thisIf = parent.Parent()
		default:
			return nil
		}
		if thisIf == nil || thisIf.Type() != "if_statement" {
			return nil
		}
		if rootIf == nil {
			rootIf = thisIf
		} else if rootIf != thisIf {
			return nil // assignments span more than one if-statement — ambiguous
		}
		if _, dup := branchValue[block]; dup {
			return nil // more than one assignment to name within the same branch
		}
		branchValue[block] = assign.ChildByFieldName("right")
	}
	if rootIf == nil {
		return nil
	}

	var branches []*sitter.Node
	consequence := rootIf.ChildByFieldName("consequence")
	v, ok := branchValue[consequence]
	if !ok {
		return nil
	}
	branches = append(branches, v)

	sawElse := false
	for i := 0; i < int(rootIf.ChildCount()); i++ {
		if rootIf.FieldNameForChild(i) != "alternative" {
			continue
		}
		alt := rootIf.Child(i)
		switch alt.Type() {
		case "elif_clause":
			v, ok := branchValue[alt.ChildByFieldName("consequence")]
			if !ok {
				return nil
			}
			branches = append(branches, v)
		case "else_clause":
			v, ok := branchValue[alt.ChildByFieldName("body")]
			if !ok {
				return nil
			}
			branches = append(branches, v)
			sawElse = true
		default:
			return nil
		}
	}
	if !sawElse || len(branches) != len(branchValue) {
		return nil // no else (not exhaustive), or a stray assignment outside the branch set
	}
	return branches
}

// pythonAssignmentBranchBlock returns the nearest enclosing `block` ancestor
// of assign, stopping at (and returning nil past) the owning
// function_definition/module — mirrors pythonEnclosingScope's containment
// boundary so a branch block is never mistaken for one in an outer scope.
func pythonAssignmentBranchBlock(assign *sitter.Node) *sitter.Node {
	for cur := assign.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "block" {
			return cur
		}
		if cur.Type() == "function_definition" || cur.Type() == "module" {
			return nil
		}
	}
	return nil
}
