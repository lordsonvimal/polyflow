package linker

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// ResolveRubyPolymorphicPathSites is Tier L.2: the per-call-site companion to
// ResolveRubyHTTPHosts for a Ruby http_client whose *host* it already resolved
// (Meta["host_env_var"] is set) but whose *path* it could not, because the one
// AST location is a polymorphic sink — a private `execute`/`request` helper
// reached from several public entry methods, each forwarding a different
// endpoint constant through keyword args and an ActiveSupport `delegate`.
//
// orion-vega-agent's NordicImportWatcher is the motivating shape:
//
//	Connection#execute(method, url, …)          # <- the shared key_dynamic node
//	  ← get(path:)            ← verify:            get(path: VERIFY_ORGANIZATION)
//	  ← patch_or_post(path:)  ← patch(path:) ← (delegate, Uploader) update_file_information: patch(path: COMPLETE_IMPORT_PATH)
//	                          ← post(path:)  ← (delegate, Uploader) presigned_url:          post(path: PRE_SIGNED_POST_PATH)
//
// A single node cannot carry three paths, so ResolveRubyHTTPHosts correctly
// leaves it key_dynamic. This pass walks the keyword-arg + delegate caller
// chain backwards from the sink's URL parameter, collects every *string
// literal or same-file string constant* that reaches it, and mints one
// http_client node per (entry method, endpoint) pair — attributed to the entry
// method with a `calls` edge, carrying the sink's resolved host_env_var and the
// endpoint as its path, key_dynamic cleared. Anything that is not a literal or
// a resolvable string constant (a local var, an interpolation, a method call)
// abstains: no node is minted for that branch. The original shared node is
// never touched — it keeps its honest key_dynamic ledger entry.
//
// Bounded, non-symbolic, same rules as ruby_wrapper_url_forward.go: depth 6,
// one caller hop per level, no control-flow modelling. Runs after
// ruby_http_hosts (needs host_env_var) and before the contract engine.
func ResolveRubyPolymorphicPathSites(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	type trigger struct {
		idx     int
		file    string
		line    int
		hostEnv string
	}
	bySvc := map[string][]trigger{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient || n.Language != "ruby" {
			continue
		}
		if n.Meta["key_dynamic"] != "true" || n.Meta["host_env_var"] == "" {
			continue
		}
		bySvc[n.Service] = append(bySvc[n.Service], trigger{i, n.File, n.Line, n.Meta["host_env_var"]})
	}
	if len(bySvc) == 0 {
		return nil, nil
	}

	// Enclosing function ranges for calls-edge attribution (mirrors RW.2).
	type fnRange struct {
		id            string
		line, endLine int
	}
	fnRangesByFile := map[string][]fnRange{}
	for i := range nodes {
		n := &nodes[i]
		if (n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod) && n.EndLine > 0 {
			fnRangesByFile[n.File] = append(fnRangesByFile[n.File], fnRange{n.ID, n.Line, n.EndLine})
		}
	}
	enclosingFunc := func(file string, line int) string {
		best, bestSpan := "", -1
		for _, r := range fnRangesByFile[file] {
			if line < r.line || line > r.endLine {
				continue
			}
			if span := r.endLine - r.line; bestSpan == -1 || span < bestSpan {
				best, bestSpan = r.id, span
			}
		}
		return best
	}

	svcNames := make([]string, 0, len(bySvc))
	for s := range bySvc {
		svcNames = append(svcNames, s)
	}
	sort.Strings(svcNames)

	var newNodes []graph.Node
	var newEdges []graph.Edge
	seenNode := map[string]bool{}

	for _, svc := range svcNames {
		files := filterRubyFiles(serviceFiles[svc])
		sort.Strings(files)
		if len(files) == 0 {
			continue
		}
		reg := buildRubyHostRegistry(files)

		fas := mapParallel(files, parseRubyFileAST)
		pathOf := map[*rubyFileAST]string{}
		byPath := map[string]*rubyFileAST{}
		delegators := map[string][]*rubyFileAST{}
		strConsts := map[*rubyFileAST]map[string]string{}
		for i, fa := range fas {
			if fa == nil {
				continue
			}
			pathOf[fa] = files[i]
			// serviceFiles paths and node.File may differ in absolute-vs-relative
			// form; normalize both through RelativizeToCwd so the trigger node
			// finds its AST.
			byPath[patterns.RelativizeToCwd(files[i])] = fa
			strConsts[fa] = fa.stringConsts()
			for _, name := range fa.delegatedNames() {
				delegators[name] = append(delegators[name], fa)
			}
		}
		defer func(fas []*rubyFileAST) {
			for _, fa := range fas {
				if fa != nil && fa.release != nil {
					fa.release()
				}
			}
		}(fas)

		ctx := &polyPathCtx{
			reg:        reg,
			delegators: delegators,
			strConsts:  strConsts,
		}

		for _, tg := range bySvc[svc] {
			fa := byPath[patterns.RelativizeToCwd(tg.file)]
			if fa == nil {
				continue
			}
			sinkM := fa.enclosingMethod(tg.line)
			if sinkM == nil {
				continue
			}
			urlParam := fa.sinkURLParam(tg.line, sinkM)
			if urlParam == "" {
				continue
			}
			ctx.out = ctx.out[:0]
			ctx.seen = map[string]bool{}
			ctx.trace(fa, sinkM, urlParam, "", 0)

			dedup := map[string]bool{}
			for _, leaf := range ctx.out {
				endpoint := rubyClientPath(leaf.literal)
				if endpoint == "" {
					continue
				}
				relFile := patterns.RelativizeToCwd(pathOf[leaf.fa])
				entry := leaf.fa.enclosingMethod(leaf.line)
				entryName := "?"
				if entry != nil {
					entryName = entry.name
				}
				key := relFile + "\x00" + entryName + "\x00" + endpoint
				if dedup[key] {
					continue
				}
				dedup[key] = true

				id := fmt.Sprintf("%s:%s:http_client:ruby_poly_path_site:%s:%d", svc, relFile, entryName, leaf.line)
				if seenNode[id] {
					continue
				}
				seenNode[id] = true

				meta := map[string]string{
					"pattern":           "ruby_poly_path_site",
					"path":              endpoint,
					"path_resolved_via": "ruby_delegate_path_trace",
					"host_env_var":      tg.hostEnv,
					"host_resolved_via": "ruby_env_method",
					"key_dynamic_raw":   `ENV.fetch("` + tg.hostEnv + `")`,
				}
				if leaf.verb != "" {
					meta["method"] = leaf.verb
				}
				n := graph.Node{
					ID:       id,
					Type:     graph.NodeTypeHTTPClient,
					Label:    strings.TrimSpace(leaf.verb + " " + endpoint),
					Service:  svc,
					File:     relFile,
					Line:     leaf.line,
					Language: "ruby",
					Meta:     meta,
				}
				newNodes = append(newNodes, n)
				if fn := enclosingFunc(relFile, leaf.line); fn != "" {
					newEdges = append(newEdges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:ruby_poly_path_site", fn, n.ID),
						From:       fn,
						To:         n.ID,
						Type:       graph.EdgeTypeCalls,
						Confidence: graph.ConfidenceInferred,
						Meta:       map[string]string{"via": "ruby_poly_path_site"},
					})
				}
			}
		}
	}
	return newNodes, newEdges
}

type polyPathLeaf struct {
	fa      *rubyFileAST
	line    int
	literal string
	verb    string
}

type polyPathCtx struct {
	reg        map[string]rubyHostInfo
	delegators map[string][]*rubyFileAST
	strConsts  map[*rubyFileAST]map[string]string
	out        []polyPathLeaf
	seen       map[string]bool
}

var httpVerbNames = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH",
	"delete": "DELETE", "head": "HEAD", "options": "OPTIONS",
}

// trace walks callers of method m that pass paramName, resolving each caller's
// argument for that parameter into a literal endpoint (appending to ctx.out) or
// recursing one hop further.
func (ctx *polyPathCtx) trace(fa *rubyFileAST, m *rubyMethodInfo, paramName, verb string, depth int) {
	if depth > 6 || m == nil {
		return
	}
	key := fmt.Sprintf("%p\x00%s\x00%s", fa, m.name, paramName)
	if ctx.seen[key] {
		return
	}
	ctx.seen[key] = true

	isKw, idx := rubyClassifyOwnParam(m.node, paramName, fa.src)
	if !isKw && idx < 0 {
		return // param not on this method's own signature — abstain
	}
	if verb == "" {
		if v, ok := httpVerbNames[m.name]; ok {
			verb = v
		}
	}

	callerFAs := append([]*rubyFileAST{fa}, ctx.delegators[m.name]...)
	for _, cfa := range callerFAs {
		for _, call := range cfa.bareCallsTo(m.name) {
			args := call.ChildByFieldName("arguments")
			if args == nil {
				continue
			}
			arg := rubyCallArgAt(args, idx, isKw, paramName, cfa.src)
			if arg == nil {
				continue
			}
			callLine := int(call.StartPoint().Row) + 1
			cm := cfa.enclosingMethod(callLine)
			if cfa == fa && cm == m {
				continue
			}
			ctx.resolveArg(cfa, cm, arg, callLine, verb, depth+1)
		}
	}
}

// resolveArg turns one call-site argument node into a literal endpoint or a
// further trace, in the context of the method cm that contains it.
func (ctx *polyPathCtx) resolveArg(cfa *rubyFileAST, cm *rubyMethodInfo, e *sitter.Node, callLine int, verb string, depth int) {
	if e == nil || depth > 6 {
		return
	}
	switch e.Type() {
	case "string":
		if s, ok := rubyPlainString(e, cfa.src); ok {
			ctx.out = append(ctx.out, polyPathLeaf{cfa, callLine, s, verb})
		}
	case "constant", "scoped_identifier", "scope_resolution":
		name := e.Content(cfa.src)
		if i := strings.LastIndex(name, "::"); i >= 0 {
			name = name[i+2:]
		}
		if v, ok := ctx.strConsts[cfa][name]; ok {
			ctx.out = append(ctx.out, polyPathLeaf{cfa, callLine, v, verb})
		}
	case "identifier":
		if cm == nil {
			return
		}
		name := e.Content(cfa.src)
		if isKw, i := rubyClassifyOwnParam(cm.node, name, cfa.src); isKw || i >= 0 {
			ctx.trace(cfa, cm, name, verb, depth+1)
			return
		}
		if rhs := cfa.assignmentRHSNode(cm.node, name); rhs != nil {
			ctx.resolveArg(cfa, cm, rhs, callLine, verb, depth+1)
		}
	case "call", "method_call":
		mn := finalMethodName(strings.TrimSpace(e.Content(cfa.src)))
		if mn == "" {
			return
		}
		if _, ok := ctx.reg[mn]; !ok {
			return // not a host method — can't see through it
		}
		if a0 := ctx.firstPositionalArg(e, cfa.src); a0 != nil {
			ctx.resolveArg(cfa, cm, a0, callLine, verb, depth+1)
		}
	}
}

func (ctx *polyPathCtx) firstPositionalArg(call *sitter.Node, src []byte) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	return rubyCallArgAt(args, 0, false, "", src)
}

// sinkURLParam finds the RestClient Request.execute/new call on line and
// returns the name of the sink method's own parameter passed as its `url:`,
// or "" when the url argument is not a bare parameter reference.
func (fa *rubyFileAST) sinkURLParam(line int, m *rubyMethodInfo) string {
	var found string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found != "" {
			return
		}
		if (n.Type() == "call" || n.Type() == "method_call") && int(n.StartPoint().Row)+1 == line {
			if mn := n.ChildByFieldName("method"); mn != nil {
				switch mn.Content(fa.src) {
				case "execute", "new":
					if args := n.ChildByFieldName("arguments"); args != nil {
						if v := rubyCallArgAt(args, -1, true, "url", fa.src); v != nil && v.Type() == "identifier" {
							if k, i := rubyClassifyOwnParam(m.node, v.Content(fa.src), fa.src); k || i >= 0 {
								found = v.Content(fa.src)
								return
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(m.node)
	return found
}

// stringConsts maps a same-file `CONST = "literal"` (optionally `.freeze`d) to
// its value. A constant assigned a non-string, or assigned more than once, is
// omitted rather than guessed.
func (fa *rubyFileAST) stringConsts() map[string]string {
	out := map[string]string{}
	bad := map[string]bool{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "constant" {
				name := left.Content(fa.src)
				r := right
				if r.Type() == "call" {
					if mn := r.ChildByFieldName("method"); mn != nil && mn.Content(fa.src) == "freeze" {
						if rc := r.ChildByFieldName("receiver"); rc != nil {
							r = rc
						}
					}
				}
				if s, ok := rubyPlainString(r, fa.src); ok && !bad[name] {
					if _, dup := out[name]; dup {
						delete(out, name)
						bad[name] = true
					} else {
						out[name] = s
					}
				} else {
					delete(out, name)
					bad[name] = true
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
	return out
}

// delegatedNames returns every method name this file forwards with a bare
// `delegate :a, :b, …, to: :target` (requires the `to:` pair).
func (fa *rubyFileAST) delegatedNames() []string {
	var out []string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" || n.Type() == "command" {
			if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(fa.src) == "delegate" {
				if args := n.ChildByFieldName("arguments"); args != nil {
					hasTo := false
					var names []string
					for i := 0; i < int(args.NamedChildCount()); i++ {
						c := args.NamedChild(i)
						if c.Type() == "pair" {
							if k := c.ChildByFieldName("key"); k != nil &&
								strings.TrimSuffix(k.Content(fa.src), ":") == "to" {
								hasTo = true
							}
							continue
						}
						if s := rubySymbolNodeName(c, fa.src); s != "" {
							names = append(names, s)
						}
					}
					if hasTo {
						out = append(out, names...)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(fa.root)
	return out
}

// assignmentRHSNode is assignmentRHS's node-returning twin.
func (fa *rubyFileAST) assignmentRHSNode(method *sitter.Node, id string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		if n != method && (n.Type() == "method" || n.Type() == "singleton_method") {
			return
		}
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "identifier" && left.Content(fa.src) == id {
				found = right
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(method)
	return found
}

// rubyPlainString returns a string node's content when it has no interpolation,
// else ("", false).
func rubyPlainString(n *sitter.Node, src []byte) (string, bool) {
	if n == nil || n.Type() != "string" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case `"`, "'", "`":
			continue
		case "interpolation", "escape_sequence":
			return "", false
		default:
			b.WriteString(c.Content(src))
		}
	}
	return b.String(), true
}
