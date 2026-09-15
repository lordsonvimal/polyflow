package factpipe

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// hub_react_prop_urls.go is the "react_prop_urls" hub provider (see hub.go)
// — the Tier FX migration of the retired internal/linker/react_prop_urls.go's
// LinkReactPropURLs.
//
// Every piece of this pass needs real, order-sensitive cross-language
// extraction the "AST shape + pull a value" convergence rule
// (docs/declarative-framework-pipeline-plan.md) explicitly excludes from a
// tree-sitter-query-plus-`.dl`-join shape: an ERB `react_component(...)`
// props-hash scan joined against a Rails route_helper table (Ruby-side), a
// separate JSX-attribute scan for prop-to-prop forwarding, a component
// registry built the same way rails_views.go's componentIndex is (global
// `window.Foo = Foo` symbol, cross-service), a one-hop local-variable-
// assignment trace on the JS call site, and a destructuring-pattern check to
// tell a genuine prop reference from a same-named local. None of this is a
// bounded join a `.dl` rule could express; it stays hand-written Go, same
// class of hub as js_hoc_sites (hub_js_hoc.go) and ruby_host_registry
// (ruby_polymorphic_path.go's shared infra) — the algorithm doesn't shrink,
// only the plumbing around it becomes declarative (a `patch:`-only `.dl`
// pass-through, `patterns/generic/react_prop_urls.yaml`).
//
// language: generic (not javascript) is deliberate, not cosmetic: this hub
// needs the WHOLE graph and every service's file list in one call (a
// component's Rails ERB and its JSX implementation are routinely different
// services), which the shared per-service factpipe_frameworks loop
// (internal/indexer/factpipe_pass.go) cannot give it — patternLangForFile has
// no case for "generic", so that loop always sees an empty file list for
// this framework and skips it; only the dedicated pipeline.Run call in
// internal/indexer/link_passes.go's "react_prop_urls" pass invokes it, over
// st.allNodes (whole graph) and every service's files flattened. Same
// shielding mechanism as amqp_handshake/hints/config_baseurl/js_http_grade —
// see docs/declarative-framework-pipeline-plan.md's react_prop_urls row.
func init() { RegisterHub("react_prop_urls", reactPropURLsHub) }

const reactPropURLsPatchPred = "react_prop_urls_patch"

func reactPropURLsHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint) []Fact {
	type routeInfo struct{ url, method string }

	// 1. route_helper → routes. App-global: a component's Rails side and the
	// API it calls are the same application even when split into services.
	helpers := map[string][]routeInfo{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		h, p := n.Meta["route_helper"], n.Meta["path"]
		if h == "" || !strings.HasPrefix(p, "/") {
			continue
		}
		helpers[h] = append(helpers[h], routeInfo{url: rprColonToWildcard(p), method: strings.ToUpper(n.Meta["method"])})
	}
	if len(helpers) == 0 {
		return nil
	}

	resolvePropURL := func(val string) (string, bool) {
		val = strings.TrimSpace(val)
		if lit, _, ok := railsview.LeadingLiteral(val); ok {
			if strings.HasPrefix(lit, "/") {
				return rprColonToWildcard(lit), true
			}
			return "", false
		}
		head := val
		if i := strings.IndexByte(head, '('); i >= 0 {
			head = head[:i]
		}
		head = strings.TrimSpace(head)
		if head == "" || strings.ContainsAny(head, ". \t?:[]{}\"'`") {
			return "", false // method chain, receiver, ternary, interpolation
		}
		var base string
		switch {
		case strings.HasSuffix(head, "_url"):
			base = head[:len(head)-len("_url")]
		case strings.HasSuffix(head, "_path"):
			base = head[:len(head)-len("_path")]
		default:
			return "", false
		}
		ris := helpers[base]
		if len(ris) == 0 {
			return "", false
		}
		u := ris[0].url
		for _, r := range ris[1:] {
			if r.url != u {
				return "", false // helper spans routes with different paths — ambiguous
			}
		}
		return u, true
	}

	// 2. (component name, prop name) → resolved url, conflict-aware.
	type propKey struct{ comp, prop string }
	propURL := map[propKey]string{}
	propBad := map[propKey]bool{}
	record := func(comp, prop, url string) {
		k := propKey{comp, prop}
		if propBad[k] {
			return
		}
		if cur, ok := propURL[k]; ok {
			if cur != url {
				delete(propURL, k)
				propBad[k] = true
			}
			return
		}
		propURL[k] = url
	}
	// 2b. JSX → JSX prop forwarding. Not every URL prop originates on the
	// Rails side: `<JobDetailModal url={`/app/lro/${lroId}?study_id=${sid}`} />`
	// in one component passes a literal/template path straight to another.
	// The child's `apiGet(url)` call site is still minted `key_dynamic`.
	rprScanJSXPropURLs(files, record)

	erbSeen := map[string]bool{}
	for _, f := range files {
		if !strings.HasSuffix(f, ".erb") || erbSeen[f] {
			continue
		}
		erbSeen[f] = true
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		_, ruby := railsview.SplitERB(src)
		for _, rc := range railsview.ScanReactComponents(ruby) {
			if rc.Dynamic {
				continue
			}
			for _, p := range rc.Props {
				if u, ok := resolvePropURL(p.Value); ok {
					record(rc.Name, p.Name, u)
				}
			}
		}
	}
	if len(propURL) == 0 {
		return nil
	}

	// 3. component name → implementation files (cross-service; the JSX is
	// often its own service). Ports rails_views.go's newComponentIndex's
	// bySymbol-building half (only that half is used here — no barrel
	// resolution) since internal/factpipe cannot call internal/linker's
	// unexported helper.
	nodeFile := map[string]string{}
	svcSet := map[string]bool{}
	for i := range nodes {
		nodeFile[nodes[i].ID] = nodes[i].File
		svcSet[nodes[i].Service] = true
	}
	compsByFile := map[string]map[string]bool{}
	addComp := func(f, sym string) {
		if f == "" || sym == "" {
			return
		}
		if compsByFile[f] == nil {
			compsByFile[f] = map[string]bool{}
		}
		compsByFile[f][sym] = true
	}
	for svc := range svcSet {
		for sym, ids := range rprComponentsBySymbol(nodes, svc) {
			for _, id := range ids {
				addComp(nodeFile[id], sym)
			}
		}
	}
	// A JSX component that only forwards a prop to another (JobDetailModal)
	// is often never registered on `window` — the registry above can't see
	// it. Fall back to its function/class declaration: a Capitalized
	// top-level name in the file it's defined in. record()'s conflict guard
	// covers name clashes.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeClass {
			continue
		}
		if l := n.Label; l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			addComp(n.File, l)
		}
	}

	// 4. resolve + emit.
	localSrc := map[string][]byte{}
	localRoot := map[string]*sitter.Node{}
	fileTree := func(file string) (*sitter.Node, []byte) {
		root, ok := localRoot[file]
		if !ok {
			src, r, _, parsed := rprParseJS(file)
			if parsed {
				localSrc[file], localRoot[file] = src, r
				root = r
			}
			localRoot[file] = root // cache even nil
		}
		return root, localSrc[file]
	}
	localSource := func(file, name string, line int) string {
		root, src := fileTree(file)
		if root == nil {
			return ""
		}
		return rprLastAssignmentBefore(root, src, name, line)
	}
	// isPropParam reports whether name is bound by a destructuring pattern
	// in file — a component prop parameter (`({ url }) => …`) or a
	// `const { url } = props`. Distinguishes a genuine prop reference from a
	// same-named local.
	isPropParam := func(file, name string) bool {
		root, src := fileTree(file)
		if root == nil {
			return false
		}
		return rprDestructuresName(root, src, name)
	}

	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient || n.Meta["pattern"] != "js_api_wrapper_call_site" || n.Meta["key_dynamic"] != "true" {
			continue
		}
		urlExpr := n.Meta["url_expr"]
		if urlExpr == "" {
			urlExpr = n.Meta["key_dynamic_raw"]
		}
		prop := rprLeadingJSIdent(urlExpr)
		// Only a `*_url`/`*_path`/`*_uri` identifier is taken as the prop
		// itself. A bare `url` at the call site is far more often a local
		// variable (`const url = some_url_prop.replace(...)`) shadowing the
		// prop, so follow one hop of local assignment:
		// `const url = add_lro_details_url.replace(…)` →
		// `add_lro_details_url`. Anything past one hop, or a non-prop
		// source, still abstains.
		if !rprHasURLSuffix(prop) {
			if src := rprLeadingJSIdent(localSource(n.File, prop, n.Line)); rprHasURLSuffix(src) {
				prop = src
			} else if !isPropParam(n.File, prop) {
				// Not a `*_url` prop, no one-hop local assignment from one,
				// and not a destructured prop parameter — abstain
				// (local-var risk).
				continue
			}
			// else: `function JobDetailModal({ url }) { apiGet(url) }` — the
			// bare identifier is the prop parameter itself.
		}
		var resolved string
		conflict := false
		for comp := range compsByFile[n.File] {
			u, ok := propURL[propKey{comp, prop}]
			if !ok {
				continue
			}
			if resolved != "" && resolved != u {
				conflict = true
				break
			}
			resolved = u
		}
		if conflict || resolved == "" {
			continue
		}
		method := rprWrapperMethod(n.Meta["wrapper"])
		finalMethod := ""
		if n.Meta["method"] == "" && method != "" {
			finalMethod = method
		}
		label := ""
		if n.Label == "" || n.Label == "dynamic" {
			label = strings.TrimSpace(method + " " + resolved)
		}
		ownerService := ""
		if n.Meta["owner_service"] == "" {
			ownerService = n.Service
		}
		out = append(out, Fact{
			Pred: reactPropURLsPatchPred,
			Args: []Atom{Node(n.ID), Str(resolved), Str(finalMethod), Str(label), Str(ownerService)},
			Origin: Origin{
				Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "react_prop_urls",
			},
		})
	}
	return out
}

// rprComponentsBySymbol ports rails_views.go's newComponentIndex's
// bySymbol-building half, restricted to one service — the only piece
// react_prop_urls needs (no barrel resolution).
func rprComponentsBySymbol(nodes []graph.Node, svc string) map[string][]string {
	regFiles := map[string][]string{}
	implID := map[string]string{}
	varID := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc || n.Meta["is_test"] == "true" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeClass:
			key := n.File + "\x00" + n.Label
			if _, dup := implID[key]; !dup {
				implID[key] = n.ID
			}
		case graph.NodeTypeVariable:
			sym := n.Meta["global_symbol"]
			if sym == "" || n.Meta["scope"] != "global" {
				continue
			}
			regFiles[sym] = append(regFiles[sym], n.File)
			varID[sym+"\x00"+n.File] = n.ID
		}
	}
	out := map[string][]string{}
	for sym, fs := range regFiles {
		sort.Strings(fs)
		for _, f := range fs {
			if id, ok := implID[f+"\x00"+sym]; ok {
				out[sym] = append(out[sym], id)
			} else if id, ok := varID[sym+"\x00"+f]; ok {
				out[sym] = append(out[sym], id)
			}
		}
	}
	return out
}

// rprJSXPropURLQuery captures `<Component prop={"..."|`...`} />` and
// `<Component prop="..." />` — a string or template literal passed as a JSX
// attribute value.
const rprJSXPropURLQuery = `
[
  (jsx_opening_element
    name: (_) @tag
    (jsx_attribute (property_identifier) @prop (jsx_expression [(string) (template_string)] @val)))
  (jsx_self_closing_element
    name: (_) @tag
    (jsx_attribute (property_identifier) @prop (jsx_expression [(string) (template_string)] @val)))
  (jsx_opening_element
    name: (_) @tag
    (jsx_attribute (property_identifier) @prop (string) @val))
  (jsx_self_closing_element
    name: (_) @tag
    (jsx_attribute (property_identifier) @prop (string) @val))
]`

// rprCompiledJSXQuery compiles rprJSXPropURLQuery against the tsx grammar
// only — rprScanJSXPropURLs' caller pre-filters to .jsx/.tsx files, so
// rprParseJS always selects tsxsitter for them; the plain "typescript"
// grammar has no jsx_* node types at all, so a query using them fails to
// compile against it (there is no non-JSX call path to serve).
var (
	rprJSXQuery     *sitter.Query
	rprJSXQueryErr  error
	rprJSXQueryOnce sync.Once
)

func rprCompiledJSXQuery(bool) (*sitter.Query, error) {
	rprJSXQueryOnce.Do(func() {
		rprJSXQuery, rprJSXQueryErr = sitter.NewQuery([]byte(rprJSXPropURLQuery), tsxsitter.GetLanguage())
	})
	return rprJSXQuery, rprJSXQueryErr
}

// rprScanJSXPropURLs walks every .jsx/.tsx file for JSX elements that pass a
// literal or template-literal path as a prop
// (`<Modal url={`/app/lro/${id}`} />`) and feeds each
// (component, prop, resolved-path) to record.
func rprScanJSXPropURLs(files []string, record func(comp, prop, url string)) {
	seen := map[string]bool{}
	for _, f := range files {
		if ext := strings.ToLower(filepath.Ext(f)); ext != ".jsx" && ext != ".tsx" {
			continue
		}
		if graph.IsTestFilePath(f) {
			continue // a test's `<Modal url="/fixture" />` isn't a real forward
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		src, root, isJSX, ok := rprParseJS(f)
		if !ok {
			continue
		}
		q, err := rprCompiledJSXQuery(isJSX)
		if err != nil || q == nil {
			continue
		}
		cur := sitter.NewQueryCursor()
		cur.Exec(q, root)
		for {
			m, ok := cur.NextMatch()
			if !ok {
				break
			}
			caps := map[string]string{}
			for _, c := range m.Captures {
				caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
			}
			tag, prop, val := caps["tag"], caps["prop"], caps["val"]
			if i := strings.LastIndexByte(tag, '.'); i >= 0 {
				tag = tag[i+1:]
			}
			if tag == "" || prop == "" || tag[0] < 'A' || tag[0] > 'Z' {
				continue // lowercase tag = HTML element
			}
			if u, ok := rprResolveJSPropURL(val); ok {
				record(tag, prop, u)
			}
		}
	}
}

var rprTemplateSubst = regexp.MustCompile(`\$\{[^{}]*\}`)

// rprResolveJSPropURL turns a JS string / template-literal source into a
// root-relative wildcard path: `"/app/lro/5"` → `/app/lro/5`,
// “ `/app/lro/${id}?study_id=${s}` “ → `/app/lro/*`. Anything not starting
// with a literal `/` (a bare identifier, an interpolation-led template)
// fails.
func rprResolveJSPropURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != raw[len(raw)-1] {
		return "", false
	}
	var body string
	switch raw[0] {
	case '"', '\'':
		body = raw[1 : len(raw)-1]
	case '`':
		body = rprTemplateSubst.ReplaceAllString(raw[1:len(raw)-1], "*")
	default:
		return "", false
	}
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	if i := strings.IndexAny(body, "?#"); i >= 0 {
		body = body[:i]
	}
	segs := strings.Split(body, "/")
	for i, s := range segs {
		if strings.Contains(s, "*") {
			segs[i] = "*"
		}
	}
	body = strings.TrimRight(strings.Join(segs, "/"), "/")
	if body == "" {
		return "", false
	}
	return body, true
}

// rprDestructuresName reports whether name appears as a key in any object
// destructuring pattern in the tree (`({ name }) => …`, `const { name } = x`).
func rprDestructuresName(root *sitter.Node, src []byte, name string) bool {
	found := false
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		if n.Type() == "object_pattern" {
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				switch c.Type() {
				case "shorthand_property_identifier_pattern":
					if c.Content(src) == name {
						found = true
						return
					}
				case "pair_pattern":
					if k := c.ChildByFieldName("key"); k != nil && k.Content(src) == name {
						found = true
						return
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return found
}

// rprColonToWildcard turns a Rails route path template into the client-side
// wildcard form the js http_client nodes use: `/x/:id/y` → `/x/*/y`,
// `/files/*path` → `/files/*`.
func rprColonToWildcard(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, "/")
}

// rprLeadingJSIdent returns the identifier an expression starts with:
// `create_lro_url` → itself, `last_mile_url.replace("/0/", x)` →
// `last_mile_url`, a template literal or string → "".
func rprLeadingJSIdent(expr string) string {
	expr = strings.TrimSpace(expr)
	i := 0
	for i < len(expr) {
		c := expr[i]
		if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return ""
	}
	return expr[:i]
}

// rprLastAssignmentBefore returns the verbatim source of the right-hand side
// of the last `name = <rhs>` / `const name = <rhs>` above line, or "".
func rprLastAssignmentBefore(root *sitter.Node, src []byte, name string, line int) string {
	best, bestRow := "", -1
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row < line {
			var lhs, rhs *sitter.Node
			switch n.Type() {
			case "variable_declarator":
				lhs, rhs = n.ChildByFieldName("name"), n.ChildByFieldName("value")
			case "assignment_expression":
				lhs, rhs = n.ChildByFieldName("left"), n.ChildByFieldName("right")
			}
			if lhs != nil && rhs != nil && lhs.Type() == "identifier" && lhs.Content(src) == name && row > bestRow {
				best, bestRow = strings.TrimSpace(rhs.Content(src)), row
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return best
}

func rprHasURLSuffix(id string) bool {
	return strings.HasSuffix(id, "_url") || strings.HasSuffix(id, "_path") || strings.HasSuffix(id, "_uri")
}

// rprWrapperMethod maps an api-wrapper name to its HTTP verb
// (`apiPost` → POST, `apiDelete` → DELETE, `get` → GET).
func rprWrapperMethod(w string) string {
	l := strings.ToLower(w)
	l = strings.TrimPrefix(l, "api")
	switch l {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return strings.ToUpper(l)
	case "del":
		return "DELETE"
	}
	return ""
}

// rprParseJS reads + parses one JS/TS/JSX/TSX file, reporting whether the
// tsx grammar (isJSX) was selected — the JSX query needs the matching
// compiled *sitter.Query for whichever grammar produced the tree.
func rprParseJS(file string) (src []byte, root *sitter.Node, isJSX bool, ok bool) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, false, false
	}
	lang := tssitter.GetLanguage()
	if ext := strings.ToLower(filepath.Ext(file)); ext == ".tsx" || ext == ".jsx" {
		lang = tsxsitter.GetLanguage()
		isJSX = true
	}
	root, err = sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil, nil, false, false
	}
	return src, root, isJSX, true
}
