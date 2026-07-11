package linker

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// isVendorPath reports whether a path is a build output or vendored dependency,
// so a hashed dist copy or a node_modules bundle never wins over the source
// file a templ `<script src>` actually references.
func isVendorPath(file string) bool {
	return strings.Contains(file, "node_modules/") ||
		strings.HasPrefix(file, "dist/") || strings.Contains(file, "/dist/")
}

// jsFileRep picks a representative node ID for each JS source file: the
// synthetic module node when present, otherwise the lowest-line node in the
// file. Cross-file `imports` edges target this representative.
type jsFileRep struct {
	id     string
	line   int
	module bool
}

// LinkTemplScripts draws `imports` edges from a templ component to the JS file
// its `<script src>` loads. The templ parser stashes each resolved asset path
// on the component's `script_srcs` meta (newline-separated); this pass matches
// that logical path to an indexed JS source file and emits the edge. Assets that
// match no indexed file surface as UnresolvedRefs rather than being dropped.
func LinkTemplScripts(nodes []graph.Node) ([]graph.Edge, []graph.UnresolvedRef) {
	reps := map[string]jsFileRep{} // file -> representative node
	for i := range nodes {
		n := &nodes[i]
		if !isJSFile(n.File) || isVendorPath(n.File) {
			continue
		}
		module := n.Meta["scope"] == "module"
		cur, ok := reps[n.File]
		if !ok || (module && !cur.module) || (module == cur.module && n.Line < cur.line) {
			reps[n.File] = jsFileRep{id: n.ID, line: n.Line, module: module}
		}
	}
	if len(reps) == 0 {
		return nil, nil
	}

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	seen := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeComponent || n.Language != "templ" {
			continue
		}
		srcs := n.Meta["script_srcs"]
		if srcs == "" {
			continue
		}
		for _, src := range strings.Split(srcs, "\n") {
			targetID, conf := resolveAssetFile(src, reps)
			if targetID == "" {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: src, Kind: "import_ref",
				})
				continue
			}
			edgeID := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeImports), n.ID, targetID)
			if seen[edgeID] {
				continue
			}
			seen[edgeID] = true
			edges = append(edges, graph.Edge{
				ID:         edgeID,
				From:       n.ID,
				To:         targetID,
				Type:       graph.EdgeTypeImports,
				Confidence: conf,
				Meta:       map[string]string{"via": "script_src", "asset": src},
			})
		}
	}
	return edges, unresolved
}

// resolveAssetFile matches a logical asset path (`js/board.js`, possibly served
// as `/static/js/board.js`) to an indexed JS file's representative node. A path
// suffix match is confident (static); a basename-only fallback is partial —
// build tooling can remap the directory (`js/datastar.js` → `assets/datastar.js`).
func resolveAssetFile(src string, reps map[string]jsFileRep) (id, confidence string) {
	norm := src
	if i := strings.IndexByte(norm, '?'); i >= 0 {
		norm = norm[:i]
	}
	norm = strings.TrimPrefix(norm, "/")
	norm = strings.TrimPrefix(norm, "static/")
	if norm == "" {
		return "", ""
	}
	base := path.Base(norm)

	var suffixID, baseID string
	for file, rep := range reps {
		if file == norm || strings.HasSuffix(file, "/"+norm) {
			suffixID = rep.id
			break
		}
		if path.Base(file) == base {
			baseID = rep.id // keep looking for a stronger suffix match
		}
	}
	if suffixID != "" {
		return suffixID, graph.ConfidenceStatic
	}
	if baseID != "" {
		return baseID, graph.ConfidencePartial
	}
	return "", ""
}

var reSimpleID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// LinkDOMDefinitions links a JS DOM target (querySelector/getElementById) to the
// templ element that declares the matching `id=`. The templ parser records each
// element id on its component's `dom_ids` meta ("id@line", newline-separated);
// this pass creates a templ_element node for every id a JS selector actually
// references and emits a `defined_in` edge from the JS target to it. Only id
// selectors are resolved here — class selectors (which match many elements) and
// attribute/tag/dynamic selectors are left for a later phase. A selector id with
// no templ definition surfaces as an UnresolvedRef.
func LinkDOMDefinitions(nodes []graph.Node) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	type idDef struct {
		compID, file string
		line         int
	}
	idDefs := map[string][]idDef{} // service\x00id -> definitions
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeComponent || n.Language != "templ" {
			continue
		}
		raw := n.Meta["dom_ids"]
		if raw == "" {
			continue
		}
		for _, entry := range strings.Split(raw, "\n") {
			id, line := splitIDLine(entry)
			if id == "" {
				continue
			}
			key := n.Service + "\x00" + id
			idDefs[key] = append(idDefs[key], idDef{n.ID, n.File, line})
		}
	}
	if len(idDefs) == 0 {
		return nil, nil, nil
	}

	var newNodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	elemNodes := map[string]string{} // compID\x00id -> element nodeID
	seenEdge := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget {
			continue
		}
		id, ok := domTargetID(n.Meta["fn"], n.Meta["selector"])
		if !ok {
			continue
		}
		defs, ok := idDefs[n.Service+"\x00"+id]
		if !ok {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: "#" + id, Kind: "dom_ref",
			})
			continue
		}
		for _, d := range defs {
			ekey := d.compID + "\x00" + id
			elemID, exists := elemNodes[ekey]
			if !exists {
				elemID = fmt.Sprintf("%s:%s:%s:%s:%d", n.Service, d.file, string(graph.NodeTypeTemplElement), id, d.line)
				elemNodes[ekey] = elemID
				newNodes = append(newNodes, graph.Node{
					ID:       elemID,
					Type:     graph.NodeTypeTemplElement,
					Label:    "#" + id,
					Service:  n.Service,
					File:     d.file,
					Line:     d.line,
					Language: "templ",
					Meta:     map[string]string{"dom_id": id, "component": d.compID},
				})
			}
			edgeID := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeDefinedIn), n.ID, elemID)
			if seenEdge[edgeID] {
				continue
			}
			seenEdge[edgeID] = true
			edges = append(edges, graph.Edge{
				ID:         edgeID,
				From:       n.ID,
				To:         elemID,
				Type:       graph.EdgeTypeDefinedIn,
				Confidence: graph.ConfidenceStatic,
			})
		}
	}
	return newNodes, edges, unresolved
}

// splitIDLine splits a "id@line" dom_ids entry into its id and line number.
func splitIDLine(entry string) (string, int) {
	i := strings.LastIndexByte(entry, '@')
	if i < 0 {
		return entry, 0
	}
	line := 0
	fmt.Sscanf(entry[i+1:], "%d", &line)
	return entry[:i], line
}

// domTargetID extracts the element id a DOM query targets, or ok=false when the
// selector is not a plain id (class/attribute/tag/compound/dynamic). getElementById
// takes a bare id; querySelector(All) takes a CSS selector, so an id there is
// `#foo`.
func domTargetID(fn, rawSelector string) (string, bool) {
	sel := stripQuote(strings.TrimSpace(rawSelector))
	if sel == "" || strings.ContainsAny(sel, "${}`+") {
		return "", false // dynamic / interpolated selector
	}
	switch fn {
	case "getElementById":
		if reSimpleID.MatchString(sel) {
			return sel, true
		}
	case "querySelector", "querySelectorAll":
		if strings.HasPrefix(sel, "#") {
			id := sel[1:]
			if reSimpleID.MatchString(id) {
				return id, true
			}
		}
	}
	return "", false
}

// stripQuote removes a single matching pair of surrounding quotes (single,
// double, or backtick) from a captured selector literal.
func stripQuote(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
