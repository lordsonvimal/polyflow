package linker

import (
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// hocNames are the React/MobX higher-order-component wrappers whose call result
// is the component itself — `X = observer(X)`, `const Y = memo(() => …)`,
// `export default forwardRef((props, ref) => …)`. The structural JS parser sees
// only the `call_expression` initializer, so a `const C = observer(arrow)` binding
// stays a plain `variable` node with no `renders` linkage; and a
// `class C {}; C = observer(C)` reassignment leaves the class node unmarked.
var hocNames = map[string]bool{
	"observer":   true,
	"memo":       true,
	"forwardRef": true,
	"withRouter": true,
	"pure":       true,
	"hot":        true,
}

// LinkJSHOC (Tier JCM.6) unwraps HOC wrapper identity so the render-tree graph
// lands on the real component:
//
//   - `const C = observer((props) => …)` / `memo(function …)` — the wrapper
//     variable node IS the component; stamp Meta["component"]="true" so
//     js_link Pass 1 will treat it as a `renders` target, plus Meta["hoc"].
//   - `C = observer(C)` / `export default observer(C)` where C names a
//     function/class/method declaration — stamp Meta["hoc"] on that declaration
//     (Pass 1 already resolves `<C/>` to it by label).
//
// It returns the affected nodes re-emitted with the added Meta (the caller
// upserts them by ID and replaces them in place in its working set — they
// already live there). No new nodes, no edges: the value is purely the Meta
// stamp, consumed by js_link Pass 1 (index) and JCM.7 (observer-render reads).
//
// Must run before js_link so Pass 1's render-target index sees the stamps.
func LinkJSHOC(nodes []graph.Node, serviceFiles map[string][]string) (updatedNodes []graph.Node) {
	// --- index existing nodes ---
	nodeIdx := make(map[string]int, len(nodes))
	varByFileLabel := make(map[string]string)  // file\x00label → id (NodeTypeVariable)
	declByFileLabel := make(map[string]string) // file\x00label → id (function/class/method)
	declBySvcLabel := make(map[string]string)  // service\x00label → id (first wins)
	svcOfFile := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		nodeIdx[n.ID] = i
		if n.Service != "" && n.File != "" {
			svcOfFile[n.File] = n.Service
		}
		switch n.Type {
		case graph.NodeTypeVariable:
			if _, ok := varByFileLabel[n.File+"\x00"+n.Label]; !ok {
				varByFileLabel[n.File+"\x00"+n.Label] = n.ID
			}
		case graph.NodeTypeFunction, graph.NodeTypeClass, graph.NodeTypeMethod:
			if n.Label == "(module)" {
				continue
			}
			if _, ok := declByFileLabel[n.File+"\x00"+n.Label]; !ok {
				declByFileLabel[n.File+"\x00"+n.Label] = n.ID
			}
			if _, ok := declBySvcLabel[n.Service+"\x00"+n.Label]; !ok {
				declBySvcLabel[n.Service+"\x00"+n.Label] = n.ID
			}
		}
	}

	updated := make(map[string]graph.Node)
	stamp := func(id, hoc string, asComponent bool, endLine int) {
		idx, ok := nodeIdx[id]
		if !ok {
			return
		}
		n := nodes[idx]
		if cur, done := updated[id]; done {
			n = cur
		}
		m := make(map[string]string, len(n.Meta)+3)
		for k, v := range n.Meta {
			m[k] = v
		}
		m["hoc"] = hoc
		m["tier"] = "jcm6"
		if asComponent {
			m["component"] = "true"
			if endLine > n.EndLine {
				n.EndLine = endLine
				m["end_line"] = strconv.Itoa(endLine)
			}
		}
		n.Meta = m
		updated[id] = n
	}

	seen := make(map[string]bool)
	for _, files := range serviceFiles {
		for _, abs := range files {
			if !isJSFile(abs) {
				continue
			}
			src, root, _, ok := jsParse(abs)
			if !ok {
				continue
			}
			rel := patterns.RelativizeToCwd(abs)
			if seen[rel] {
				continue
			}
			seen[rel] = true
			svc := svcOfFile[rel]
			s := string(src)
			if !hocMentioned(s) {
				continue
			}

			hocWalk(root, func(call *sitter.Node) {
				hoc := hocCalleeName(call, src)
				if hoc == "" {
					return
				}
				arg0 := hocFirstArg(call)
				if arg0 == nil {
					return
				}
				wrapperName, viaExport := hocBinding(call, src)
				if wrapperName == "" && !viaExport {
					return
				}
				endLine := int(call.EndPoint().Row) + 1

				switch arg0.Type() {
				case "arrow_function", "function_expression", "function":
					// Inline component — the wrapper variable node is canonical.
					if wrapperName == "" {
						// export default observer(function Named(){}) — stamp the
						// inner function node if it carries a name.
						if nm := arg0.ChildByFieldName("name"); nm != nil {
							if id := declByFileLabel[rel+"\x00"+nm.Content(src)]; id != "" {
								stamp(id, hoc, true, endLine)
							}
						}
						return
					}
					if id := varByFileLabel[rel+"\x00"+wrapperName]; id != "" {
						stamp(id, hoc, true, endLine)
					}
				case "identifier":
					inner := arg0.Content(src)
					id := declByFileLabel[rel+"\x00"+inner]
					if id == "" && svc != "" {
						id = declBySvcLabel[svc+"\x00"+inner]
					}
					if id != "" {
						stamp(id, hoc, false, 0)
					}
				}
			})
		}
	}

	for _, n := range updated {
		updatedNodes = append(updatedNodes, n)
	}
	return updatedNodes
}

func hocMentioned(s string) bool {
	for name := range hocNames {
		if strings.Contains(s, name+"(") {
			return true
		}
	}
	return false
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
