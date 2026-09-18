package factpipe

import (
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
)

// hub_js_local_url.go — Tier RC.2 (docs/js-declarative-composition-cluster-plan.md):
// the Tier FX migration of internal/linker/js_local_url.go's
// ResolveJSLocalURLs. The resolution itself was already fully generic
// before this migration (jsast.ResolveLocalURLBinding, backed by
// internal/valuegraph's spec-driven engine — RC's whole point is that this
// was never the hand-written part). What this hub replaces is the
// driver+emit loop: find the candidate node's expression, call the (already
// generic) resolver, and turn the result into a mint/patch/ledger decision.
// That decision logic stays real Go here (a HubProvider, same
// "schema_url_link_sweep" shape) because it drives the value-fanout mint
// shape (one node patched in place, N-1 additional nodes minted) — the
// per-site loop is mechanical, not an algorithm, but iterating nodes and
// caching parses per file is unavoidably a loop over real data, same as
// every other hub here.
func init() {
	RegisterHub("js_local_url", julHub)
}

const (
	julPatchPred  = "jul_patch"  // (ID, URL, Label, BranchIndex)
	julMintPred   = "jul_mint"   // (ID, ParentID, BranchIndex, URL, Label, Method, Service, File, Line, Language)
	julLedgerPred = "jul_ledger" // (Service, File, Line, Name, Kind)

	julOriginLocalBinding = "local_binding"
	julVgLayer            = "L2"
	julVgLocalBindingRule = "valuegraph/javascript#local_binding"

	julLedgerUnresolved = "local_url_unresolved"
	julLineSlack         = 6
)

func julHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, schema graph.SchemaConfig, _ []graph.UnresolvedRef) []Fact {
	svc, resolver := sulBuildResolver(nodes, files, svcPath, schema)
	if svc == "" {
		return nil
	}
	var out []Fact
	fileCache := map[string]*julParsedFile{}
	for i := range nodes {
		n := &nodes[i]
		raw, ok := julCandidate(n)
		if !ok {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			jf = julParseFile(n.File)
			fileCache[n.File] = jf
		}
		if jf == nil {
			// Matches internal/linker/js_local_url.go's ResolveJSLocalURLs:
			// an unparseable file is silently skipped here, not ledgered —
			// the file itself, not this specific site, is the failure, and
			// nothing else in the pipeline can read it either.
			continue
		}
		expr := jf.exprAtLine(n.Line, raw)
		if expr == nil {
			out = append(out, julLedgerFact(n.Service, n.File, n.Line, julLedgerName(raw), julLedgerUnresolved))
			continue
		}
		fn := jsast.EnclosingFunction(expr)
		paths, reason, ok := jsast.ResolveLocalURLBinding(expr, fn, jf.src)
		if !ok || len(paths) == 0 {
			// Tier MS.1/MS.2: not a literal local binding, but a read of a
			// discovered data asset — the same resolver
			// schema_url_link_sweep uses, tried here first (this pass runs
			// before it) so a hit is attributed to the pass that actually
			// found it.
			hit, hok, hkind := resolver.ResolveURLExpr(expr, fn, jf.src, n.Service)
			verb := strings.ToUpper(n.Meta["method"])
			if hok && verb != "" {
				out = append(out, julSchemaPatchFact(n.ID, verb, hit))
				continue
			}
			if hok || hkind != "" {
				k := hkind
				if k == "" {
					k = schemaurl.LedgerSchemaEntityUnresolved
				}
				out = append(out, julLedgerFact(n.Service, n.File, n.Line, julLedgerName(raw), k))
				continue
			}
			if reason == "" {
				reason = julLedgerUnresolved
			}
			out = append(out, julLedgerFact(n.Service, n.File, n.Line, julLedgerName(raw), reason))
			continue
		}

		method := strings.ToUpper(n.Meta["method"])
		out = append(out, julPatchFact(n.ID, paths[0], julLabel(method, paths[0]), 0))
		for bi := 1; bi < len(paths); bi++ {
			out = append(out, julMintFact(n, bi, paths[bi], julLabel(method, paths[bi]), method))
		}
	}
	return out
}

// julCandidate mirrors internal/linker/js_local_url.go's localURLCandidate —
// same criteria, ported rather than shared, matching the sulSchemaURLCandidate/
// sulParsedFile precedent already in this file's sibling hub
// (hub_schema_url_link.go's own doc comment: "not worth threading through a
// shared package for one caller here and one there").
func julCandidate(n *graph.Node) (raw string, ok bool) {
	if n.Type != graph.NodeTypeHTTPClient || n.File == "" {
		return "", false
	}
	if n.Language != "javascript" && n.Language != "typescript" {
		return "", false
	}
	if n.Meta["nav_link"] != "" || n.Meta["key_dynamic"] != "true" {
		return "", false
	}
	if n.Meta["url"] != "" || n.Meta["path"] != "" {
		return "", false
	}
	if n.Meta["url_origin"] == julOriginLocalBinding {
		return "", false
	}
	raw = n.Meta["key_dynamic_raw"]
	if raw == "" || raw == "(attached)" {
		return "", false
	}
	return raw, true
}

func julLabel(method, path string) string {
	if method != "" {
		return method + " " + path
	}
	return path
}

func julLedgerName(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "(dynamic)"
	}
	return raw
}

func julPatchFact(id, url, label string, branch int) Fact {
	return Fact{
		Pred: julPatchPred,
		Args: []Atom{Node(id), Str(url), Str(label), Int(int64(branch))},
		Origin: Origin{Kind: OriginPrimitive, Pattern: julPatchPred},
	}
}

func julMintFact(n *graph.Node, branch int, url, label, method string) Fact {
	id := n.ID + ":ul" + strconv.Itoa(branch)
	return Fact{
		Pred: julMintPred,
		Args: []Atom{
			Str(id), Node(n.ID), Int(int64(branch)), Str(url), Str(label), Str(method),
			Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(n.Language),
		},
		Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: julMintPred},
	}
}

func julSchemaPatchFact(id, verb string, hit schemaurl.Hit) Fact {
	return Fact{
		Pred: julPatchPred + "_schema",
		Args: []Atom{
			Node(id), Str(hit.Path), Str(verb + " " + hit.Path),
			Str(hit.File), Str(hit.Entity), Str(hit.Key), Str(hit.RawURL),
		},
		Origin: Origin{Kind: OriginPrimitive, Pattern: julPatchPred + "_schema"},
	}
}

func julLedgerFact(svc, file string, line int, name, kind string) Fact {
	return Fact{
		Pred:   julLedgerPred,
		Args:   []Atom{Str(svc), Str(file), Int(int64(line)), Str(name), Str(kind)},
		Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: julLedgerPred},
	}
}

// julParsedFile + exprAtLine mirror hub_schema_url_link.go's sulParsedFile —
// a small, self-contained line-window text match, deliberately duplicated
// rather than shared (same call already made for that hub).
type julParsedFile struct {
	src  []byte
	root *sitter.Node
}

func julParseFile(file string) *julParsedFile {
	src, root, _, ok := jsast.Parse(file)
	if !ok || root == nil {
		return nil
	}
	return &julParsedFile{src: src, root: root}
}

func (jf *julParsedFile) exprAtLine(line int, raw string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row > line+julLineSlack {
			return
		}
		if row >= line && n.Content(jf.src) == raw {
			found = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(jf.root)
	return found
}
