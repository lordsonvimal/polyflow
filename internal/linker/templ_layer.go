package linker

import (
	"fmt"
	"path"
	"regexp"
	"sort"
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

// elemDef records one definition site for a DOM element (by id or class).
// When nodeID is non-empty the element node already exists (from HTML/JSX
// parsing) and no new node needs to be minted. When nodeID is empty the
// element node must be minted from the templ component (compID, file, line).
type elemDef struct {
	nodeID string // existing element node ID (HTML/JSX source)
	compID string // templ component ID (used when minting new element nodes)
	file   string
	line   int
	lang   string
}

// LinkDOMDefinitions links JS DOM targets (querySelector/getElementById/jQuery
// selectors) to the element nodes that declare the matching id= or class=.
//
// Element definitions are collected from all indexed template sources:
//   - templ component nodes: dom_ids meta ("id@line\n…") as before
//   - HTML/JSX element nodes: NodeTypeElement nodes with meta["id"] or meta["class"]
//
// Simple selectors are resolved:
//
//	#id       → id index (static confidence; unresolved if missing)
//	.class    → class index, fan-out to ALL matching elements (inferred; no
//	            unresolved on miss — class may be styled externally)
//	tag.class → class index fan-out
//
// Complex selectors (descendant, pseudo, attribute, interpolation) →
// selector_dynamic ledger entry.
//
// Newly minted element nodes use NodeTypeElement ("element"); existing nodes
// (HTML/JSX source) are reused. NodeTypeTemplElement ("templ_element") is kept
// as a deprecated alias for stored graphs.
func LinkDOMDefinitions(nodes []graph.Node) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	// Build element-definition indexes.
	// idDefs:   "svc\x00id"    → definitions
	// classDefs: "svc\x00class" → definitions
	idDefs := map[string][]elemDef{}
	classDefs := map[string][]elemDef{}

	for i := range nodes {
		n := &nodes[i]
		switch {
		case n.Type == graph.NodeTypeComponent && n.Language == "templ":
			// Existing templ convention: dom_ids meta carries "id@line\n…".
			for _, entry := range strings.Split(n.Meta["dom_ids"], "\n") {
				id, line := splitIDLine(entry)
				if id == "" {
					continue
				}
				key := n.Service + "\x00" + id
				idDefs[key] = append(idDefs[key], elemDef{compID: n.ID, file: n.File, line: line, lang: "templ"})
			}
		case n.Type == graph.NodeTypeElement:
			// HTML/JSX element nodes emitted by the parser-level patterns.
			if id := n.Meta["id"]; id != "" {
				key := n.Service + "\x00" + id
				idDefs[key] = append(idDefs[key], elemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language})
			}
			if classes := n.Meta["class"]; classes != "" {
				for _, cls := range strings.Fields(classes) {
					key := n.Service + "\x00" + cls
					classDefs[key] = append(classDefs[key], elemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language})
				}
			}
		}
	}

	// Sort definitions within each bucket for deterministic emission (rule 2).
	sortDefs := func(defs []elemDef) {
		sort.Slice(defs, func(i, j int) bool {
			a, b := defs[i], defs[j]
			if a.file != b.file {
				return a.file < b.file
			}
			return a.line < b.line
		})
	}
	for k := range idDefs {
		sortDefs(idDefs[k])
	}
	for k := range classDefs {
		sortDefs(classDefs[k])
	}

	var newNodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	// elemNodeFor returns (or mints) the element node ID for a definition.
	elemNodes := map[string]string{} // uniqueKey → element nodeID
	elemNodeFor := func(svc string, d elemDef, elemName string) (string, bool) {
		if d.nodeID != "" {
			return d.nodeID, false // already exists
		}
		// Mint a new element node from templ component data.
		ekey := d.compID + "\x00" + elemName
		if id, ok := elemNodes[ekey]; ok {
			return id, false
		}
		elemID := fmt.Sprintf("%s:%s:%s:%s:%d", svc, d.file, string(graph.NodeTypeElement), "#"+elemName, d.line)
		elemNodes[ekey] = elemID
		newNodes = append(newNodes, graph.Node{
			ID:       elemID,
			Type:     graph.NodeTypeElement,
			Label:    "#" + elemName,
			Service:  svc,
			File:     d.file,
			Line:     d.line,
			Language: d.lang,
			Meta:     map[string]string{"dom_id": elemName, "component": d.compID},
		})
		return elemID, true
	}

	seenEdge := map[string]bool{}
	addEdge := func(fromID, toID string, conf string) {
		edgeID := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeDefinedIn), fromID, toID)
		if seenEdge[edgeID] {
			return
		}
		seenEdge[edgeID] = true
		edges = append(edges, graph.Edge{
			ID:         edgeID,
			From:       fromID,
			To:         toID,
			Type:       graph.EdgeTypeDefinedIn,
			Confidence: conf,
		})
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget {
			continue
		}

		rawSel := n.Meta["selector"]
		fn := n.Meta["fn"]
		id, cls, isComplex := parseDOMSelector(fn, rawSel)

		if isComplex {
			if rawSel != "" && !strings.ContainsAny(stripQuote(rawSel), "${}`+") {
				// Simple-enough to recognize as complex CSS — surface in ledger.
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: stripQuote(rawSel), Kind: "selector_dynamic",
				})
			}
			continue
		}

		if id != "" {
			defs, ok := idDefs[n.Service+"\x00"+id]
			if !ok {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: "#" + id, Kind: "dom_ref",
				})
				continue
			}
			for _, d := range defs {
				elemID, _ := elemNodeFor(n.Service, d, id)
				addEdge(n.ID, elemID, graph.ConfidenceStatic)
			}
			continue
		}

		if cls != "" {
			defs := classDefs[n.Service+"\x00"+cls]
			// No unresolved on class miss — classes may be defined externally in CSS.
			for _, d := range defs {
				var elemID string
				if d.nodeID != "" {
					elemID = d.nodeID
				} else {
					// Templ components don't track dom_classes in this phase;
					// treat as unresolvable without minting a ghost node.
					continue
				}
				addEdge(n.ID, elemID, graph.ConfidenceInferred)
			}
		}
	}
	return newNodes, edges, unresolved
}

// parseDOMSelector extracts a simple #id or .class (or tag.class) target from a
// raw selector string. Returns (id, class, isComplex) where exactly one of id/class
// is non-empty when isComplex=false.
//
// Handles:
//   - getElementById(bare_id) → id
//   - querySelector/querySelectorAll("#id") → id
//   - querySelector/querySelectorAll(".class") → class
//   - jQuery $(...) and delegation selectors
//   - tag.class form → class (the class part)
func parseDOMSelector(fn, rawSelector string) (id, class string, isComplex bool) {
	sel := stripQuote(strings.TrimSpace(rawSelector))
	if sel == "" {
		return "", "", false
	}
	// Dynamic interpolation → complex.
	if strings.ContainsAny(sel, " ${}`+") {
		return "", "", true
	}

	if fn == "getElementById" {
		if reSimpleID.MatchString(sel) {
			return sel, "", false
		}
		return "", "", true
	}

	// querySelector, querySelectorAll, jQuery $(…), delegation selector, etc.
	if strings.HasPrefix(sel, "#") {
		id = sel[1:]
		if reSimpleID.MatchString(id) {
			return id, "", false
		}
		return "", "", true
	}
	if strings.HasPrefix(sel, ".") {
		cls := sel[1:]
		if reSimpleID.MatchString(cls) {
			return "", cls, false
		}
		return "", "", true
	}
	// tag.class form (e.g. "button.save-btn").
	if dot := strings.LastIndex(sel, "."); dot > 0 {
		cls := sel[dot+1:]
		if reSimpleID.MatchString(cls) && !strings.ContainsAny(sel[:dot], ".#:[") {
			return "", cls, false
		}
	}
	return "", "", true
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
