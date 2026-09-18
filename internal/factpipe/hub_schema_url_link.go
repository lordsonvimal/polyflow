package factpipe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
)

// hub_schema_url_link.go registers TWO hub providers — "schema_url_link_props"
// and "schema_url_link_sweep" — the Tier FX migration of
// internal/linker/js_prop_client.go's retired LinkJSPropClients (SPA.4/SPA.5)
// and internal/linker/schema_url_link.go's retired ResolveSchemaURLs
// (FX.8.29) respectively. Both share the SAME per-service schemaurl.Resolver
// (sulBuildResolver), which is why they live in one file, but they are
// deliberately TWO separate hubs/frameworks, not one:
// internal/indexer/link_passes.go must call the props half BEFORE
// js_local_urls (Tier UL) and the sweep half AFTER it — schema_url_link's
// own retired doc comment says so explicitly ("Runs after js_local_urls so a
// node it could already read is left alone"), and js_local_urls itself must
// see prop-client-minted nodes as candidates. A single combined hub run
// once would also make pipeline.Run's one merged Result.Unresolved slice
// impossible to split correctly between the two call sites, since both
// halves can independently produce the SAME ledger Kind strings
// (schema_entity_unresolved/ambiguous, schema_key_ambiguous) for entirely
// different reasons — two frameworks give each call site its own
// unambiguous Result.Unresolved instead.
//
// Scoped out 2026-09-16 before this file existed: migrating ResolveSchemaURLs
// alone looked meta-mutation-only (patch:-shaped, like hints/config_baseurl),
// but it shares its resolver with LinkJSPropClients, an SPA.4 mint pass whose
// full algorithm (HOC detection, dynamic-URL-builder shape synthesis,
// call-site resolution) stays hand-written Go here, ported near-verbatim —
// same "hub carries the whole algorithm" shape as pusher_producer/
// react_prop_urls. internal/linker/js_local_url.go's ResolveJSLocalURLs is a
// THIRD consumer of the same resolver that was never part of this migration
// (out of scope, still hand-written Go in internal/linker) — its own copy of
// the resolver is built directly via internal/schemaurl by
// internal/indexer/link_passes.go's still-live "schema_url_tables" pass, not
// by this hub. Both hubs here recompute their own resolver from the same
// inputs rather than sharing state across pipeline.Run calls.
func init() {
	RegisterHub("schema_url_link_props", schemaURLLinkPropsHub)
	RegisterHub("schema_url_link_sweep", schemaURLLinkSweepHub)
}

const (
	sulPropClientMintPred   = "sul_prop_client_mint"   // (ID, Service, File, Line, Label, Method, URL, KeyCandidates, URLOrigin, BranchIndex, SchemaFile, SchemaEntity, SchemaKey, SchemaURLRaw)
	sulPropClientEdgePred   = "sul_prop_client_edge"   // (FnID, ID)
	sulPropClientLedgerPred = "sul_prop_client_ledger" // (Service, File, Line, Name, Kind)
	sulSchemaPatchPred      = "sul_schema_patch"       // (ID, Label, URL, URLOrigin, SchemaFile, SchemaEntity, SchemaKey, SchemaURLRaw)
	sulSchemaLedgerPred     = "sul_schema_ledger"      // (Service, File, Line, Name, Kind)
)

func schemaURLLinkPropsHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, schema graph.SchemaConfig) []Fact {
	svc, resolver := sulBuildResolver(nodes, files, svcPath, schema)
	if svc == "" {
		return nil
	}
	return sulPropClientFacts(nodes, files, svc, resolver)
}

func schemaURLLinkSweepHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, schema graph.SchemaConfig) []Fact {
	svc, resolver := sulBuildResolver(nodes, files, svcPath, schema)
	if svc == "" || resolver == nil {
		return nil
	}
	return sulSchemaPatchFacts(nodes, svc, resolver)
}

// sulBuildResolver builds the per-service schemaurl.Resolver every hub in
// this cluster needs (this file's two, plus hub_js_local_url.go's), from the
// same handler-corroboration inputs the retired "schema_url_tables" pass
// computed. A hub cannot share Go state across pipeline.Run calls, so this
// rebuilds independently every call.
//
// MS.0 discovery needs to SEE the schema asset file itself (a .json/.yaml
// checked-in config, not source) — `files` (a HubProvider's own parameter,
// internal/indexer/link_passes.go's `sf.files`) is the parser-recognized
// subset walkService built, which deliberately excludes exactly these
// assets (internal/indexer/indexer.go's walkService: "if parser.ForFile(path)
// != nil { files = append(...) }, else unparsed[key]++"). Without svcPath,
// this resolver can only ever see zero assets — a real regression Tier RC.2
// caught (docs/js-declarative-composition-cluster-plan.md): js_local_urls
// used to resolve these sites through a WIDE resolver built once via
// walkAllFiles (internal/indexer/link_passes.go's "schema_url_tables" pass),
// which silently masked this hub's own narrower one never having discovered
// anything on a fresh run. svcPath (walked here, same exclusions as
// internal/indexer's walkAllFiles: dotfiles/node_modules/vendor/dist/build)
// fixes it for every caller, not just the one that exposed it.
func sulBuildResolver(nodes []graph.Node, files []string, svcPath string, schema graph.SchemaConfig) (svc string, resolver *schemaurl.Resolver) {
	svc = sulServiceOf(nodes)
	if svc == "" {
		return "", nil
	}
	handlerPaths := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		if norm, ok := schemaurl.NormalizeSchemaPath(n.Meta["path"]); ok {
			handlerPaths[norm] = true
		}
	}
	allFiles := files
	if svcPath != "" {
		allFiles = sulWalkAllFiles(svcPath)
	}
	serviceFiles := map[string][]string{svc: allFiles}
	tables, _ := schemaurl.LoadTables(serviceFiles, map[string]map[string]bool{svc: handlerPaths}, schemaurl.Config(schema))
	return svc, schemaurl.NewResolver(tables, serviceFiles)
}

// sulWalkAllFiles returns every regular file under root, no parser-recognition
// filtering — mirrors internal/indexer/indexer.go's walkAllFiles, ported
// rather than shared (internal/factpipe cannot import internal/indexer).
func sulWalkAllFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root {
				switch d.Name() {
				case ".git", "node_modules", "vendor", "dist", "build", ".next", ".nuxt", ".svelte-kit":
					return filepath.SkipDir
				}
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		files = append(files, p)
		return nil
	})
	return files
}

func sulServiceOf(nodes []graph.Node) string {
	for i := range nodes {
		if nodes[i].Service != "" {
			return nodes[i].Service
		}
	}
	return ""
}

// ── schema_url_links: existing-node sweep (retired ResolveSchemaURLs) ──────

// sulSchemaPatchFacts sweeps every JS/TS http_client the matcher left
// dynamic, resolving its URL expression through resolver — ported from
// internal/linker/schema_url_link.go's ResolveSchemaURLs.
func sulSchemaPatchFacts(nodes []graph.Node, svc string, resolver *schemaurl.Resolver) []Fact {
	if resolver == nil {
		return nil
	}
	var out []Fact
	fileCache := map[string]*sulParsedFile{}
	for i := range nodes {
		n := &nodes[i]
		raw, ok := sulSchemaURLCandidate(n)
		if !ok {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			jf = sulParseHostFile(n.File)
			fileCache[n.File] = jf
		}
		if jf == nil {
			continue
		}
		expr := jf.exprAtLine(n.Line, raw)
		if expr == nil {
			continue
		}
		fn := jsast.EnclosingFunction(expr)
		hit, ok, kind := resolver.ResolveURLExpr(expr, fn, jf.src, svc)
		if kind != "" {
			out = append(out, sulLedgerFact(sulSchemaLedgerPred, svc, n.File, n.Line, raw, kind))
			continue
		}
		if !ok {
			continue
		}
		verb := strings.ToUpper(n.Meta["method"])
		if verb == "" {
			out = append(out, sulLedgerFact(sulSchemaLedgerPred, svc, n.File, n.Line, raw, "schema_entity_unresolved"))
			continue
		}
		out = append(out, Fact{
			Pred: sulSchemaPatchPred,
			Args: []Atom{
				Node(n.ID), Str(verb + " " + hit.Path), Str(hit.Path), Str("schema_asset"),
				Str(hit.File), Str(hit.Entity), Str(hit.Key), Str(hit.RawURL),
			},
			Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: sulSchemaPatchPred},
		})
	}
	return out
}

func sulSchemaURLCandidate(n *graph.Node) (raw string, ok bool) {
	if n.Type != graph.NodeTypeHTTPClient || n.File == "" {
		return "", false
	}
	if n.Language != "javascript" && n.Language != "typescript" {
		return "", false
	}
	if n.Meta["url"] != "" || n.Meta["path"] != "" {
		return "", false
	}
	if n.Meta["url_origin"] == "schema_asset" {
		return "", false
	}
	if n.Meta["key_dynamic"] != "true" {
		return "", false
	}
	raw = n.Meta["key_dynamic_raw"]
	if raw == "" || raw == "(attached)" {
		return "", false
	}
	return raw, true
}

func sulLedgerFact(pred, svc, file string, line int, name, kind string) Fact {
	return Fact{
		Pred:   pred,
		Args:   []Atom{Str(svc), Str(file), Int(int64(line)), Str(name), Str(kind)},
		Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: pred},
	}
}

// sulParsedFile + exprAtLine mirror internal/linker/js_http_hosts.go's
// jsHostFile/exprAtLine (a small, self-contained line-window text match, not
// worth threading through a shared package for one caller here and one
// there).
type sulParsedFile struct {
	src  []byte
	root *sitter.Node
}

func sulParseHostFile(file string) *sulParsedFile {
	src, root, _, ok := jsast.Parse(file)
	if !ok || root == nil {
		return nil
	}
	return &sulParsedFile{src: src, root: root}
}

const sulLineSlack = 6

func (jf *sulParsedFile) exprAtLine(line int, raw string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row > line+sulLineSlack {
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

// ── js_prop_clients: SPA.4 prop-injected HTTP-client wrapper (retired
//    LinkJSPropClients) + SPA.5 dynamic-URL-builder functions ──────────────

type sulPropClientMethod struct {
	Verb        string
	URLArgIndex int
	URLOptKey   string
}

type sulPropClientSpec struct {
	HOCExport    string
	InjectedProp string
	Methods      map[string]sulPropClientMethod
}

var sulPropClientMethodNames = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
	"del": true, "ajax": true, "request": true, "fetch": true, "head": true,
}

func sulPropClientVerb(name string) string {
	switch name {
	case "get", "post", "put", "patch", "delete", "head":
		return strings.ToUpper(name)
	case "del":
		return "DELETE"
	}
	return ""
}

type sulFnDef struct {
	node *sitter.Node
	src  []byte
}

func sulIndexFnDefs(root *sitter.Node, src []byte, emit func(name string, fn *sitter.Node)) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration", "generator_function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(src), n)
			}
		case "variable_declarator", "public_field_definition", "field_definition":
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression", "function":
					if nm := n.ChildByFieldName("name"); nm != nil {
						emit(nm.Content(src), v)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

// sulPropClientFacts is the SPA.4/SPA.5 pass, ported from
// internal/linker/js_prop_client.go's LinkJSPropClients.
func sulPropClientFacts(nodes []graph.Node, files []string, svc string, resolver *schemaurl.Resolver) []Fact {
	fnByLabel := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			if fnByLabel[n.Label] == "" {
				fnByLabel[n.Label] = n.ID
			}
		}
	}

	walker := contract.KeyWalkerFor("javascript")

	specs := map[string]sulPropClientSpec{}
	fnDefs := map[string]sulFnDef{}
	type parsedFile struct {
		rel  string
		src  []byte
		root *sitter.Node
	}
	var pfiles []parsedFile
	seen := map[string]bool{}
	for _, abs := range files {
		if !jsast.IsJSFile(abs) {
			continue
		}
		rel := sulRelativize(abs)
		if seen[rel] || jsast.IsTestFile(rel) {
			continue
		}
		seen[rel] = true
		src, root, _, ok := jsast.Parse(abs)
		if !ok {
			continue
		}
		pfiles = append(pfiles, parsedFile{rel: rel, src: src, root: root})
		sulIndexFnDefs(root, src, func(name string, fn *sitter.Node) {
			if _, exists := fnDefs[name]; !exists {
				fnDefs[name] = sulFnDef{node: fn, src: src}
			}
		})
		if spec, ok := sulDetectPropClientSpec(root, src); ok {
			specs[spec.InjectedProp] = spec
		}
	}
	if len(specs) == 0 {
		return nil
	}

	var out []Fact
	mintSeen := map[string]bool{}
	for _, pf := range pfiles {
		if len(specs) == 0 {
			continue
		}
		fileText := string(pf.src)
		propsNames := sulCollectPropsBindings(pf.root, pf.src)
		var walk func(n *sitter.Node, fn string, names map[string]bool)
		walk = func(n *sitter.Node, fn string, names map[string]bool) {
			extra := sulParamPropClientNames(n, pf.src, specs)
			for k := range sulParamDerivedPropClientNames(n, pf.src, specs) {
				if extra == nil {
					extra = make(map[string]bool, 1)
				}
				extra[k] = true
			}
			if len(extra) > 0 {
				merged := make(map[string]bool, len(names)+len(extra))
				for k := range names {
					merged[k] = true
				}
				for k := range extra {
					merged[k] = true
				}
				names = merged
			}
			switch n.Type() {
			case "function_declaration", "method_definition":
				if nm := n.ChildByFieldName("name"); nm != nil {
					fn = nm.Content(pf.src)
				}
			case "variable_declarator", "public_field_definition", "field_definition":
				if v := n.ChildByFieldName("value"); v != nil {
					switch v.Type() {
					case "arrow_function", "function_expression", "function":
						if nm := n.ChildByFieldName("name"); nm != nil {
							fn = nm.Content(pf.src)
						}
					}
				}
			case "call_expression":
				if _, method, urlNode, siteVerb, line, ok := sulPropClientCallSite(n, pf.src, specs, fileText, names); !ok {
					if _, _, line, recognised := sulPropClientRecognisedSite(n, pf.src, specs, fileText, names); recognised {
						out = append(out, sulLedgerFact(sulPropClientLedgerPred, svc, pf.rel, line, "(no url argument)", "prop_client_dynamic_url"))
					}
				} else {
					verb := method.Verb
					if siteVerb != "" {
						verb = siteVerb
					}
					verbKnown := verb != ""
					if verb == "" {
						verb = "GET"
					}

					cands, dyn := sulWalkerKey(walker, urlNode, pf.src)

					type mintReq struct {
						path         string
						cands        []string
						localBinding bool
						schemaMeta   map[string]string
					}
					var reqs []mintReq

					if !dyn && len(cands) > 0 &&
						(strings.HasPrefix(cands[0], "/") || strings.HasPrefix(cands[0], "*")) {
						reqs = append(reqs, mintReq{path: cands[0], cands: cands})
					} else if shapes, fname, kind := sulDynamicURLBuilder(urlNode, pf.src, fnDefs, walker); kind == "shapes" {
						for _, sh := range shapes {
							reqs = append(reqs, mintReq{path: sh, cands: []string{sh}})
						}
					} else if paths, reason, ok := jsast.ResolveLocalURLBinding(urlNode, jsast.EnclosingFunction(n), pf.src); ok {
						for _, p := range paths {
							reqs = append(reqs, mintReq{path: p, cands: []string{p}, localBinding: true})
						}
					} else if hit, hok, hkind := resolver.ResolveURLExpr(urlNode, jsast.EnclosingFunction(n), pf.src, svc); hok || hkind != "" {
						if hok && verbKnown {
							reqs = append(reqs, mintReq{path: hit.Path, cands: []string{hit.Path}, schemaMeta: schemaurl.MintMeta(hit)})
						} else {
							kk := hkind
							if kk == "" {
								kk = "schema_entity_unresolved"
							}
							out = append(out, sulLedgerFact(sulPropClientLedgerPred, svc, pf.rel, line, "(schema)", kk))
						}
					} else {
						name := "(dynamic)"
						k := "prop_client_dynamic_url"
						switch {
						case fname != "":
							name, k = fname, kind
						case len(cands) > 0:
							name = cands[0]
						case reason == jsast.LedgerLocalURLHighFanout:
							k = reason
						}
						out = append(out, sulLedgerFact(sulPropClientLedgerPred, svc, pf.rel, line, name, k))
					}

					for ri, req := range reqs {
						id := fmt.Sprintf("%s:%s:http_client:prop_client:%d", svc, pf.rel, line)
						if len(reqs) > 1 {
							id = fmt.Sprintf("%s:%d", id, ri)
						}
						if mintSeen[id] {
							continue
						}
						mintSeen[id] = true
						keyCandidates := ""
						if len(req.cands) > 1 {
							keyCandidates = contract.MarshalKeyCandidates(req.cands)
						}
						urlOrigin, branchIndex := "", ""
						if req.localBinding {
							urlOrigin = "local_binding"
							branchIndex = strconv.Itoa(ri)
						}
						schemaFile, schemaEntity, schemaKey, schemaRaw := "", "", "", ""
						if req.schemaMeta != nil {
							urlOrigin = req.schemaMeta["url_origin"]
							schemaFile = req.schemaMeta["schema_file"]
							schemaEntity = req.schemaMeta["schema_entity"]
							schemaKey = req.schemaMeta["schema_key"]
							schemaRaw = req.schemaMeta["schema_url_raw"]
						}
						out = append(out, Fact{
							Pred: sulPropClientMintPred,
							Args: []Atom{
								Node(id), Str(svc), Str(pf.rel), Int(int64(line)), Str(verb + " " + req.path),
								Str(verb), Str(req.path), Str(keyCandidates), Str(urlOrigin), Str(branchIndex),
								Str(schemaFile), Str(schemaEntity), Str(schemaKey), Str(schemaRaw),
							},
							Origin: Origin{Kind: OriginPrimitive, File: pf.rel, Line: line, Pattern: sulPropClientMintPred},
						})
						if fnID := fnByLabel[fn]; fnID != "" && fnID != id {
							out = append(out, Fact{
								Pred:   sulPropClientEdgePred,
								Args:   []Atom{Node(fnID), Node(id)},
								Origin: Origin{Kind: OriginPrimitive, File: pf.rel, Line: line, Pattern: sulPropClientEdgePred},
							})
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i), fn, names)
			}
		}
		walk(pf.root, "(module)", propsNames)
	}
	return out
}

// ── SPA.5: dynamic URL-builder functions ────────────────────────────────────

func sulDynamicURLBuilder(urlNode *sitter.Node, src []byte, defs map[string]sulFnDef, w contract.KeyWalker) (shapes []string, fnName, kind string) {
	if urlNode == nil || urlNode.Type() != "call_expression" {
		return nil, "", ""
	}
	callee := urlNode.ChildByFieldName("function")
	if callee == nil || callee.Type() != "identifier" {
		return nil, "", ""
	}
	name := callee.Content(src)
	def, ok := defs[name]
	if !ok {
		return nil, name, "dynamic_url_builder"
	}
	sh, ok := sulSynthURLBuilderShapes(w, def.node, def.src)
	if !ok || len(sh) == 0 {
		return nil, name, "dynamic_url_builder"
	}
	if len(sh) > 6 {
		return nil, name, "dynamic_url_fanout"
	}
	return sh, name, "shapes"
}

func sulSynthURLBuilderShapes(w contract.KeyWalker, fn *sitter.Node, src []byte) ([]string, bool) {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil, false
	}
	var rets []*sitter.Node
	if body.Type() != "statement_block" {
		rets = append(rets, body)
	} else {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			switch n.Type() {
			case "function_declaration", "function_expression", "arrow_function", "function":
				return
			case "return_statement":
				if n.NamedChildCount() > 0 {
					rets = append(rets, n.NamedChild(0))
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
		}
		for i := 0; i < int(body.NamedChildCount()); i++ {
			walk(body.NamedChild(i))
		}
	}
	if len(rets) == 0 {
		return nil, false
	}
	set := make(map[string]bool)
	for _, r := range rets {
		cands, dyn := sulWalkerKey(w, r, src)
		if dyn || len(cands) == 0 {
			return nil, false
		}
		for _, c := range cands {
			if !strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "*") {
				return nil, false
			}
			set[c] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, true
}

func sulWalkerKey(w contract.KeyWalker, node *sitter.Node, src []byte) ([]string, bool) {
	if w == nil || node == nil {
		return nil, true
	}
	cands, dyn := w.WalkKey(node, src, func(string) (string, bool) { return "", false })
	return cands, dyn
}

// ── call-site recognition ───────────────────────────────────────────────────

func sulPropClientRecognisedSite(call *sitter.Node, src []byte, specs map[string]sulPropClientSpec, fileText string, propsNames map[string]bool) (sulPropClientSpec, sulPropClientMethod, int, bool) {
	var zero sulPropClientSpec
	callee := call.ChildByFieldName("function")
	if callee == nil || callee.Type() != "member_expression" {
		return zero, sulPropClientMethod{}, 0, false
	}
	propNode := callee.ChildByFieldName("property")
	obj := callee.ChildByFieldName("object")
	if propNode == nil || obj == nil {
		return zero, sulPropClientMethod{}, 0, false
	}
	prop, ok := sulPropClientReceiverProp(obj, src)
	if !ok {
		return zero, sulPropClientMethod{}, 0, false
	}
	spec, ok := specs[prop]
	if !ok {
		return zero, sulPropClientMethod{}, 0, false
	}
	if obj.Type() == "identifier" {
		if !propsNames[prop] && (spec.HOCExport == "" || !strings.Contains(fileText, spec.HOCExport)) {
			return zero, sulPropClientMethod{}, 0, false
		}
	}
	method, ok := spec.Methods[propNode.Content(src)]
	if !ok {
		return zero, sulPropClientMethod{}, 0, false
	}
	return spec, method, int(call.StartPoint().Row) + 1, true
}

func sulParamPropClientNames(fn *sitter.Node, src []byte, specs map[string]sulPropClientSpec) map[string]bool {
	if fn == nil || len(specs) == 0 {
		return nil
	}
	var out map[string]bool
	for nm := range sulParamBindingNames(fn, src) {
		if _, ok := specs[nm]; !ok {
			continue
		}
		if out == nil {
			out = make(map[string]bool, 1)
		}
		out[nm] = true
	}
	return out
}

func sulParamBindingNames(fn *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var bind func(p *sitter.Node)
	bind = func(p *sitter.Node) {
		if p == nil {
			return
		}
		switch p.Type() {
		case "identifier", "shorthand_property_identifier_pattern", "shorthand_property_identifier":
			out[p.Content(src)] = true
			return
		case "object_pattern", "array_pattern":
			for i := 0; i < int(p.NamedChildCount()); i++ {
				bind(p.NamedChild(i))
			}
			return
		case "pair_pattern":
			bind(p.ChildByFieldName("value"))
			return
		}
		if pat := p.ChildByFieldName("pattern"); pat != nil {
			bind(pat)
			return
		}
		if l := p.ChildByFieldName("left"); l != nil {
			bind(l)
			return
		}
		for i := 0; i < int(p.NamedChildCount()); i++ {
			switch c := p.NamedChild(i); c.Type() {
			case "identifier", "object_pattern", "array_pattern":
				bind(c)
				return
			}
		}
	}
	if params := fn.ChildByFieldName("parameters"); params != nil {
		for i := 0; i < int(params.NamedChildCount()); i++ {
			bind(params.NamedChild(i))
		}
		return out
	}
	bind(fn.ChildByFieldName("parameter"))
	return out
}

func sulParamDerivedPropClientNames(fn *sitter.Node, src []byte, specs map[string]sulPropClientSpec) map[string]bool {
	if fn == nil || len(specs) == 0 {
		return nil
	}
	params := sulParamBindingNames(fn, src)
	if len(params) == 0 {
		return nil
	}
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out map[string]bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "variable_declarator" {
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name != nil && name.Type() == "object_pattern" && val != nil && sulFromParam(val, src, params) {
				for i := 0; i < int(name.NamedChildCount()); i++ {
					c := name.NamedChild(i)
					var nm string
					switch c.Type() {
					case "shorthand_property_identifier_pattern", "shorthand_property_identifier":
						nm = c.Content(src)
					case "pair_pattern":
						if k := c.ChildByFieldName("key"); k != nil {
							nm = k.Content(src)
						}
					}
					if nm == "" {
						continue
					}
					if _, ok := specs[nm]; !ok {
						continue
					}
					if out == nil {
						out = make(map[string]bool, 1)
					}
					out[nm] = true
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

func sulFromParam(val *sitter.Node, src []byte, params map[string]bool) bool {
	switch val.Type() {
	case "identifier":
		return params[val.Content(src)]
	case "member_expression":
		obj := val.ChildByFieldName("object")
		prop := val.ChildByFieldName("property")
		return obj != nil && prop != nil && obj.Type() == "identifier" &&
			prop.Content(src) == "props" && params[obj.Content(src)]
	}
	return false
}

func sulPropClientCallSite(call *sitter.Node, src []byte, specs map[string]sulPropClientSpec, fileText string, propsNames map[string]bool) (sulPropClientSpec, sulPropClientMethod, *sitter.Node, string, int, bool) {
	var zero sulPropClientSpec
	miss := func() (sulPropClientSpec, sulPropClientMethod, *sitter.Node, string, int, bool) {
		return zero, sulPropClientMethod{}, nil, "", 0, false
	}
	spec, method, _, ok := sulPropClientRecognisedSite(call, src, specs, fileText, propsNames)
	if !ok {
		return miss()
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return miss()
	}
	var argNodes []*sitter.Node
	for i := 0; i < int(args.NamedChildCount()); i++ {
		argNodes = append(argNodes, args.NamedChild(i))
	}
	if method.URLArgIndex >= len(argNodes) {
		return miss()
	}
	urlNode := argNodes[method.URLArgIndex]
	var siteVerb string
	if method.URLOptKey != "" {
		optsObj := urlNode
		if optsObj.Type() != "object" {
			if nm := jsast.LocalIdentName(optsObj, src); nm != "" {
				if efn := jsast.EnclosingFunction(call); efn != nil {
					for _, rhs := range jsast.LocalAssignments(efn, optsObj.StartByte(), src, nm) {
						if rhs.Type() == "object" {
							optsObj = rhs
						}
					}
				}
			}
		}
		urlNode = jsast.ObjectKeyValue(optsObj, src, method.URLOptKey)
		if urlNode == nil {
			return miss()
		}
		for _, k := range []string{"type", "method"} {
			if vn := jsast.ObjectKeyValue(optsObj, src, k); vn != nil && vn.Type() == "string" {
				siteVerb = strings.ToUpper(strings.Trim(vn.Content(src), `"'`))
			}
		}
	}
	line := int(call.StartPoint().Row) + 1
	return spec, method, urlNode, siteVerb, line, true
}

func sulPropClientReceiverProp(obj *sitter.Node, src []byte) (string, bool) {
	switch obj.Type() {
	case "identifier":
		return obj.Content(src), true
	case "member_expression":
		prop := obj.ChildByFieldName("property")
		inner := obj.ChildByFieldName("object")
		if prop == nil || inner == nil || inner.Type() != "member_expression" {
			return "", false
		}
		innerProp := inner.ChildByFieldName("property")
		innerObj := inner.ChildByFieldName("object")
		if innerProp == nil || innerObj == nil {
			return "", false
		}
		if innerProp.Content(src) == "props" && innerObj.Type() == "this" {
			return prop.Content(src), true
		}
	}
	return "", false
}

func sulCollectPropsBindings(root *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "variable_declarator":
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name != nil && name.Type() == "object_pattern" && val != nil &&
				val.Type() == "member_expression" &&
				strings.TrimSpace(val.Content(src)) == "this.props" {
				for i := 0; i < int(name.NamedChildCount()); i++ {
					c := name.NamedChild(i)
					switch c.Type() {
					case "shorthand_property_identifier_pattern", "shorthand_property_identifier":
						out[c.Content(src)] = true
					case "pair_pattern":
						if k := c.ChildByFieldName("key"); k != nil {
							out[k.Content(src)] = true
						}
					}
				}
			}
		case "member_expression":
			inner := n.ChildByFieldName("object")
			prop := n.ChildByFieldName("property")
			if inner != nil && prop != nil && inner.Type() == "member_expression" &&
				strings.TrimSpace(inner.Content(src)) == "this.props" {
				out[prop.Content(src)] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// ── HOC detection ───────────────────────────────────────────────────────────

func sulDetectPropClientSpec(root *sitter.Node, src []byte) (sulPropClientSpec, bool) {
	var result sulPropClientSpec
	found := false

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found {
			return
		}
		switch n.Type() {
		case "function_declaration", "function_expression", "arrow_function":
			if spec, ok := sulPropClientFromHOCBody(n, src); ok {
				spec.HOCExport = sulHOCExportName(n, src)
				result = spec
				found = true
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return result, found
}

func sulPropClientFromHOCBody(fn *sitter.Node, src []byte) (sulPropClientSpec, bool) {
	var zero sulPropClientSpec
	params := fn.ChildByFieldName("parameters")
	body := fn.ChildByFieldName("body")
	if params == nil || body == nil || params.NamedChildCount() == 0 {
		return zero, false
	}
	wrapped := sulParamName(params.NamedChild(0), src)
	if wrapped == "" {
		return zero, false
	}
	injected := sulJSXSelfPropForElement(body, src, wrapped)
	if injected == "" {
		return zero, false
	}
	methods := sulScanClassTransportMethods(body, src)
	if len(methods) == 0 {
		return zero, false
	}
	return sulPropClientSpec{InjectedProp: injected, Methods: methods}, true
}

func sulParamName(p *sitter.Node, src []byte) string {
	if p == nil {
		return ""
	}
	if p.Type() == "identifier" {
		return p.Content(src)
	}
	if pat := p.ChildByFieldName("pattern"); pat != nil && pat.Type() == "identifier" {
		return pat.Content(src)
	}
	for i := 0; i < int(p.NamedChildCount()); i++ {
		if c := p.NamedChild(i); c.Type() == "identifier" {
			return c.Content(src)
		}
	}
	return ""
}

func sulJSXSelfPropForElement(body *sitter.Node, src []byte, tag string) string {
	var out string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if out != "" {
			return
		}
		switch n.Type() {
		case "jsx_opening_element", "jsx_self_closing_element":
			name := n.ChildByFieldName("name")
			if name != nil && name.Content(src) == tag {
				for i := 0; i < int(n.NamedChildCount()); i++ {
					attr := n.NamedChild(i)
					if attr.Type() != "jsx_attribute" {
						continue
					}
					if attr.NamedChildCount() < 2 {
						continue
					}
					an := attr.NamedChild(0)
					av := attr.NamedChild(1)
					if av.Type() == "jsx_expression" && strings.TrimSpace(sulInnerText(av, src)) == "this" {
						out = an.Content(src)
						return
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

func sulInnerText(n *sitter.Node, src []byte) string {
	if n.NamedChildCount() == 1 {
		return n.NamedChild(0).Content(src)
	}
	t := n.Content(src)
	t = strings.TrimPrefix(t, "{")
	t = strings.TrimSuffix(t, "}")
	return t
}

func sulScanClassTransportMethods(body *sitter.Node, src []byte) map[string]sulPropClientMethod {
	var cls *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if cls != nil {
			return
		}
		switch n.Type() {
		case "class", "class_declaration":
			cls = n
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	if cls == nil {
		return nil
	}
	var clsBody *sitter.Node
	for i := 0; i < int(cls.NamedChildCount()); i++ {
		if c := cls.NamedChild(i); c.Type() == "class_body" {
			clsBody = c
			break
		}
	}
	if clsBody == nil {
		return nil
	}

	type member struct {
		name    string
		fnNode  *sitter.Node
		bodyTxt string
	}
	var members []member
	for i := 0; i < int(clsBody.NamedChildCount()); i++ {
		c := clsBody.NamedChild(i)
		var name string
		var fnNode *sitter.Node
		switch c.Type() {
		case "method_definition":
			if nm := c.ChildByFieldName("name"); nm != nil {
				name = nm.Content(src)
			}
			fnNode = c
		case "public_field_definition", "field_definition":
			v := c.ChildByFieldName("value")
			if v == nil {
				continue
			}
			switch v.Type() {
			case "arrow_function", "function_expression", "function":
				if nm := c.ChildByFieldName("name"); nm != nil {
					name = nm.Content(src)
				}
				fnNode = v
			}
		}
		if name == "" || fnNode == nil {
			continue
		}
		members = append(members, member{name: name, fnNode: fnNode, bodyTxt: fnNode.Content(src)})
	}

	directReach := func(txt string) bool {
		for _, m := range []string{"$.ajax", "window.$", ".ajax(", "fetch(", "axios(", "XMLHttpRequest", "$.get(", "$.post("} {
			if strings.Contains(txt, m) {
				return true
			}
		}
		return false
	}
	transportMembers := make(map[string]bool)
	for _, m := range members {
		if directReach(m.bodyTxt) {
			transportMembers[m.name] = true
		}
	}

	out := make(map[string]sulPropClientMethod)
	for _, m := range members {
		if !sulPropClientMethodNames[m.name] {
			continue
		}
		reaches := transportMembers[m.name]
		if !reaches {
			for sib := range transportMembers {
				if strings.Contains(m.bodyTxt, "this."+sib+"(") {
					reaches = true
					break
				}
			}
		}
		if !reaches {
			continue
		}
		idx, optKey, ok := sulPropClientURLArg(m.fnNode, src)
		if !ok {
			continue
		}
		out[m.name] = sulPropClientMethod{
			Verb:        sulPropClientVerb(m.name),
			URLArgIndex: idx,
			URLOptKey:   optKey,
		}
	}
	return out
}

func sulPropClientURLArg(fn *sitter.Node, src []byte) (int, string, bool) {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return 0, "", false
	}
	var names []string
	for i := 0; i < int(params.NamedChildCount()); i++ {
		names = append(names, sulParamName(params.NamedChild(i), src))
	}
	for i, nm := range names {
		if nm == "url" {
			return i, "", true
		}
	}
	body := fn.ChildByFieldName("body")
	bodyTxt := ""
	if body != nil {
		bodyTxt = body.Content(src)
	}
	for i, nm := range names {
		if i == 0 || nm == "" {
			continue
		}
		if strings.Contains(bodyTxt, "$.ajax("+nm+")") ||
			strings.Contains(bodyTxt, "ajax("+nm+")") ||
			strings.Contains(bodyTxt, "fetch("+nm+")") ||
			strings.Contains(bodyTxt, nm+".url") {
			return i, "url", true
		}
	}
	return 0, "", false
}

func sulHOCExportName(fn *sitter.Node, src []byte) string {
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "variable_declarator":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		case "function_declaration":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		}
	}
	if nm := fn.ChildByFieldName("name"); nm != nil {
		return nm.Content(src)
	}
	return ""
}

// sulRelativize mirrors internal/patterns.RelativizeToCwd (matcher.go) — this
// package cannot import internal/patterns (it imports internal/factpipe, an
// import cycle), so the small, stable cwd-relativization logic is
// duplicated rather than restructured — same precedent as hub_js_hoc.go's
// jhcRelativize.
var sulRawCwd = sync.OnceValue(func() string {
	cwd, _ := os.Getwd()
	return cwd
})

var sulCanonCwd = sync.OnceValue(func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
})

func sulRelativize(file string) string {
	if raw := sulRawCwd(); raw != "" && filepath.IsAbs(file) {
		if rel, err := filepath.Rel(raw, file); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	cwd := sulCanonCwd()
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
