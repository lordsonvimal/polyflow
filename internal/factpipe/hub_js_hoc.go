package factpipe

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_js_hoc.go is the "js_hoc_sites" hub provider (see hub.go) — the Tier
// FX FX.8.8 migration of internal/linker/js_hoc.go's retired LinkJSHOC.
//
// Every piece of this pass is real, order-sensitive tree-sitter structural
// analysis, not "one AST shape, one value" a `.dl` rule could express: an
// arbitrary-depth curried-call peel (`wrap(a,b)(inner)`, up to 8 levels), a
// recursive JSX-presence scan of a function/class body, a class-superclass
// substring check, and — the part that most needs the graph-so-far, not just
// the file being parsed — resolving a bare `identifier` HOC argument against
// an index of every function/class/method/variable node in the SAME file
// (declByFileLabel/varByFileLabel) or, cross-file, the same service
// (declBySvcLabel). This is exactly the class of hub the pusher_producer
// (FX.8.11) and rails_route_actions (FX.8.24) hubs already established: the
// discovery walk stays hand-written Go, and it does the index-building AND
// resolution itself (like rails_route_actions did for both join sides),
// rather than shipping a raw AST dump for a `.dl` join to attempt — no `.dl`
// rule can peel a curry or recurse a JSX scan.
//
// The hub emits three already-resolved fact predicates; rules/javascript/
// js_hoc.dl is pure pass-through (no `.dl`-side logic at all, same shape as
// pusher_producer.dl's mint relation):
//
//   - js_hoc_patch(TargetID, Hoc, Wrapper, Inner, Component, EndLine) — an
//     existing node gets its Meta patched via `patch:` (FX.8.8's new emit
//     primitive, internal/factpipe/emit.go), NOT `mint:`. mint:/replace:
//     rebuild a node from scratch out of only the keys the spec names, which
//     would silently drop every OTHER meta key an earlier parse-time or
//     link pass already stamped on that function/class/method/variable node
//     (js_variables.go's global_symbol/global_path, etc. — the exact
//     `nav_link` regression class FX.8.7 hit, but on nodes whose meta set is
//     open-ended rather than narrow, so a `patch:` merge is required, not
//     just more diligent key enumeration).
//   - js_hoc_mint(ID, Label, Svc, File, Line, EndLine, Wrapper) — SPA.1's
//     synthetic default-export component, minted only when no existing node
//     already owns that file+label (brand new node, `mint:` is exactly
//     right here — nothing pre-existing to preserve).
//   - js_hoc_edge(BindingID, InnerID, Wrapper) — SPA.1's component_impl
//     bridge from the wrapper's own binding to the real inner component.
//
// Gate: any service with at least one JS/TS file — cheap and mirrors the
// retired pass, which had no node-type gate of its own (LinkJSHOC ran
// unconditionally over every JS file).
func init() { RegisterHub("js_hoc_sites", jsHOCSitesHub) }

const (
	jsHOCPatchPred = "js_hoc_patch"
	jsHOCMintPred  = "js_hoc_mint"
	jsHOCEdgePred  = "js_hoc_edge"
)

// jhcHOCNames / jhcAppHOCDenylist: ported verbatim from js_hoc.go's
// hocNames / appHOCDenylist — see that file's retired doc comments.
var jhcHOCNames = map[string]bool{
	"observer": true, "memo": true, "forwardRef": true,
	"withRouter": true, "pure": true, "hot": true,
}

var jhcAppHOCDenylist = map[string]bool{
	"createStore": true, "configureStore": true, "combineReducers": true,
	"createContext": true, "createRoot": true, "hydrateRoot": true,
	"require": true, "import": true,
}

func jsHOCSitesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	nodeIdx := make(map[string]bool, len(nodes))
	varByFileLabel := make(map[string]string)
	declByFileLabel := make(map[string]string)
	declBySvcLabel := make(map[string]string)
	svcOfFile := make(map[string]string)
	// defaultSvc: svcOfFile only resolves a file that owns at least one
	// function/class/variable/method node — a file whose only HOC call is
	// an app-local default export wrapping an inline class/arrow (no
	// separately-declared identifier in that file) can have none. Falls
	// back to "the service this whole call's nodes belong to", correct as
	// long as the caller (internal/indexer/link_passes.go) runs this hub
	// once per service — the same fix FX.8.10 (js_client_routes) needed;
	// see docs/declarative-framework-pipeline-plan.md's FX.8.10 row. Before
	// this fix, this hub used ONE global svc (the first non-empty Service
	// seen across the WHOLE nodes slice) for every file regardless of which
	// service it belonged to — silently wrong for every service after the
	// first in a multi-service call.
	defaultSvc := ""
	for i := range nodes {
		n := &nodes[i]
		nodeIdx[n.ID] = true
		if n.Service != "" && n.File != "" {
			svcOfFile[n.File] = n.Service
		}
		if defaultSvc == "" && n.Service != "" {
			defaultSvc = n.Service
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

	var jsFiles []string
	for _, f := range files {
		if jhcIsJSFile(f) {
			jsFiles = append(jsFiles, f)
		}
	}
	if len(jsFiles) == 0 {
		return nil
	}
	sort.Strings(jsFiles)

	type accum struct {
		hoc, wrapper, inner string
		component            bool
		endLine              int
	}
	patched := map[string]*accum{}
	patchOrder := []string{}
	stamp := func(id, hoc, wrapper, inner string, asComponent bool, endLine int) {
		if id == "" {
			return
		}
		a, ok := patched[id]
		if !ok {
			a = &accum{}
			patched[id] = a
			patchOrder = append(patchOrder, id)
		}
		a.hoc = hoc
		if wrapper != "" {
			a.wrapper = wrapper
		}
		if inner != "" {
			a.inner = inner
		}
		if asComponent {
			a.component = true
			if endLine > a.endLine {
				a.endLine = endLine
			}
		}
	}

	minted := map[string]Fact{}
	var mintedOrder []string
	seenEdge := map[string]bool{}
	var edges []Fact

	seenFile := map[string]bool{}
	for _, abs := range jsFiles {
		src, root, ok := jhcParseJS(abs)
		if !ok || root == nil {
			continue
		}
		rel := jhcRelativize(abs)
		if seenFile[rel] {
			continue
		}
		seenFile[rel] = true
		svc := svcOfFile[rel]
		if svc == "" {
			svc = defaultSvc
		}

		s := string(src)
		if !jhcHOCMentioned(s) && !strings.Contains(s, "export ") {
			continue
		}

		jhcWalk(root, func(call *sitter.Node) {
			hoc := jhcCalleeName(call, src)
			if hoc == "" {
				jhcLinkAppLocal(call, src, rel, svc, declByFileLabel, varByFileLabel,
					nodeIdx, stamp, minted, &mintedOrder, seenEdge, &edges)
				return
			}
			arg0 := jhcFirstArg(call)
			if arg0 == nil {
				return
			}
			wrapperName, viaExport := jhcBinding(call, src)
			if wrapperName == "" && !viaExport {
				return
			}
			endLine := int(call.EndPoint().Row) + 1

			switch arg0.Type() {
			case "arrow_function", "function_expression", "function":
				if wrapperName == "" {
					if nm := arg0.ChildByFieldName("name"); nm != nil {
						if id := declByFileLabel[rel+"\x00"+nm.Content(src)]; id != "" {
							stamp(id, hoc, "", "", true, endLine)
						}
					}
					return
				}
				if id := varByFileLabel[rel+"\x00"+wrapperName]; id != "" {
					stamp(id, hoc, "", "", true, endLine)
				}
			case "identifier":
				inner := arg0.Content(src)
				id := declByFileLabel[rel+"\x00"+inner]
				if id == "" && svc != "" {
					id = declBySvcLabel[svc+"\x00"+inner]
				}
				if id != "" {
					stamp(id, hoc, "", "", false, 0)
				}
			}
		})
	}

	var out []Fact
	for _, id := range patchOrder {
		a := patched[id]
		comp := ""
		if a.component {
			comp = "true"
		}
		out = append(out, Fact{
			Pred: jsHOCPatchPred,
			Args: []Atom{
				Str(id), Str(a.hoc), Str(a.wrapper), Str(a.inner),
				Str(comp), Int(int64(a.endLine)),
			},
			Origin: Origin{Kind: OriginPrimitive, Pattern: jsHOCPatchPred},
		})
	}
	for _, id := range mintedOrder {
		if nodeIdx[id] {
			continue
		}
		out = append(out, minted[id])
	}
	out = append(out, edges...)
	return out
}

// jhcLinkAppLocal handles SPA.1 — ported from js_hoc.go's linkAppLocalHOC.
func jhcLinkAppLocal(
	call *sitter.Node, src []byte, rel, svc string,
	declByFileLabel, varByFileLabel map[string]string,
	nodeIdx map[string]bool,
	stamp func(id, hoc, wrapper, inner string, asComponent bool, endLine int),
	minted map[string]Fact, mintedOrder *[]string,
	seenEdge map[string]bool, edges *[]Fact,
) {
	name, viaExport := jhcBinding(call, src)
	if name == "" && !viaExport {
		return
	}
	wrapper := jhcOutermostCallee(call, src)
	if wrapper == "" || jhcAppHOCDenylist[wrapper] {
		return
	}
	argNode := jhcUnwrapCurried(call)
	if argNode == nil {
		return
	}

	var innerID string
	switch argNode.Type() {
	case "identifier":
		inner := argNode.Content(src)
		if !jhcStartsUpperASCII(inner) {
			return
		}
		innerID = declByFileLabel[rel+"\x00"+inner]
		if innerID == "" {
			innerID = varByFileLabel[rel+"\x00"+inner]
		}
		if innerID == "" {
			return
		}
	case "class", "class_declaration", "arrow_function", "function_expression", "function":
		if !jhcLooksLikeComponentNode(argNode, src) {
			return
		}
	default:
		return
	}

	endLine := int(call.EndPoint().Row) + 1

	var bindingID string
	if !viaExport {
		bindingID = varByFileLabel[rel+"\x00"+name]
		if bindingID == "" {
			bindingID = declByFileLabel[rel+"\x00"+name]
		}
		if bindingID == "" {
			return
		}
		stamp(bindingID, "app_local", wrapper, innerID, true, endLine)
	} else {
		base := jhcBasenameNoExt(rel)
		if !jhcStartsUpperASCII(base) {
			return
		}
		bindingID = varByFileLabel[rel+"\x00"+base]
		if bindingID == "" {
			bindingID = declByFileLabel[rel+"\x00"+base]
		}
		if bindingID == "" {
			bindingID = string(graph.NodeTypeVariable) + ":" + svc + ":" + rel + ":" + base
		}
		if nodeIdx[bindingID] {
			stamp(bindingID, "app_local", wrapper, innerID, true, endLine)
		} else if _, done := minted[bindingID]; !done {
			minted[bindingID] = Fact{
				Pred: jsHOCMintPred,
				Args: []Atom{
					Str(bindingID), Str(base), Str(svc), Str(rel),
					Int(int64(call.StartPoint().Row) + 1), Int(int64(endLine)), Str(wrapper),
				},
				Origin: Origin{Kind: OriginPrimitive, File: rel, Line: int(call.StartPoint().Row) + 1, Pattern: jsHOCMintPred},
			}
			*mintedOrder = append(*mintedOrder, bindingID)
		}
	}

	if bindingID == "" || innerID == "" || bindingID == innerID {
		return
	}
	edgeKey := "component_impl:" + bindingID + "->" + innerID
	if seenEdge[edgeKey] {
		return
	}
	seenEdge[edgeKey] = true
	*edges = append(*edges, Fact{
		Pred:   jsHOCEdgePred,
		Args:   []Atom{Str(bindingID), Str(innerID), Str(wrapper)},
		Origin: Origin{Kind: OriginPrimitive, Pattern: jsHOCEdgePred},
	})
}

// --- ported structural helpers (jhc-prefixed, from js_hoc.go) ---

func jhcOutermostCallee(call *sitter.Node, src []byte) string {
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

func jhcUnwrapCurried(call *sitter.Node) *sitter.Node {
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

func jhcLooksLikeComponentNode(n *sitter.Node, src []byte) bool {
	switch n.Type() {
	case "class", "class_declaration":
		if sc := n.ChildByFieldName("superclass"); sc != nil {
			return strings.Contains(sc.Content(src), "Component")
		}
		head := n.Content(src)
		if i := strings.Index(head, "{"); i > 0 {
			head = head[:i]
		}
		return strings.Contains(head, "Component")
	default:
		return jhcJSXWithin(n, 0)
	}
}

func jhcJSXWithin(n *sitter.Node, depth int) bool {
	if n == nil || depth > 40 {
		return false
	}
	switch n.Type() {
	case "jsx_element", "jsx_self_closing_element", "jsx_fragment":
		return true
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if jhcJSXWithin(n.NamedChild(i), depth+1) {
			return true
		}
	}
	return false
}

func jhcBasenameNoExt(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func jhcStartsUpperASCII(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

func jhcHOCMentioned(s string) bool {
	for name := range jhcHOCNames {
		if strings.Contains(s, name+"(") {
			return true
		}
	}
	return false
}

func jhcCalleeName(call *sitter.Node, src []byte) string {
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
	if jhcHOCNames[name] {
		return name
	}
	return ""
}

func jhcFirstArg(call *sitter.Node) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return nil
	}
	return args.NamedChild(0)
}

func jhcBinding(call *sitter.Node, src []byte) (name string, viaExport bool) {
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

func jhcWalk(n *sitter.Node, fn func(*sitter.Node)) {
	if n.Type() == "call_expression" {
		fn(n)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		jhcWalk(n.NamedChild(i), fn)
	}
}

func jhcIsJSFile(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".js", ".jsx", ".mjs", ".es6", ".ts", ".tsx":
		return true
	}
	return false
}

// jhcParseJS is pjcParseJS (hub_pusher_js_consumer.go) in all but name — kept
// as its own binding so a js_hoc-specific extension doesn't ripple into the
// pusher hub's call sites.
func jhcParseJS(file string) (src []byte, root *sitter.Node, ok bool) {
	return pjcParseJS(file)
}

// jhcRelativize mirrors internal/patterns.RelativizeToCwd (matcher.go) — this
// package cannot import internal/patterns (it imports internal/factpipe, an
// import cycle), so the small, stable cwd-relativization logic is
// duplicated rather than restructured.
var jhcRawCwd = sync.OnceValue(func() string {
	cwd, _ := os.Getwd()
	return cwd
})

var jhcCanonCwd = sync.OnceValue(func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
})

func jhcRelativize(file string) string {
	if raw := jhcRawCwd(); raw != "" && filepath.IsAbs(file) {
		if rel, err := filepath.Rel(raw, file); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	cwd := jhcCanonCwd()
	if cwd == "" || !filepath.IsAbs(file) {
		return file
	}
	canon := file
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		canon = resolved
	}
	rel, err := filepath.Rel(cwd, canon)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return file
	}
	return filepath.ToSlash(rel)
}
