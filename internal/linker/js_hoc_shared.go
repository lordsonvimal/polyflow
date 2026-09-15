package linker

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// js_hoc_shared.go holds the small HOC-call-shape helpers js_mobx.go
// (FX.8.9) and js_client_routes.go (FX.8.10) still depend on now that
// js_hoc.go itself (LinkJSHOC) was deleted by the Tier FX FX.8.8 migration
// (patterns/javascript/js_hoc.yaml + internal/factpipe/hub_js_hoc.go, which
// ported its own copies, jhc-prefixed, since internal/factpipe cannot import
// internal/linker). Delete this file once FX.8.9/FX.8.10 land and no caller
// remains.
var hocNames = map[string]bool{
	"observer": true, "memo": true, "forwardRef": true,
	"withRouter": true, "pure": true, "hot": true,
}

func startsUpperASCII(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

// hocOutermostCallee returns the name of the wrapper actually applied to the
// component: the identifier / member-property at the head of the (possibly
// curried) call chain — `connect(a,b)(X)` → "connect".
func hocOutermostCallee(call *sitter.Node, src []byte) string {
	fn := call.ChildByFieldName("function")
	for fn != nil && fn.Type() == "call_expression" {
		fn = fn.ChildByFieldName("function")
	}
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

// hocCalleeName returns the HOC name if call is `hoc(...)` or `React.hoc(...)`.
func hocCalleeName(call *sitter.Node, src []byte) string {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return ""
	}
	var name string
	switch fn.Type() {
	case "identifier":
		name = fn.Content(src)
	case "member_expression":
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj != nil && prop != nil && obj.Content(src) == "React" {
			name = prop.Content(src)
		}
	}
	if hocNames[name] {
		return name
	}
	return ""
}

func hocFirstArg(call *sitter.Node) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return nil
	}
	return args.NamedChild(0)
}

// hocBinding resolves the name the HOC result is bound to:
//   - `const C = hoc(...)`      → ("C", false)
//   - `C = hoc(...)`            → ("C", false)
//   - `export default hoc(...)` → ("", true)
//
// Any other parent context is not a top-level component binding.
func hocBinding(call *sitter.Node, src []byte) (name string, viaExport bool) {
	p := call.Parent()
	if p == nil {
		return "", false
	}
	switch p.Type() {
	case "variable_declarator":
		if nm := p.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
			return nm.Content(src), false
		}
	case "assignment_expression":
		if l := p.ChildByFieldName("left"); l != nil && l.Type() == "identifier" {
			return l.Content(src), false
		}
	case "export_statement":
		return "", true
	}
	return "", false
}

func hocWalk(n *sitter.Node, fn func(*sitter.Node)) {
	if n.Type() == "call_expression" {
		fn(n)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		hocWalk(n.NamedChild(i), fn)
	}
}
