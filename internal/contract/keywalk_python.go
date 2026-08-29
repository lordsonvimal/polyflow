package contract

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// pythonKeyWalker enumerates literal alternatives for Python key expressions.
// Tier PK (docs/python-parity-plan.md): replaces the placeholder that always
// declined ("return nil, true") with a real implementation, mirroring
// Go/JS/Ruby's shapes for the ones Python actually has: plain/f-string
// literals, ternary (`a if cond else b`), and identifier resolution (locals,
// then module-level constants).
//
// F-strings diverge from Ruby/JS's string_interpolation/template_string
// handling on purpose: those two always wildcard an interpolation hole
// regardless of whether the identifier resolves, because their key shapes
// are paths where a "*" hole is still useful for wildcard-anchored matching.
// Python's dominant f-string shape is host/base-URL composition
// (`f"{BASE_URL}/users"`), where a wildcarded host is not a useful match —
// so an interpolation resolves to its concrete value or the whole string
// stays dynamic; there is no wildcard middle ground here.
type pythonKeyWalker struct{}

func (w *pythonKeyWalker) Language() string { return "python" }

func (w *pythonKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkPythonExpr(node, src, consts, 0)
}

func walkPythonExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "string":
		return walkPythonString(node, src, consts, depth)

	case "conditional_expression":
		// `body if condition else alternative` — grammar.js:1061 gives this
		// node no field names, only three positional named children (body,
		// condition, alternative); the condition itself is never walked.
		if node.NamedChildCount() < 3 {
			return nil, true
		}
		body := node.NamedChild(0)
		alt := node.NamedChild(2)
		if body == nil || alt == nil {
			return nil, true
		}
		bodyVals, bodyDyn := walkPythonExpr(body, src, consts, depth+1)
		if bodyDyn {
			return nil, true
		}
		altVals, altDyn := walkPythonExpr(alt, src, consts, depth+1)
		if altDyn {
			return nil, true
		}
		combined := append(bodyVals, altVals...)
		if len(combined) > keyWalkerMaxBranches {
			return nil, true
		}
		return combined, false

	case "identifier":
		name := string(src[node.StartByte():node.EndByte()])
		if v, ok := consts(name); ok {
			return []string{v}, false
		}
		if v := pythonResolveLocalBinding(node, src, name); v != nil {
			return walkPythonExpr(v, src, consts, depth+1)
		}
		if branches := pythonResolveBranchBindings(node, src, name); branches != nil {
			var combined []string
			for _, b := range branches {
				vals, dyn := walkPythonExpr(b, src, consts, depth+1)
				if dyn {
					return nil, true
				}
				combined = append(combined, vals...)
			}
			if len(combined) > keyWalkerMaxBranches {
				return nil, true
			}
			return combined, false
		}
		return nil, true

	default:
		return nil, true
	}
}

// walkPythonString handles both plain string literals and f-strings — the
// same "string" node type per the grammar (pinned fact #3,
// docs/python-parity-plan.md), distinguished only by whether it has
// "interpolation" children. string_start/string_end (quote/prefix tokens)
// are skipped; string_content chunks are kept verbatim; each interpolation's
// inner expression is resolved recursively and substituted, or the whole
// string is declined as dynamic if any interpolation can't be resolved to
// exactly one concrete value.
func walkPythonString(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	var out strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "string_start", "string_end":
			continue
		case "interpolation":
			expr := child.ChildByFieldName("expression")
			if expr == nil {
				return nil, true
			}
			vals, dyn := walkPythonExpr(expr, src, consts, depth+1)
			if dyn || len(vals) != 1 {
				return nil, true
			}
			out.WriteString(vals[0])
		default: // string_content or other literal text chunk
			out.WriteString(string(src[child.StartByte():child.EndByte()]))
		}
	}
	result := out.String()
	if !hasConcreteTemplateContent(result) {
		return nil, true
	}
	return []string{result}, false
}

func init() {
	RegisterKeyWalker(&pythonKeyWalker{})
}
