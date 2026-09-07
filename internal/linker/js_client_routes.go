package linker

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkJSClientRoutes (SPA.2) models a single-page-app's client-side router as
// graph nodes and edges:
//
//   - A module-level route table — `export default { cdm: "/standards/:id#cdm/:m", … }`
//     (an object literal whose values are all URL-pattern strings) — mints one
//     `client_route` node per entry, labelled by the route name, with
//     Meta["path"] holding the normalised *pre-hash* path (`:seg` → `*`) and
//     Meta["hash"] recording whether the pattern carried a `#fragment`.
//   - A `switch (routeName)` / `switch (activeTabKey)` elsewhere in the service
//     maps each `case "<name>":` to the JSX page component rendered in that
//     block. Emits `client_route --renders--> <componentNode>`.
//   - Where a `client_route`'s normalised path equals an `http_handler`'s
//     normalised path, emits `http_handler --navigates_to--> client_route` so
//     "open `GET /standards/:id` → which SPA page" is answerable.
//
// No name allow-list for the route table or the switch discriminant beyond the
// two conventional discriminant identifiers; the table is recognised purely by
// shape. Runs after js_link so the render-target component nodes it resolves
// against are already stamped (SPA.1 synthetics included).
func LinkJSClientRoutes(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge) {
	// --- index existing nodes ---
	svcOfFile := make(map[string]string)
	comp := make(map[string]string) // service\x00label → component node id
	compRank := make(map[string]int)
	handlersByPath := make(map[string][]crHandler) // normalised path → handlers
	consider := func(svc, label, id string, rank int) {
		if label == "" || svc == "" {
			return
		}
		key := svc + "\x00" + label
		if cur, ok := compRank[key]; !ok || rank > cur {
			comp[key] = id
			compRank[key] = rank
		}
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Service != "" && n.File != "" {
			svcOfFile[n.File] = n.Service
		}
		switch n.Type {
		case graph.NodeTypeComponent:
			consider(n.Service, n.Label, n.ID, 3)
		case graph.NodeTypeVariable:
			if n.Meta["component"] == "true" {
				consider(n.Service, n.Label, n.ID, 2)
			}
		case graph.NodeTypeClass:
			if startsUpperASCII(n.Label) {
				consider(n.Service, n.Label, n.ID, 2)
			}
		case graph.NodeTypeFunction:
			if startsUpperASCII(n.Label) {
				consider(n.Service, n.Label, n.ID, 1)
			}
		case graph.NodeTypeHTTPHandler:
			p := n.Meta["path"]
			if p == "" {
				break
			}
			np := normRoutePath(p)
			if np == "/" || np == "" {
				break
			}
			handlersByPath[np] = append(handlersByPath[np], crHandler{
				id:     n.ID,
				method: strings.ToUpper(n.Meta["method"]),
				svc:    n.Service,
			})
		}
	}

	type crEntry struct {
		name, pattern, rel string
	}
	routesBySvc := make(map[string][]crEntry)
	seenRoute := make(map[string]bool)
	caseBySvc := make(map[string]map[string][]string) // svc → routeName → component tags

	seen := make(map[string]bool)
	for svcKey, files := range serviceFiles {
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
			svc := svcKey
			if svc == "" {
				svc = svcOfFile[rel]
			}

			for _, obj := range exportedObjectLiterals(root) {
				for _, e := range routeTableEntries(obj, src) {
					key := svc + "\x00" + rel + "\x00" + e.name
					if seenRoute[key] {
						continue
					}
					seenRoute[key] = true
					routesBySvc[svc] = append(routesBySvc[svc], crEntry{e.name, e.pattern, rel})
				}
			}

			for name, tags := range routeCaseComponents(root, src) {
				m := caseBySvc[svc]
				if m == nil {
					m = make(map[string][]string)
					caseBySvc[svc] = m
				}
				m[name] = append(m[name], tags...)
			}
		}
	}

	seenEdge := make(map[string]bool)
	addEdge := func(e graph.Edge) {
		if seenEdge[e.ID] {
			return
		}
		seenEdge[e.ID] = true
		edges = append(edges, e)
	}

	for svc, entries := range routesBySvc {
		for _, e := range entries {
			path, hash, frag := normClientRoute(e.pattern)
			routeID := svc + ":" + e.rel + ":client_route:" + e.name
			meta := map[string]string{
				"path":    path,
				"hash":    boolStr(hash),
				"pattern": e.pattern,
			}
			if frag != "" {
				meta["hash_fragment"] = frag
			}
			newNodes = append(newNodes, graph.Node{
				ID:       routeID,
				Type:     graph.NodeTypeClientRoute,
				Label:    e.name,
				Service:  svc,
				File:     e.rel,
				Language: "javascript",
				Meta:     meta,
			})

			for _, tag := range dedupStrings(caseBySvc[svc][e.name]) {
				cid := comp[svc+"\x00"+tag]
				if cid == "" || cid == routeID {
					continue
				}
				addEdge(graph.Edge{
					ID:         "renders:" + routeID + "->" + cid,
					From:       routeID,
					To:         cid,
					Type:       graph.EdgeTypeRenders,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "route_component"},
				})
			}

			hs := handlersByPath[path]
			if path == "/" || path == "" || len(hs) > 6 {
				continue
			}
			for _, h := range hs {
				if h.method != "" && h.method != "GET" {
					continue
				}
				if h.svc != "" && svc != "" && h.svc != svc {
					continue
				}
				addEdge(graph.Edge{
					ID:         "navigates_to:" + h.id + "->" + routeID,
					From:       h.id,
					To:         routeID,
					Type:       graph.EdgeTypeNavigatesTo,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "client_route", "via": "path_shape"},
				})
			}
		}
	}
	return newNodes, edges
}

type crHandler struct{ id, method, svc string }

// exportedObjectLiterals returns object literals that are the value of an
// `export default …` or an exported `const … = { … }`.
func exportedObjectLiterals(root *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "export_statement" {
			if v := n.ChildByFieldName("value"); v != nil {
				if o := unwrapToObject(v); o != nil {
					out = append(out, o)
				}
			}
			if d := n.ChildByFieldName("declaration"); d != nil {
				for i := 0; i < int(d.NamedChildCount()); i++ {
					vd := d.NamedChild(i)
					if vd.Type() != "variable_declarator" {
						continue
					}
					if o := unwrapToObject(vd.ChildByFieldName("value")); o != nil {
						out = append(out, o)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

func unwrapToObject(n *sitter.Node) *sitter.Node {
	for n != nil {
		switch n.Type() {
		case "object":
			return n
		case "parenthesized_expression":
			n = n.NamedChild(0)
		default:
			return nil
		}
	}
	return nil
}

type crPair struct{ name, pattern string }

// routeTableEntries validates that obj is a client-route table — at least three
// pairs, every value a string literal, at least one value carrying a `:param` —
// and returns the entries whose value looks like a URL pattern.
func routeTableEntries(obj *sitter.Node, src []byte) []crPair {
	if obj == nil || obj.Type() != "object" {
		return nil
	}
	var pairs []crPair
	total, strVals := 0, 0
	hasColon := false
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		p := obj.NamedChild(i)
		if p.Type() != "pair" {
			continue
		}
		total++
		k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
		name := crKeyName(k, src)
		val, ok := crStringLit(v, src)
		if name == "" || !ok {
			continue
		}
		strVals++
		if strings.Contains(val, ":") {
			hasColon = true
		}
		if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "#") {
			pairs = append(pairs, crPair{name, val})
		}
	}
	if len(pairs) < 3 || total == 0 || strVals != total || !hasColon {
		return nil
	}
	return pairs
}

func crKeyName(k *sitter.Node, src []byte) string {
	if k == nil {
		return ""
	}
	switch k.Type() {
	case "property_identifier", "identifier":
		return k.Content(src)
	case "string":
		s, _ := crStringLit(k, src)
		return s
	}
	return ""
}

func crStringLit(n *sitter.Node, src []byte) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Type() {
	case "string":
		t := n.Content(src)
		if len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	case "template_string":
		t := n.Content(src)
		if strings.Contains(t, "${") || len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	}
	return "", false
}

// routeCaseComponents maps `case "<name>":` labels to the PascalCase JSX tags
// rendered in that block, for every `switch (routeName)` / `switch (activeTabKey)`
// in the file. Fall-through cases (a `case` with no JSX before the next) inherit
// the following block's components.
func routeCaseComponents(root *sitter.Node, src []byte) map[string][]string {
	res := make(map[string][]string)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "switch_statement" && isRouteSwitchDiscriminant(n, src) {
			body := n.ChildByFieldName("body")
			if body == nil {
				for i := 0; i < int(n.NamedChildCount()); i++ {
					if c := n.NamedChild(i); c.Type() == "switch_body" {
						body = c
						break
					}
				}
			}
			if body != nil {
				var pending []string
				for i := 0; i < int(body.NamedChildCount()); i++ {
					c := body.NamedChild(i)
					if c.Type() != "switch_case" {
						continue
					}
					lbl, ok := crStringLit(c.ChildByFieldName("value"), src)
					if !ok {
						continue
					}
					tags := jsxTagsIn(c, src)
					if len(tags) == 0 {
						pending = append(pending, lbl)
						continue
					}
					for _, nm := range append(pending, lbl) {
						res[nm] = append(res[nm], tags...)
					}
					pending = pending[:0]
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return res
}

func isRouteSwitchDiscriminant(sw *sitter.Node, src []byte) bool {
	v := sw.ChildByFieldName("value")
	if v != nil && v.Type() == "parenthesized_expression" && v.NamedChildCount() > 0 {
		v = v.NamedChild(0)
	}
	if v == nil {
		return false
	}
	t := strings.TrimSpace(strings.Trim(v.Content(src), "()"))
	return t == "routeName" || t == "activeTabKey"
}

func jsxTagsIn(n *sitter.Node, src []byte) []string {
	var out []string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_self_closing_element", "jsx_opening_element":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				if tag := nm.Content(src); startsUpperASCII(tag) {
					out = append(out, tag)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(n)
	return out
}

// normClientRoute splits a url-pattern route into its normalised pre-hash path
// (`:seg` → `*`, optional `(…)` groups and query dropped), a hash flag, and the
// raw hash fragment name (up to its first optional group).
func normClientRoute(pattern string) (path string, hash bool, frag string) {
	raw := pattern
	if strings.HasPrefix(raw, "#") {
		// Leading-hash (hashbang) router: the fragment *is* the route path.
		return normRoutePath(strings.TrimPrefix(raw, "#")), true, ""
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		hash = true
		frag = raw[i+1:]
		raw = raw[:i]
	}
	path = normRoutePath(raw)
	if j := strings.IndexAny(frag, "(?/"); j >= 0 {
		frag = frag[:j]
	}
	return path, hash, frag
}

func normRoutePath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "?#("); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	out := segs[:0]
	for _, s := range segs {
		if s == "" {
			continue
		}
		if s[0] == ':' || s[0] == '*' || strings.HasPrefix(s, "{") {
			out = append(out, "*")
		} else {
			out = append(out, s)
		}
	}
	return "/" + strings.Join(out, "/")
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func dedupStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
