package contract

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// rubyKeyWalker enumerates literal alternatives for Ruby key expressions.
// Handles: string literals, ternary (depth ≤2), constant references (shape b).
type rubyKeyWalker struct{}

func (w *rubyKeyWalker) Language() string { return "ruby" }

func (w *rubyKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkRubyExpr(node, src, consts, 0)
}

func walkRubyExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "string", "simple_string":
		// X.1b: reconstruct via children instead of capturing the whole raw
		// node text. Ruby interpolation parses as a "string" node containing
		// "interpolation" children (#{...}) — the same node type as a plain
		// literal — so the old whole-text capture left the #{...} markers
		// embedded verbatim in the key (bug-class #6: raw captured text).
		// Literal string_content chunks are kept verbatim; each
		// interpolation becomes a "*" wildcard hole.
		tmpl := rubyReconstructString(node, src)
		if !hasConcreteTemplateContent(tmpl) {
			return nil, true
		}
		return []string{tmpl}, false

	case "binary":
		if tmpl, ok := rubyReconstructConcat(node, src, depth); ok && hasConcreteTemplateContent(tmpl) {
			return []string{tmpl}, false
		}
		return nil, true

	case "if":
		// Ternary-style: `cond ? a : b` parses as if/else in Ruby
		thenClause := node.ChildByFieldName("consequence")
		elseClause := node.ChildByFieldName("alternative")
		if thenClause == nil || elseClause == nil {
			return nil, true
		}
		thenVals, thenDyn := walkRubyExpr(thenClause, src, consts, depth+1)
		if thenDyn {
			return nil, true
		}
		elseVals, elseDyn := walkRubyExpr(elseClause, src, consts, depth+1)
		if elseDyn {
			return nil, true
		}
		combined := append(thenVals, elseVals...)
		if len(combined) > keyWalkerMaxBranches {
			return nil, true
		}
		return combined, false

	case "constant":
		// Shape (b): Ruby constant (ALL_CAPS by convention)
		name := string(src[node.StartByte():node.EndByte()])
		if v, ok := consts(name); ok {
			return []string{v}, false
		}
		return nil, true

	default:
		return nil, true
	}
}

// rubyReconstructString reconstructs a Ruby string node (plain or
// interpolated — both parse as the same "string" node type) into a single
// wildcarded template: literal `string_content` chunks kept verbatim, each
// `#{...}` interpolation becomes a "*" wildcard hole. Quote delimiters are
// skipped.
func rubyReconstructString(node *sitter.Node, src []byte) string {
	var out strings.Builder
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case `"`, "'", "`":
			continue
		case "interpolation":
			out.WriteString("*")
		default: // string_content or other literal text chunk
			out.WriteString(string(src[child.StartByte():child.EndByte()]))
		}
	}
	return out.String()
}

// rubyReconstructConcat reconstructs a `+`/`<<`-chained string concatenation
// into a single wildcarded template: literal operands contribute their text
// verbatim, any other operand becomes a "*" hole. Depth-bounded like the
// ternary walker to avoid pathological trees.
func rubyReconstructConcat(node *sitter.Node, src []byte, depth int) (string, bool) {
	if depth > keyWalkerMaxDepth {
		return "", false
	}
	op := node.ChildByFieldName("operator")
	if op == nil {
		return "", false
	}
	opText := string(src[op.StartByte():op.EndByte()])
	if opText != "+" && opText != "<<" {
		return "", false
	}
	left := node.ChildByFieldName("left")
	right := node.ChildByFieldName("right")
	if left == nil || right == nil {
		return "", false
	}
	return rubyConcatSegment(left, src, depth+1) + rubyConcatSegment(right, src, depth+1), true
}

// rubyConcatSegment reconstructs one operand of a concatenation chain: a
// string literal verbatim, a nested `+`/`<<` chain recursively, anything
// else "*".
func rubyConcatSegment(node *sitter.Node, src []byte, depth int) string {
	if node == nil || depth > keyWalkerMaxDepth {
		return "*"
	}
	switch node.Type() {
	case "string", "simple_string":
		return rubyReconstructString(node, src)
	case "binary":
		if tmpl, ok := rubyReconstructConcat(node, src, depth); ok {
			return tmpl
		}
	}
	return "*"
}

func init() {
	RegisterKeyWalker(&rubyKeyWalker{})
}
