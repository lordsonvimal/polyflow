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
	id      string
	service string
	line    int
	module  bool
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
			reps[n.File] = jsFileRep{id: n.ID, service: n.Service, line: n.Line, module: module}
		}
	}
	if len(reps) == 0 {
		return nil, nil
	}
	// Candidate order must not depend on map iteration: two files can match the
	// same `<script src>` equally well (the same vendored bundle checked into
	// two services), and picking between them by range order made the whole
	// index nondeterministic — successive runs emitted a different edge.
	repFiles := make([]string, 0, len(reps))
	for file := range reps {
		repFiles = append(repFiles, file)
	}
	sort.Strings(repFiles)

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
			targetID, conf := resolveAssetFile(src, n.Service, reps, repFiles)
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
//
// Resolution is confined to svc, the service that owns the templ component: a
// `<script src>` is served by its own service, so matching another service's
// file is never right. Shared vendored bundle names (datastar.min.js checked
// into two services) made that misfire routinely, and the winner depended on
// map order. repFiles must be the sorted key set of reps so that ties between
// two equally good in-service candidates resolve the same way on every run.
func resolveAssetFile(src, svc string, reps map[string]jsFileRep, repFiles []string) (id, confidence string) {
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
	for _, file := range repFiles {
		rep := reps[file]
		// An empty service on either side means the caller is not
		// service-scoped (unit fixtures, single-service workspaces) — fall
		// back to matching on path alone rather than dropping the edge.
		if svc != "" && rep.service != "" && rep.service != svc {
			continue
		}
		if file == norm || strings.HasSuffix(file, "/"+norm) {
			suffixID = rep.id
			break
		}
		if baseID == "" && path.Base(file) == base {
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

// maxClassFanout caps how many definition sites a single .class selector may
// fan out to before the edges are suppressed in favor of one ledger entry
// (DS.3's noise filter). Adding CSS-only producers to classDefs only grows an
// already-uncapped fan-out set; a utility-class-style name shared by dozens of
// elements carries no real signal about which one a given consumer means.
// Picked as a round number pending measurement against a utility-class-heavy
// corpus (juniper has no class used more than once) — revisit if a real
// corpus shows the cap firing too eagerly or not eagerly enough.
const maxClassFanout = 20

// maxFanoutTargetsListed caps how many definition sites formatFanoutTargets
// writes into a suppressed ref's Targets field, so a single ledger entry for
// an extreme case (hundreds of matches) doesn't itself become a token dump —
// an agent asking "what got dropped" needs a representative sample and a
// count, not the entire list.
const maxFanoutTargetsListed = 15

// formatFanoutTargets renders the definition sites a fan-out cap suppressed,
// as newline-separated "file:line" entries (truncated with a "+N more"
// marker) — so an agent can locate what was dropped and, if it matters for
// the question at hand, use search/read on it directly instead of the
// suppressed edges polyflow declined to spray.
func formatFanoutTargets(defs []elemDef) string {
	n := len(defs)
	if n > maxFanoutTargetsListed {
		n = maxFanoutTargetsListed
	}
	lines := make([]string, 0, n+1)
	for _, d := range defs[:n] {
		lines = append(lines, fmt.Sprintf("%s:%d", d.file, d.line))
	}
	if rest := len(defs) - n; rest > 0 {
		lines = append(lines, fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(lines, "\n")
}

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
		case n.Type == graph.NodeTypeElement && n.Meta["pattern"] == "stylesheet_selector":
			// DS.3: a top-level CSS/SCSS `.class`/`#id` rule (internal/css/scan.go)
			// is a real definition site for a JS selector consumer, exactly like a
			// templ/HTML class= or id= attribute — a class toggled purely by
			// addClass/classList with no literal class="…" anywhere in markup
			// otherwise resolves to nothing even though the stylesheet defines it.
			sel := n.Meta["selector"]
			name := strings.TrimPrefix(strings.TrimPrefix(sel, "."), "#")
			if name == "" {
				continue
			}
			key := n.Service + "\x00" + name
			def := elemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language}
			if n.Meta["selector_kind"] == "id" {
				idDefs[key] = append(idDefs[key], def)
			} else {
				classDefs[key] = append(classDefs[key], def)
			}
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
			// dom_classes meta carries "class@line\n…", one entry per
			// space-separated class= token (parser/templ.go). Same shape as
			// dom_ids, so a class selector consumer gets a real templ-side
			// index instead of the previous unconditional "unresolvable".
			for _, entry := range strings.Split(n.Meta["dom_classes"], "\n") {
				cls, line := splitIDLine(entry)
				if cls == "" {
					continue
				}
				key := n.Service + "\x00" + cls
				classDefs[key] = append(classDefs[key], elemDef{compID: n.ID, file: n.File, line: line, lang: "templ"})
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
	// marker distinguishes an id definition ("#") from a class definition
	// (".") in the minted node's ID/label — both share the same templ
	// component-backed minting path, an id/class pair on the same tag mints
	// two distinct element nodes rather than colliding on elemName alone.
	elemNodes := map[string]string{} // uniqueKey → element nodeID
	elemNodeFor := func(svc string, d elemDef, elemName string, marker string) (string, bool) {
		if d.nodeID != "" {
			return d.nodeID, false // already exists
		}
		// Mint a new element node from templ component data.
		ekey := d.compID + "\x00" + marker + elemName
		if id, ok := elemNodes[ekey]; ok {
			return id, false
		}
		metaKey := "dom_id"
		if marker == "." {
			metaKey = "dom_class"
		}
		elemID := fmt.Sprintf("%s:%s:%s:%s:%d", svc, d.file, string(graph.NodeTypeElement), marker+elemName, d.line)
		elemNodes[ekey] = elemID
		newNodes = append(newNodes, graph.Node{
			ID:       elemID,
			Type:     graph.NodeTypeElement,
			Label:    marker + elemName,
			Service:  svc,
			File:     d.file,
			Line:     d.line,
			EndLine:  d.line,
			Language: d.lang,
			Meta:     map[string]string{metaKey: elemName, "component": d.compID},
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

	// listenEdge closes the Tier K.4 chain. `defined_in` already points the
	// registration site at the elements its selector names; this points the
	// *element* at the handler that runs, which is the direction a trace has to
	// walk to answer "what happens when I click this". Only jQuery event nodes
	// carry handler_node, so an ordinary querySelector target adds nothing here.
	//
	// Confidence is inferred, never static: a selector fans out to every element
	// declaring that class across the whole service, and only the request that
	// rendered the page decides which ones were on it (rule #1 — fan out, do not
	// pick).
	listenEdge := func(target *graph.Node, elemID string) {
		handler := target.Meta["handler_node"]
		if handler == "" {
			return
		}
		edgeID := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeDOMListen), elemID, handler)
		if seenEdge[edgeID] {
			return
		}
		seenEdge[edgeID] = true
		meta := map[string]string{"event": target.Meta["event"], "via": "jquery"}
		if target.Meta["delegated"] == "true" {
			meta["delegated"] = "true"
			meta["delegate_root"] = target.Meta["delegate_root"]
		}
		edges = append(edges, graph.Edge{
			ID:         edgeID,
			From:       elemID,
			To:         handler,
			Type:       graph.EdgeTypeDOMListen,
			Confidence: graph.ConfidenceInferred,
			Meta:       meta,
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
			// A bare tag selector — $("body"), querySelector("div") — names an
			// element *type*. This index holds ids and classes only, so nothing
			// was attempted and nothing failed; ledgering it would fabricate a
			// clue (Tier K.4, same call as K.2's `render json:`).
			if sel := stripQuote(rawSel); rawSel != "" && !strings.ContainsAny(sel, "${}`+") &&
				strings.ContainsAny(sel, ".#[:") {
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
				elemID, _ := elemNodeFor(n.Service, d, id, "#")
				addEdge(n.ID, elemID, graph.ConfidenceStatic)
				listenEdge(n, elemID)
			}
			continue
		}

		if cls != "" {
			defs := classDefs[n.Service+"\x00"+cls]
			if len(defs) > maxClassFanout {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: "." + cls, Kind: "dom_class_high_fanout",
					Targets: formatFanoutTargets(defs),
				})
				continue
			}
			// No unresolved on class miss — classes may be defined externally in CSS.
			for _, d := range defs {
				elemID, _ := elemNodeFor(n.Service, d, cls, ".")
				addEdge(n.ID, elemID, graph.ConfidenceInferred)
				listenEdge(n, elemID)
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
