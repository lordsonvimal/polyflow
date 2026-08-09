package contract

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// jsKeyWalker enumerates literal alternatives for JS/TS/JSX/TSX key expressions.
// Handles: string literals, ternary expressions (depth ≤2, branches ≤8),
// template literals without interpolations, identifier constant references (shape b).
type jsKeyWalker struct{}

func (w *jsKeyWalker) Language() string { return "javascript" }

func (w *jsKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkJSExpr(node, src, consts, 0)
}

func walkJSExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "string":
		// String literal "..." or '...': extract content
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "template_string":
		// X.1b: reconstruct instead of bailing on any interpolation — literal
		// chunks kept verbatim, each ${...} becomes a "*" wildcard hole.
		tmpl := jsReconstructTemplateString(node, src)
		if !hasConcreteTemplateContent(tmpl) {
			return nil, true
		}
		return []string{tmpl}, false

	case "binary_expression":
		if tmpl, ok := jsReconstructConcat(node, src); ok && hasConcreteTemplateContent(tmpl) {
			return []string{tmpl}, false
		}
		return nil, true

	case "ternary_expression":
		cons := node.ChildByFieldName("consequence")
		alt := node.ChildByFieldName("alternative")
		if cons == nil || alt == nil {
			return nil, true
		}
		consVals, consDyn := walkJSExpr(cons, src, consts, depth+1)
		if consDyn {
			return nil, true
		}
		altVals, altDyn := walkJSExpr(alt, src, consts, depth+1)
		if altDyn {
			return nil, true
		}
		combined := append(consVals, altVals...)
		if len(combined) > keyWalkerMaxBranches {
			return nil, true
		}
		return combined, false

	case "object":
		// C.1: the URL capture bound a whole options object rather than a
		// path — `$.ajax({url: …, type: "GET"})`. Read the url field.
		if v := jsObjectURLValue(node, src); v != nil {
			return walkJSExpr(v, src, consts, depth+1)
		}
		return nil, true

	case "identifier":
		// Shape (b): constant reference — resolve via ConstResolver
		name := string(src[node.StartByte():node.EndByte()])
		if v, ok := consts(name); ok {
			return []string{v}, false
		}
		// C.1: the dominant real shape is not a file constant but a
		// function-local `var url = "/app/…" + id` a few lines up.
		if v := jsResolveLocalBinding(node, src, name); v != nil {
			return walkJSExpr(v, src, consts, depth+1)
		}
		return nil, true

	case "member_expression":
		// H.2: object.property access into a same-file const object literal
		// (Solid Router's `clientRoutes.home`) — resolved via the compound
		// "obj.prop" key const_object_member populates in the const table.
		// Anything but a plain identifier.identifier chain (computed access,
		// nested chains, `this.x`) stays dynamic — never guessed.
		objNode := node.ChildByFieldName("object")
		propNode := node.ChildByFieldName("property")
		if objNode == nil || propNode == nil || objNode.Type() != "identifier" {
			return nil, true
		}
		obj := string(src[objNode.StartByte():objNode.EndByte()])
		prop := string(src[propNode.StartByte():propNode.EndByte()])
		if v, ok := consts(obj + "." + prop); ok {
			return []string{v}, false
		}
		return nil, true

	default:
		return nil, true
	}
}

// jsReconstructTemplateString reconstructs a template literal into a single
// wildcarded template: literal `string_fragment` chunks kept verbatim, each
// `${...}` substitution becomes a "*" wildcard hole. The backtick delimiters
// are skipped (not part of the key value).
func jsReconstructTemplateString(node *sitter.Node, src []byte) string {
	var out strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "`":
			continue
		case "template_substitution":
			out.WriteString("*")
		default: // string_fragment or other literal text chunk
			out.WriteString(string(src[child.StartByte():child.EndByte()]))
		}
	}
	return out.String()
}

// jsConcatMaxOperands bounds a `+` chain by the number of operands rather
// than by keyWalkerMaxDepth. A concatenation is linear, not branching: its
// tree depth is simply how many `+` it has, and `"/app/studies/" + studyId +
// "/deliverables/" + id + ".js"` nests four deep. Charging that against the
// depth-2 ceiling — which exists to stop ternary branch explosion — truncated
// exactly the longest, most specific URLs into a wildcard soup.
const jsConcatMaxOperands = 24

// jsReconstructConcat reconstructs a `+`-chained string concatenation into a
// single wildcarded template: literal operands contribute their text
// verbatim, any other operand becomes a "*" hole. The root must be a `+`, so
// an unrelated binary expression stays dynamic instead of collapsing to "*".
func jsReconstructConcat(node *sitter.Node, src []byte) (string, bool) {
	if !jsIsPlusExpr(node, src) {
		return "", false
	}
	var (
		out      []byte
		operands int
		ok       = true
	)
	var flatten func(n *sitter.Node)
	flatten = func(n *sitter.Node) {
		if !ok {
			return
		}
		if n != nil && jsIsPlusExpr(n, src) {
			left, right := n.ChildByFieldName("left"), n.ChildByFieldName("right")
			if left == nil || right == nil {
				ok = false
				return
			}
			flatten(left)
			flatten(right)
			return
		}
		operands++
		if operands > jsConcatMaxOperands {
			ok = false
			return
		}
		out = append(out, jsConcatSegment(n, src)...)
	}
	flatten(node)
	if !ok {
		return "", false
	}
	return string(out), true
}

func jsIsPlusExpr(n *sitter.Node, src []byte) bool {
	if n == nil || n.Type() != "binary_expression" {
		return false
	}
	op := n.ChildByFieldName("operator")
	return op != nil && string(src[op.StartByte():op.EndByte()]) == "+"
}

// jsConcatSegment reconstructs one non-`+` operand of a concatenation chain:
// a string or template literal verbatim, anything else a "*" hole.
func jsConcatSegment(node *sitter.Node, src []byte) string {
	if node == nil {
		return "*"
	}
	switch node.Type() {
	case "string":
		return stripKeyLiteral(string(src[node.StartByte():node.EndByte()]))
	case "template_string":
		return jsReconstructTemplateString(node, src)
	}
	return "*"
}

func init() {
	RegisterKeyWalker(&jsKeyWalker{})
}
