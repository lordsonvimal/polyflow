package linker

import (
	"path/filepath"
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

// appHOCDenylist names call wrappers that take an identifier argument but do
// NOT return a component — so `export default createStore(rootReducer)` must
// never be mistaken for an app-local HOC (SPA.1). The uppercase-inner-arg guard
// already rejects most of these; this is belt-and-braces for the cases where a
// reducer/context happens to be PascalCase.
var appHOCDenylist = map[string]bool{
	"createStore":     true,
	"configureStore":  true,
	"combineReducers": true,
	"createContext":   true,
	"createRoot":      true,
	"hydrateRoot":     true,
	"require":         true,
	"import":          true,
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
// updatedNodes are affected nodes re-emitted with the added Meta (the caller
// upserts them by ID and replaces them in place in its working set — they
// already live there), consumed by js_link Pass 1 (index) and JCM.7
// (observer-render reads). newNodes / newEdges are non-empty only for the SPA.1
// app-local path below.
//
// SPA.1 — beyond the six library wrappers above, LinkJSHOC also recognises
// *app-local* HOCs structurally, without knowing the wrapper's name:
//
//	// react/components/CDM/CDMTopLevel.jsx
//	export default ComponentWithAjaxStatus(connect(msp, mdp)(CDMTopLevelInner));
//
// When the innermost non-call argument (peeling `f(g(h(X)))` and the
// `connect(a,b)(X)` curry) is an in-file, PascalCase component identifier, the
// default-export binding is treated as a component labelled by the file's
// basename (`CDMTopLevel`) so `<CDMTopLevel/>` render sites in a client router
// resolve. A synthetic `variable` node carrying Meta["component"]="true" is
// minted for the default export when no node already owns that label, plus a
// `component_impl` edge binding --> inner so a trace reaches the real body.
// For `const Name = wrap(Inner)` the existing `Name` node is stamped instead.
//
// Must run before js_link so Pass 1's render-target index sees the stamps.
func LinkJSHOC(nodes []graph.Node, serviceFiles map[string][]string) (updatedNodes, newNodes []graph.Node, newEdges []graph.Edge) {
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
	stampMeta := func(id string, extra map[string]string, asComponent bool, endLine int) {
		idx, ok := nodeIdx[id]
		if !ok {
			return
		}
		n := nodes[idx]
		if cur, done := updated[id]; done {
			n = cur
		}
		m := make(map[string]string, len(n.Meta)+len(extra)+3)
		for k, v := range n.Meta {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
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
	stamp := func(id, hoc string, asComponent bool, endLine int) {
		stampMeta(id, map[string]string{"hoc": hoc}, asComponent, endLine)
	}

	minted := make(map[string]graph.Node) // synthetic default-export component nodes, keyed by ID
	seenEdge := make(map[string]bool)
	var appEdges []graph.Edge

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
			// The structural app-local-HOC rule (SPA.1) needs any file whose export
		// binds a wrapper call; `export ` is the cheap common denominator
		// (`export default …`, `export { Wrapped }`, `export const C = …`).
		if !hocMentioned(s) && !strings.Contains(s, "export ") {
				continue
			}

			hocWalk(root, func(call *sitter.Node) {
				hoc := hocCalleeName(call, src)
				if hoc == "" {
					linkAppLocalHOC(call, src, rel, svc, declByFileLabel, varByFileLabel,
						nodeIdx, stampMeta, minted, seenEdge, &appEdges)
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
	for _, n := range minted {
		if _, exists := nodeIdx[n.ID]; exists {
			continue
		}
		newNodes = append(newNodes, n)
	}
	return updatedNodes, newNodes, appEdges
}

// linkAppLocalHOC handles the SPA.1 structural case: a `call_expression` that
// is not one of the six known library HOCs, but whose binding
// (`export default …` / `const Name = …`) wraps an in-file PascalCase component
// identifier. See LinkJSHOC's doc comment.
func linkAppLocalHOC(
	call *sitter.Node,
	src []byte,
	rel, svc string,
	declByFileLabel, varByFileLabel map[string]string,
	nodeIdx map[string]int,
	stampMeta func(string, map[string]string, bool, int),
	minted map[string]graph.Node,
	seenEdge map[string]bool,
	appEdges *[]graph.Edge,
) {
	name, viaExport := hocBinding(call, src)
	if name == "" && !viaExport {
		return
	}
	wrapper := hocOutermostCallee(call, src)
	if wrapper == "" || appHOCDenylist[wrapper] {
		return
	}
	argNode := unwrapCurried(call)
	if argNode == nil {
		return
	}
	// The wrapped value is either a named in-file component identifier
	// (`connect(...)(CDMTopLevelInner)`) or an inline class/arrow component
	// (`connect(...)(class extends React.Component { … })`). Anything else
	// (config object, string, plain function) is not an app-local HOC.
	var innerID string
	switch argNode.Type() {
	case "identifier":
		inner := argNode.Content(src)
		if !startsUpperASCII(inner) {
			return
		}
		innerID = declByFileLabel[rel+"\x00"+inner]
		if innerID == "" {
			innerID = varByFileLabel[rel+"\x00"+inner]
		}
		if innerID == "" {
			return // not resolvable in this file → out of SPA.1 scope
		}
	case "class", "class_declaration", "arrow_function", "function_expression", "function":
		if !looksLikeComponentNode(argNode, src) {
			return
		}
		// inline component — no separate node to bridge to
	default:
		return
	}

	endLine := int(call.EndPoint().Row) + 1
	extra := map[string]string{"hoc": "app_local", "hoc_wrapper": wrapper}
	if innerID != "" {
		extra["hoc_inner"] = innerID
	}

	var bindingID string
	if !viaExport {
		bindingID = varByFileLabel[rel+"\x00"+name]
		if bindingID == "" {
			bindingID = declByFileLabel[rel+"\x00"+name]
		}
		if bindingID == "" {
			return
		}
		stampMeta(bindingID, extra, true, endLine)
	} else {
		base := basenameNoExt(rel)
		if !startsUpperASCII(base) {
			return
		}
		bindingID = varByFileLabel[rel+"\x00"+base]
		if bindingID == "" {
			bindingID = declByFileLabel[rel+"\x00"+base]
		}
		if bindingID == "" {
			bindingID = string(graph.NodeTypeVariable) + ":" + svc + ":" + rel + ":" + base
		}
		if _, exists := nodeIdx[bindingID]; exists {
			stampMeta(bindingID, extra, true, endLine)
		} else if _, done := minted[bindingID]; !done {
			m := map[string]string{
				"component": "true", "tier": "jcm6", "scope": "module",
				"end_line": strconv.Itoa(endLine),
			}
			for k, v := range extra {
				m[k] = v
			}
			minted[bindingID] = graph.Node{
				ID:       bindingID,
				Type:     graph.NodeTypeVariable,
				Label:    base,
				Service:  svc,
				File:     rel,
				Line:     int(call.StartPoint().Row) + 1,
				EndLine:  endLine,
				Language: "javascript",
				Meta:     m,
			}
		}
	}

	if bindingID == "" || innerID == "" || bindingID == innerID {
		return
	}
	edgeID := "component_impl:" + bindingID + "->" + innerID
	if seenEdge[edgeID] {
		return
	}
	seenEdge[edgeID] = true
	*appEdges = append(*appEdges, graph.Edge{
		ID:   edgeID,
		From: bindingID,
		To:   innerID,
		Type: graph.EdgeTypeComponentImpl,
		Meta: map[string]string{"via": "app_hoc", "wrapper": wrapper},
	})
}

// hocOutermostCallee returns the name of the wrapper actually applied to the
// component: the identifier / member-property at the head of the (possibly
// curried) call chain — `connect(a,b)(X)` → "connect",
// `ComponentWithAjaxStatus(…)` → "ComponentWithAjaxStatus".
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

// unwrapCurried peels `wrap(...(Inner)...)` and `connect(a,b)(Inner)` down to
// the first non-call argument node (typically an identifier).
func unwrapCurried(call *sitter.Node) *sitter.Node {
	cur := call
	for i := 0; i < 8 && cur != nil && cur.Type() == "call_expression"; i++ {
		args := cur.ChildByFieldName("arguments")
		if args == nil || args.NamedChildCount() == 0 {
			return nil
		}
		arg := args.NamedChild(0)
		if arg.Type() == "parenthesized_expression" && arg.NamedChildCount() > 0 {
			arg = arg.NamedChild(0)
		}
		if arg.Type() == "call_expression" {
			cur = arg
			continue
		}
		return arg
	}
	return nil
}

// looksLikeComponentNode reports whether an inline class/arrow/function node is
// a React component: a class whose superclass mentions `Component`, or a
// function/arrow with a JSX element somewhere in its body. Guards SPA.1's
// inline-component path against `wrap(function(){ return 42 })` factories.
func looksLikeComponentNode(n *sitter.Node, src []byte) bool {
	switch n.Type() {
	case "class", "class_declaration":
		if sc := n.ChildByFieldName("superclass"); sc != nil {
			return strings.Contains(sc.Content(src), "Component")
		}
		// `class extends X` where the heritage clause is not a "superclass"
		// field in this grammar — fall back to a substring check on the head.
		head := n.Content(src)
		if i := strings.Index(head, "{"); i > 0 {
			head = head[:i]
		}
		return strings.Contains(head, "Component")
	default:
		return jsxWithin(n, 0)
	}
}

func jsxWithin(n *sitter.Node, depth int) bool {
	if n == nil || depth > 40 {
		return false
	}
	switch n.Type() {
	case "jsx_element", "jsx_self_closing_element", "jsx_fragment":
		return true
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if jsxWithin(n.NamedChild(i), depth+1) {
			return true
		}
	}
	return false
}

func basenameNoExt(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func startsUpperASCII(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
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
