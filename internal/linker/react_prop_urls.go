package linker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// LinkReactPropURLs resolves the URL of a React API call whose endpoint arrives
// as a component prop set on the Rails side.
//
// orion's FFU flow is the motivating shape: `ffu.html.erb` mounts
//
//	react_component("UppyUploader", {
//	  create_lro_url: "/client_api/v1/lros",
//	  sign_part_url:  sign_part_folder_fast_uploads_url(@folder),
//	  ...
//	})
//
// and inside UppyUploader.jsx `apiPost(create_lro_url, …)` /
// `apiPost(sign_part_url.replace("/0/", …), …)` call the endpoint. LinkJSAPIWrapperCalls
// already mints an http_client node per such call site, but its URL is the bare
// prop identifier — `key_dynamic`, no route, no edge.
//
// This pass:
//  1. indexes every Rails route by its `route_helper` name;
//  2. scans every ERB `react_component` props hash, resolving each prop value
//     that is a literal path or a `*_url`/`*_path` route-helper call to that
//     route's path (`:seg` → `*`);
//  3. for each `js_api_wrapper_call_site` http_client node still `key_dynamic`
//     whose URL expression's leading identifier is a resolved prop of a
//     component implemented in that file, rewrites the node: sets `url`, clears
//     `key_dynamic`, so the contract engine joins it to the route.
//
// A prop that resolves to two different paths across render sites, or a
// component whose file implements two components disagreeing on the prop, is
// left `key_dynamic` (abstain — the same rule as every other resolver here).
// Runs after rails_views + js_api_wrapper_calls, before the contract engine.
func LinkReactPropURLs(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	type routeInfo struct{ url, method string }

	// 1. route_helper → routes. App-global: a component's Rails side and the API
	// it calls are the same application even when split into services.
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
		helpers[h] = append(helpers[h], routeInfo{url: colonToWildcard(p), method: strings.ToUpper(n.Meta["method"])})
	}
	if len(helpers) == 0 {
		return nil
	}

	resolvePropURL := func(val string) (string, bool) {
		val = strings.TrimSpace(val)
		if lit, _, ok := railsview.LeadingLiteral(val); ok {
			if strings.HasPrefix(lit, "/") {
				return colonToWildcard(lit), true
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
	// 2b. JSX → JSX prop forwarding. Not every URL prop originates on the Rails
	// side: `<JobDetailModal url={`/app/lro/${lroId}?study_id=${sid}`} />` in one
	// component passes a literal/template path straight to another. The child's
	// `apiGet(url)` call site is still minted `key_dynamic`.
	scanJSXPropURLs(serviceFiles, record)

	erbSeen := map[string]bool{}
	for _, files := range serviceFiles {
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
	}
	if len(propURL) == 0 {
		return nil
	}

	// 3. component name → implementation files (cross-service; the JSX is often
	// its own service). Reuses newComponentIndex's window-registry resolution.
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
		ci := newComponentIndex(nodes, svc)
		for sym, ids := range ci.bySymbol {
			for _, id := range ids {
				addComp(nodeFile[id], sym)
			}
		}
	}
	// A JSX component that only forwards a prop to another (JobDetailModal) is
	// often never registered on `window` — newComponentIndex can't see it. Fall
	// back to its function/class declaration: a Capitalized top-level name in
	// the file it's defined in. record()'s conflict guard covers name clashes.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeClass {
			continue
		}
		if l := n.Label; l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			addComp(n.File, l)
		}
	}

	// 4. rewrite.
	localSrc := map[string][]byte{}
	localRoot := map[string]*sitter.Node{}
	fileTree := func(file string) (*sitter.Node, []byte) {
		root, ok := localRoot[file]
		if !ok {
			src, r, _, parsed := jsParse(file)
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
		return jsLastAssignmentBefore(root, src, name, line)
	}
	// isPropParam reports whether name is bound by a destructuring pattern in
	// file — a component prop parameter (`({ url }) => …`) or a `const { url } =
	// props`. Distinguishes a genuine prop reference from a same-named local.
	isPropParam := func(file, name string) bool {
		root, src := fileTree(file)
		if root == nil {
			return false
		}
		return jsDestructuresName(root, src, name)
	}

	var changed []graph.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient || n.Meta["pattern"] != "js_api_wrapper_call_site" || n.Meta["key_dynamic"] != "true" {
			continue
		}
		prop := leadingJSIdent(firstNonEmpty(n.Meta["url_expr"], n.Meta["key_dynamic_raw"]))
		// Only a `*_url`/`*_path`/`*_uri` identifier is taken as the prop itself.
		// A bare `url` at the call site is far more often a local variable
		// (`const url = some_url_prop.replace(...)`) shadowing the prop, so follow
		// one hop of local assignment: `const url = add_lro_details_url.replace(…)`
		// → `add_lro_details_url`. Anything past one hop, or a non-prop source,
		// still abstains.
		if !hasURLSuffix(prop) {
			if src := leadingJSIdent(localSource(n.File, prop, n.Line)); hasURLSuffix(src) {
				prop = src
			} else if !isPropParam(n.File, prop) {
				// Not a `*_url` prop, no one-hop local assignment from one, and
				// not a destructured prop parameter — abstain (local-var risk).
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
		method := jsWrapperMethod(n.Meta["wrapper"])
		n.Meta["url"] = resolved
		n.Meta["path_resolved_via"] = "react_prop_url"
		delete(n.Meta, "key_dynamic")
		delete(n.Meta, "key_dynamic_raw")
		if n.Meta["method"] == "" && method != "" {
			n.Meta["method"] = method
		}
		if n.Meta["owner_service"] == "" {
			n.Meta["owner_service"] = n.Service
		}
		if n.Label == "" || n.Label == "dynamic" {
			n.Label = strings.TrimSpace(method + " " + resolved)
		}
		changed = append(changed, *n)
	}
	return changed
}

// jsxPropURLQuery captures `<Component prop={"..."|`...`} />` and
// `<Component prop="..." />` — a string or template literal passed as a JSX
// attribute value.
const jsxPropURLQuery = `
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

// scanJSXPropURLs walks every .jsx/.tsx file for JSX elements that pass a
// literal or template-literal path as a prop (`<Modal url={`/app/lro/${id}`} />`)
// and feeds each (component, prop, resolved-path) to record.
func scanJSXPropURLs(serviceFiles map[string][]string, record func(comp, prop, url string)) {
	seen := map[string]bool{}
	for _, files := range serviceFiles {
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
			src, root, lang, ok := jsParse(f)
			if !ok {
				continue
			}
			q, err := compiledQuery(jsxPropURLQuery, lang)
			if err != nil {
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
				if u, ok := resolveJSPropURL(val); ok {
					record(tag, prop, u)
				}
			}
		}
	}
}

var reTemplateSubst = regexp.MustCompile(`\$\{[^{}]*\}`)

// resolveJSPropURL turns a JS string / template-literal source into a
// root-relative wildcard path: `"/app/lro/5"` → `/app/lro/5`,
// `` `/app/lro/${id}?study_id=${s}` `` → `/app/lro/*`. Anything not starting
// with a literal `/` (a bare identifier, an interpolation-led template) fails.
func resolveJSPropURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != raw[len(raw)-1] {
		return "", false
	}
	var body string
	switch raw[0] {
	case '"', '\'':
		body = raw[1 : len(raw)-1]
	case '`':
		body = reTemplateSubst.ReplaceAllString(raw[1:len(raw)-1], "*")
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

// jsDestructuresName reports whether name appears as a key in any object
// destructuring pattern in the tree (`({ name }) => …`, `const { name } = x`).
func jsDestructuresName(root *sitter.Node, src []byte, name string) bool {
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

// colonToWildcard turns a Rails route path template into the client-side
// wildcard form the js http_client nodes use: `/x/:id/y` → `/x/*/y`,
// `/files/*path` → `/files/*`.
func colonToWildcard(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, "/")
}

// leadingJSIdent returns the identifier an expression starts with:
// `create_lro_url` → itself, `last_mile_url.replace("/0/", x)` → `last_mile_url`,
// a template literal or string → "".
func leadingJSIdent(expr string) string {
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

// jsLastAssignmentBefore returns the verbatim source of the right-hand side of
// the last `name = <rhs>` / `const name = <rhs>` above line, or "".
func jsLastAssignmentBefore(root *sitter.Node, src []byte, name string, line int) string {
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

func hasURLSuffix(id string) bool {
	return strings.HasSuffix(id, "_url") || strings.HasSuffix(id, "_path") || strings.HasSuffix(id, "_uri")
}

// jsWrapperMethod maps an api-wrapper name to its HTTP verb
// (`apiPost` → POST, `apiDelete` → DELETE, `get` → GET).
func jsWrapperMethod(w string) string {
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
