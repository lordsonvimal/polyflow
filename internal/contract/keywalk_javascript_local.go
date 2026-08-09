package contract

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// C.1: 88 of orion's 114 dynamic JavaScript HTTP call sites pass a bare
// identifier as the URL — `var url = "/app/studies/" + studyId + "/edit"`
// three lines above `$.get(url)`. The URL is fully static and already in the
// tree; nothing was reading it, so 77% of the frontend's call sites carried no
// path at all and could not match any route however good the matcher was.
//
// This resolves such an identifier lexically from its use site, which is the
// only sound way to do it: a file-scoped constant table cannot, because one
// file routinely binds `url` a dozen times to a dozen different paths (see
// deliverables.js, where showTaskDialog/editTaskDialog/createDeliverable each
// declare their own). The binding must come from the scope the reader is
// standing in, not from the file.

// jsSearchScopes are the containers the search starts in and widens out
// through. Blocks are included because `const`/`let` are block-scoped, and the
// real corpus relies on it: initializeTaskModal (deliverables.js:1231) declares
// `const url` twice, once per branch of an if/else, and only the branch the
// call stands in is the one it read.
var jsSearchScopes = map[string]bool{
	"statement_block": true,
	"switch_case":     true,
	"switch_default":  true,

	"function_declaration":           true,
	"function_expression":            true,
	"generator_function":             true,
	"generator_function_declaration": true,
	"arrow_function":                 true,
	"method_definition":              true,
	"class_static_block":             true,
	"program":                        true,
}

// jsOpaqueScopes are the subtrees the search refuses to look inside: only
// functions, never blocks.
//
// The asymmetry with jsSearchScopes is deliberate and load-bearing. Skipping
// nested *blocks* as well would silently break the reassignment guard —
//
//	var url = "/app/a";
//	if (flag) { url = "/app/b"; }
//	$.get(url);
//
// — because the second write lives in a block the search would then never
// enter, leaving one apparent binding and a confidently wrong path.
var jsOpaqueScopes = map[string]bool{
	"function_declaration":           true,
	"function_expression":            true,
	"generator_function":             true,
	"generator_function_declaration": true,
	"arrow_function":                 true,
	"method_definition":              true,
	"class_static_block":             true,
}

// jsMaxScopeHops bounds how far out the search widens: enough to climb out of
// a few nested blocks into the enclosing function and one closure beyond it,
// without turning a miss into a whole-file scan.
const jsMaxScopeHops = 6

// jsResolveLocalBinding returns the value expression bound to name, as seen
// from use. It returns nil unless the answer is unambiguous:
//
//   - the nearest enclosing scope that binds name at all must bind it exactly
//     once. A reassigned variable has no single value, and guessing one would
//     manufacture a confident wrong path — the failure mode this whole phase
//     exists to remove.
//   - only bindings that appear textually before the use site count. A later
//     binding cannot be what the call read.
//   - bindings inside a nested function that does not contain the use site are
//     not in scope and are skipped, so a sibling handler's `url` never leaks
//     into this one.
func jsResolveLocalBinding(use *sitter.Node, src []byte, name string) *sitter.Node {
	if use == nil || name == "" {
		return nil
	}
	scope := jsEnclosingScope(use)
	for hops := 0; scope != nil && hops < jsMaxScopeHops; hops++ {
		found, count := jsFindBinding(scope, use, src, name)
		if count == 1 {
			return found
		}
		if count > 1 {
			return nil // reassigned in the scope that owns it — not knowable
		}
		if scope.Type() == "program" {
			return nil
		}
		scope = jsEnclosingScope(scope)
	}
	return nil
}

// jsEnclosingScope returns the nearest scope-introducing ancestor of n,
// strictly above it.
func jsEnclosingScope(n *sitter.Node) *sitter.Node {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if jsSearchScopes[cur.Type()] {
			return cur
		}
	}
	return nil
}

// jsFindBinding scans scope for bindings of name that precede use, returning
// the sole value expression and the total count seen. The count is returned
// rather than a bool so the caller can distinguish "not here, widen the
// search" from "here, but ambiguous, stop".
func jsFindBinding(scope, use *sitter.Node, src []byte, name string) (*sitter.Node, int) {
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
		// A nested scope that does not contain the use site holds bindings
		// that are invisible from it. (The scope we were handed is itself
		// exempt: the walk starts inside it.)
		if n != scope && jsOpaqueScopes[n.Type()] && !jsContains(n, useStart) {
			return
		}
		switch n.Type() {
		case "variable_declarator":
			if jsFieldIdentIs(n, "name", src, name) {
				if v := n.ChildByFieldName("value"); v != nil && v.StartByte() < useStart {
					found, count = v, count+1
				}
			}
		case "assignment_expression":
			if jsFieldIdentIs(n, "left", src, name) {
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

// jsFieldIdentIs reports whether n's named field is a plain identifier equal
// to name. Destructuring patterns and member targets are deliberately not
// matched: `{url} = resp` and `this.url = x` are not the local binding the
// call site read.
func jsFieldIdentIs(n *sitter.Node, field string, src []byte, name string) bool {
	f := n.ChildByFieldName(field)
	if f == nil || f.Type() != "identifier" {
		return false
	}
	return string(src[f.StartByte():f.EndByte()]) == name
}

func jsContains(n *sitter.Node, offset uint32) bool {
	return n.StartByte() <= offset && offset < n.EndByte()
}

// jsNonClientConstructors are the built-ins whose `.get`/`.set`/`.delete`
// methods are container operations, not HTTP verbs.
var jsNonClientConstructors = map[string]bool{
	"Map": true, "Set": true, "WeakMap": true, "WeakSet": true,
	"URLSearchParams": true, "FormData": true, "Headers": true,
}

// JSReceiverIsLocalContainer reports whether the identifier `name`, read at
// `use`, is bound in the surrounding code to a plain data container rather
// than to an HTTP client.
//
// The axios_instance_call pattern matches any `receiver.get(x)`, guarded only
// by whether the *service* depends on axios (axios_instance.yaml:4-11). That
// gate is per-repo, so in a repo that uses axios anywhere, every
// `updatedDetailsMap.get(d.id)` and `foldersByParent.get(id)` in the codebase
// was indexed as an outbound HTTP call — 44 phantom clients on this fleet, a
// third of all axios_instance_call nodes. An agent shown a phantom endpoint
// has been actively misled, which is worse than being shown nothing.
//
// The check is evidence-based, not name-based: it fires only when the
// receiver's own binding is visible and is unambiguously a container.
func JSReceiverIsLocalContainer(use *sitter.Node, src []byte, name string) bool {
	v := jsResolveLocalBinding(use, src, name)
	if v == nil {
		return false // unknown binding (cross-file instance) — never guess
	}
	switch v.Type() {
	case "object", "array":
		return true
	case "new_expression":
		ctor := v.ChildByFieldName("constructor")
		if ctor == nil || ctor.Type() != "identifier" {
			return false
		}
		return jsNonClientConstructors[string(src[ctor.StartByte():ctor.EndByte()])]
	}
	return false
}

// jsURLPairKeys are the object keys that hold a request path. Read when a
// pattern's URL capture binds a whole options object instead of the path.
var jsURLPairKeys = map[string]bool{"url": true, "path": true, "href": true}

// jsObjectURLValue returns the value of the url/path/href pair of an object
// literal, or nil when there is none or more than one.
//
// `$.ajax({url: "/app/" + type + "/actions", type: "GET"})` matches both the
// options-form and the direct-arg-form jQuery patterns, and when the
// direct-arg form wins, the URL capture binds the entire `{…}`. The path was
// never missing — it was one field down.
func jsObjectURLValue(obj *sitter.Node, src []byte) *sitter.Node {
	var (
		found *sitter.Node
		count int
	)
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		pair := obj.NamedChild(i)
		if pair == nil || pair.Type() != "pair" {
			continue
		}
		k := pair.ChildByFieldName("key")
		if k == nil {
			continue
		}
		key := stripKeyLiteral(string(src[k.StartByte():k.EndByte()]))
		if !jsURLPairKeys[key] {
			continue
		}
		if v := pair.ChildByFieldName("value"); v != nil {
			found, count = v, count+1
		}
	}
	if count != 1 {
		return nil
	}
	return found
}
