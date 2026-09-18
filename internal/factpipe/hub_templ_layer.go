package factpipe

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_templ_layer.go is the "templ_layer" hub provider (see hub.go) — the
// Tier FX FX.8.27 migration of internal/linker/templ_layer.go's retired
// LinkTemplScripts + LinkDOMDefinitions (Tier K.4/DS.3).
//
// The roster's original caveat called this "actually node-minting" and
// disqualified it outright — that framing predates HubProvider (FX.8.44).
// Both passes operate purely over `nodes []graph.Node`, no file I/O at
// all (script/DOM-selector matching, id/class indexing, fan-out capping
// are all pure graph-derived logic), so this is an even simpler hub than
// gorm_tables/stylesheet_imports: real Go over the graph-so-far, ported
// near-verbatim (`tl`-prefixed), no new resolution capability needed.
//
// `mint:` (frozenNodeTypes["element"], new this migration) builds the DOM
// element node a templ component's dom_ids/dom_classes meta implies when
// no HTML/JSX/stylesheet-sourced element node already claims that id/class
// — every row still carries a full mint spec even when the target already
// exists (an already-existing id/class definition resolves to that node's
// own real ID), relying on the established "mint is gap-fill only" caller
// discipline to skip it, the same shape every FX.8 mint consumer already
// uses. The dom_class_high_fanout ledger row is the reason
// `unresolvedRefSpec` grew a `targets:` field — graph.UnresolvedRef's own
// Targets carries the suppressed-match sample, unused by every
// `unresolved:` block before this one.
func init() { RegisterHub("templ_layer", templLayerHub) }

const (
	tlScriptEdgePred        = "tl_script_edge"                 // (From, To, Asset, Conf)
	tlScriptUnresolvedPred  = "tl_script_unresolved"           // (Svc, File, Line, Asset)
	tlIDDefPred             = "tl_id_def"                      // (FromID, ElemID, Label, Svc, File, Line, Lang, Name, CompID)
	tlClassDefPred          = "tl_class_def"                   // (FromID, ElemID, Label, Svc, File, Line, Lang, Name, CompID)
	tlListenEdgePred        = "tl_listen_edge"                 // (ElemID, Handler, Event, Delegated, DelegateRoot)
	tlDOMRefUnresolvedPred  = "tl_dom_ref_unresolved"          // (Svc, File, Line, Name)
	tlSelectorDynUnresolved = "tl_selector_dynamic_unresolved" // (Svc, File, Line, Name)
	tlClassFanoutUnresolved = "tl_dom_class_fanout_unresolved" // (Svc, File, Line, Name, Targets)
)

func templLayerHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, _ graph.SchemaConfig, _ []graph.UnresolvedRef) []Fact {
	var out []Fact
	out = append(out, tlScriptFacts(nodes)...)
	out = append(out, tlDOMDefinitionFacts(nodes)...)
	return out
}

// ── LinkTemplScripts port ───────────────────────────────────────────────

func tlIsJSFile(file string) bool {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".js", ".jsx", ".mjs", ".es6", ".ts", ".tsx":
		return true
	}
	return false
}

func tlIsVendorPath(file string) bool {
	return strings.Contains(file, "node_modules/") ||
		strings.HasPrefix(file, "dist/") || strings.Contains(file, "/dist/")
}

type tlJSFileRep struct {
	id      string
	service string
	line    int
	module  bool
}

func tlScriptFacts(nodes []graph.Node) []Fact {
	reps := map[string]tlJSFileRep{}
	for i := range nodes {
		n := &nodes[i]
		if !tlIsJSFile(n.File) || tlIsVendorPath(n.File) {
			continue
		}
		module := n.Meta["scope"] == "module"
		cur, ok := reps[n.File]
		if !ok || (module && !cur.module) || (module == cur.module && n.Line < cur.line) {
			reps[n.File] = tlJSFileRep{id: n.ID, service: n.Service, line: n.Line, module: module}
		}
	}
	if len(reps) == 0 {
		return nil
	}
	repFiles := make([]string, 0, len(reps))
	for file := range reps {
		repFiles = append(repFiles, file)
	}
	sort.Strings(repFiles)

	var out []Fact
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
			targetID, conf := tlResolveAssetFile(src, n.Service, reps, repFiles)
			origin := Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: tlScriptEdgePred}
			if targetID == "" {
				out = append(out, Fact{
					Pred:   tlScriptUnresolvedPred,
					Args:   []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(src)},
					Origin: origin,
				})
				continue
			}
			key := n.ID + "->" + targetID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Fact{
				Pred:   tlScriptEdgePred,
				Args:   []Atom{Node(n.ID), Node(targetID), Str(src), Str(conf)},
				Origin: origin,
			})
		}
	}
	return out
}

func tlResolveAssetFile(src, svc string, reps map[string]tlJSFileRep, repFiles []string) (id, confidence string) {
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
		if svc != "" && rep.service != "" && rep.service != svc {
			continue
		}
		if file == norm || strings.HasSuffix(file, "/"+norm) {
			suffixID = rep.id
			break
		}
		if baseID == "" && path.Base(file) == base {
			baseID = rep.id
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

// ── LinkDOMDefinitions port ──────────────────────────────────────────────

var tlReSimpleID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const tlMaxClassFanout = 20
const tlMaxFanoutTargetsListed = 15

type tlElemDef struct {
	nodeID string
	compID string
	file   string
	line   int
	lang   string
}

func tlFormatFanoutTargets(defs []tlElemDef) string {
	n := len(defs)
	if n > tlMaxFanoutTargetsListed {
		n = tlMaxFanoutTargetsListed
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

func tlDOMDefinitionFacts(nodes []graph.Node) []Fact {
	idDefs := map[string][]tlElemDef{}
	classDefs := map[string][]tlElemDef{}

	for i := range nodes {
		n := &nodes[i]
		switch {
		case n.Type == graph.NodeTypeElement && n.Meta["pattern"] == "stylesheet_selector":
			sel := n.Meta["selector"]
			name := strings.TrimPrefix(strings.TrimPrefix(sel, "."), "#")
			if name == "" {
				continue
			}
			key := n.Service + "\x00" + name
			def := tlElemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language}
			if n.Meta["selector_kind"] == "id" {
				idDefs[key] = append(idDefs[key], def)
			} else {
				classDefs[key] = append(classDefs[key], def)
			}
		case n.Type == graph.NodeTypeComponent && n.Language == "templ":
			for _, entry := range strings.Split(n.Meta["dom_ids"], "\n") {
				id, line := tlSplitIDLine(entry)
				if id == "" {
					continue
				}
				key := n.Service + "\x00" + id
				idDefs[key] = append(idDefs[key], tlElemDef{compID: n.ID, file: n.File, line: line, lang: "templ"})
			}
			for _, entry := range strings.Split(n.Meta["dom_classes"], "\n") {
				cls, line := tlSplitIDLine(entry)
				if cls == "" {
					continue
				}
				key := n.Service + "\x00" + cls
				classDefs[key] = append(classDefs[key], tlElemDef{compID: n.ID, file: n.File, line: line, lang: "templ"})
			}
		case n.Type == graph.NodeTypeElement:
			if id := n.Meta["id"]; id != "" {
				key := n.Service + "\x00" + id
				idDefs[key] = append(idDefs[key], tlElemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language})
			}
			if classes := n.Meta["class"]; classes != "" {
				for _, cls := range strings.Fields(classes) {
					key := n.Service + "\x00" + cls
					classDefs[key] = append(classDefs[key], tlElemDef{nodeID: n.ID, file: n.File, line: n.Line, lang: n.Language})
				}
			}
		}
	}

	sortDefs := func(defs []tlElemDef) {
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

	var out []Fact
	elemNodes := map[string]string{}
	elemNodeFor := func(svc string, d tlElemDef, elemName string, marker string) string {
		if d.nodeID != "" {
			return d.nodeID
		}
		ekey := d.compID + "\x00" + marker + elemName
		if id, ok := elemNodes[ekey]; ok {
			return id
		}
		elemID := fmt.Sprintf("%s:%s:%s:%s:%d", svc, d.file, string(graph.NodeTypeElement), marker+elemName, d.line)
		elemNodes[ekey] = elemID
		return elemID
	}

	emitDef := func(target *graph.Node, d tlElemDef, elemID, name, marker string, pred string) {
		out = append(out, Fact{
			Pred: pred,
			Args: []Atom{
				Node(target.ID), Node(elemID), Str(marker + name), Str(target.Service),
				Str(d.file), Int(int64(d.line)), Str(d.lang), Str(name), Str(d.compID),
			},
			Origin: Origin{Kind: OriginPrimitive, File: d.file, Line: d.line, Pattern: pred},
		})
		if handler := target.Meta["handler_node"]; handler != "" {
			out = append(out, Fact{
				Pred: tlListenEdgePred,
				Args: []Atom{
					Node(elemID), Node(handler), Str(target.Meta["event"]),
					Str(target.Meta["delegated"]), Str(target.Meta["delegate_root"]),
				},
				Origin: Origin{Kind: OriginPrimitive, File: target.File, Line: target.Line, Pattern: tlListenEdgePred},
			})
		}
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget {
			continue
		}
		rawSel := n.Meta["selector"]
		fn := n.Meta["fn"]
		id, cls, isComplex := tlParseDOMSelector(fn, rawSel)
		origin := Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "tl_dom_target"}

		if isComplex {
			if sel := tlStripQuote(rawSel); rawSel != "" && !strings.ContainsAny(sel, "${}`+") &&
				strings.ContainsAny(sel, ".#[:") {
				out = append(out, Fact{
					Pred:   tlSelectorDynUnresolved,
					Args:   []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(sel)},
					Origin: origin,
				})
			}
			continue
		}

		if id != "" {
			defs, ok := idDefs[n.Service+"\x00"+id]
			if !ok {
				out = append(out, Fact{
					Pred:   tlDOMRefUnresolvedPred,
					Args:   []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str("#" + id)},
					Origin: origin,
				})
				continue
			}
			for _, d := range defs {
				elemID := elemNodeFor(n.Service, d, id, "#")
				emitDef(n, d, elemID, id, "#", tlIDDefPred)
			}
			continue
		}

		if cls != "" {
			defs := classDefs[n.Service+"\x00"+cls]
			if len(defs) > tlMaxClassFanout {
				out = append(out, Fact{
					Pred: tlClassFanoutUnresolved,
					Args: []Atom{
						Str(n.Service), Str(n.File), Int(int64(n.Line)), Str("." + cls),
						Str(tlFormatFanoutTargets(defs)),
					},
					Origin: origin,
				})
				continue
			}
			for _, d := range defs {
				elemID := elemNodeFor(n.Service, d, cls, ".")
				emitDef(n, d, elemID, cls, ".", tlClassDefPred)
			}
		}
	}
	return out
}

func tlParseDOMSelector(fn, rawSelector string) (id, class string, isComplex bool) {
	sel := tlStripQuote(strings.TrimSpace(rawSelector))
	if sel == "" {
		return "", "", false
	}
	if strings.ContainsAny(sel, " ${}`+") {
		return "", "", true
	}
	if fn == "getElementById" {
		if tlReSimpleID.MatchString(sel) {
			return sel, "", false
		}
		return "", "", true
	}
	if strings.HasPrefix(sel, "#") {
		id = sel[1:]
		if tlReSimpleID.MatchString(id) {
			return id, "", false
		}
		return "", "", true
	}
	if strings.HasPrefix(sel, ".") {
		cls := sel[1:]
		if tlReSimpleID.MatchString(cls) {
			return "", cls, false
		}
		return "", "", true
	}
	if dot := strings.LastIndex(sel, "."); dot > 0 {
		cls := sel[dot+1:]
		if tlReSimpleID.MatchString(cls) && !strings.ContainsAny(sel[:dot], ".#:[") {
			return "", cls, false
		}
	}
	return "", "", true
}

func tlSplitIDLine(entry string) (string, int) {
	i := strings.LastIndexByte(entry, '@')
	if i < 0 {
		return entry, 0
	}
	line := 0
	fmt.Sscanf(entry[i+1:], "%d", &line)
	return entry[:i], line
}

func tlStripQuote(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
