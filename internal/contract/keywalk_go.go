package contract

import (
	"regexp"

	sitter "github.com/smacker/go-tree-sitter"
)

// goKeyWalker enumerates literal alternatives for Go key expressions.
// Go lacks inline ternaries; the main shape (b) case is package-level constants.
type goKeyWalker struct{}

func (w *goKeyWalker) Language() string { return "go" }

func (w *goKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkGoExpr(node, src, consts, 0)
}

// goFormatVerb matches a printf verb (%s, %d, %-10.2f, %q, ...) so
// X.1b's Sprintf/Fprintf reconstruction can blank it to a wildcard hole.
var goFormatVerb = regexp.MustCompile(`%[#+\-0-9.]*[a-zA-Z]`)

func walkGoExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "interpreted_string_literal":
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "raw_string_literal":
		text := string(src[node.StartByte():node.EndByte()])
		// Raw strings use backticks
		return []string{stripKeyLiteral(text)}, false

	case "identifier":
		// Shape (b): package-level const reference
		name := string(src[node.StartByte():node.EndByte()])
		if v, ok := consts(name); ok {
			return []string{v}, false
		}
		return nil, true

	case "call_expression":
		if tmpl, ok := goReconstructCall(node, src); ok && hasConcreteTemplateContent(tmpl) {
			return []string{tmpl}, false
		}
		return nil, true

	case "binary_expression":
		if tmpl, ok := goReconstructConcat(node, src, depth); ok && hasConcreteTemplateContent(tmpl) {
			return []string{tmpl}, false
		}
		return nil, true

	default:
		return nil, true
	}
}

// goReconstructCall recognizes fmt.Sprintf/fmt.Fprintf/path.Join/url.JoinPath
// call shapes and reconstructs a single wildcarded template string (holes =
// "*") — a resolved, static-shaped key the existing param_wildcard +
// wildcard_anchored tiers already match against handler routes. Returns
// (_, false) for any other call shape, which the caller treats as dynamic.
func goReconstructCall(node *sitter.Node, src []byte) (string, bool) {
	fn := node.ChildByFieldName("function")
	if fn == nil || fn.Type() != "selector_expression" {
		return "", false
	}
	operand := fn.ChildByFieldName("operand")
	field := fn.ChildByFieldName("field")
	if operand == nil || field == nil {
		return "", false
	}
	pkg := string(src[operand.StartByte():operand.EndByte()])
	method := string(src[field.StartByte():field.EndByte()])
	args := node.ChildByFieldName("arguments")
	if args == nil {
		return "", false
	}

	switch {
	case pkg == "fmt" && (method == "Sprintf" || method == "Fprintf"):
		formatIdx := 0
		if method == "Fprintf" {
			formatIdx = 1 // arg0 is the io.Writer
		}
		formatNode := args.NamedChild(formatIdx)
		if formatNode == nil || !isGoStringLiteral(formatNode) {
			return "", false
		}
		literal := stripKeyLiteral(string(src[formatNode.StartByte():formatNode.EndByte()]))
		return goFormatVerb.ReplaceAllString(literal, "*"), true

	case (pkg == "path" || pkg == "filepath") && method == "Join",
		pkg == "url" && method == "JoinPath":
		n := int(args.NamedChildCount())
		segs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			arg := args.NamedChild(i)
			if arg != nil && isGoStringLiteral(arg) {
				segs = append(segs, stripKeyLiteral(string(src[arg.StartByte():arg.EndByte()])))
			} else {
				segs = append(segs, "*")
			}
		}
		return joinNonEmpty(segs, "/"), true

	default:
		return "", false
	}
}

// goReconstructConcat reconstructs a `+`-chained string concatenation into a
// single wildcarded template: literal operands contribute their text
// verbatim, any other operand becomes a "*" hole. Depth-bounded like the
// ternary walker to avoid pathological trees.
func goReconstructConcat(node *sitter.Node, src []byte, depth int) (string, bool) {
	if depth > keyWalkerMaxDepth {
		return "", false
	}
	op := node.ChildByFieldName("operator")
	if op == nil || string(src[op.StartByte():op.EndByte()]) != "+" {
		return "", false
	}
	left := node.ChildByFieldName("left")
	right := node.ChildByFieldName("right")
	if left == nil || right == nil {
		return "", false
	}
	return goConcatSegment(left, src, depth+1) + goConcatSegment(right, src, depth+1), true
}

// goConcatSegment reconstructs one operand of a concatenation chain:
// literal text verbatim, a nested `+` chain recursively, anything else "*".
func goConcatSegment(node *sitter.Node, src []byte, depth int) string {
	if node == nil || depth > keyWalkerMaxDepth {
		return "*"
	}
	if isGoStringLiteral(node) {
		return stripKeyLiteral(string(src[node.StartByte():node.EndByte()]))
	}
	if node.Type() == "binary_expression" {
		if tmpl, ok := goReconstructConcat(node, src, depth); ok {
			return tmpl
		}
	}
	return "*"
}

// isGoStringLiteral reports whether node is a Go string literal (interpreted
// or raw/backtick).
func isGoStringLiteral(node *sitter.Node) bool {
	switch node.Type() {
	case "interpreted_string_literal", "raw_string_literal":
		return true
	default:
		return false
	}
}

// joinNonEmpty joins segs with sep, skipping empty segments so a leading
// literal "" (an empty-string Join argument) doesn't produce a stray
// separator.
func joinNonEmpty(segs []string, sep string) string {
	out := ""
	for _, s := range segs {
		if s == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += s
	}
	return out
}

func init() {
	RegisterKeyWalker(&goKeyWalker{})
}
