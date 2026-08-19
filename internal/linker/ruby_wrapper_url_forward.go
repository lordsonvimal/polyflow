package linker

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// ResolveRubyWrapperURLCallSites is RW.2: the per-call-site companion to
// patterns/ruby/wrapper_url_target.yaml's Level-1 wrapper-body detection
// (`wrapper_url_key_hash_index_ruby`).
//
// A private helper like
//
//	def rest_request(method, payload, headers: {})
//	  RestClient::Request.execute(..., url: payload[:url])
//	end
//
// is called from many places with a different `payload` each time
// (nextGen's data_server_communicator.rb calls its `request_to_agent`/
// `rest_request` pair from 17 distinct methods). The single AST location
// inside `rest_request` cannot correctly represent all 17 call sites at
// once — internal/contract/engine.go:50 also drops any `key_dynamic=true`
// producer to a ledger entry instead of an edge, so the existing
// pattern-emitted node for that one location produces zero edges today,
// not merely an imprecise one.
//
// This pass re-parses every file containing a Level-1 wrapper fact and:
//
//  1. Discovers plain relay wrappers transitively — a function that
//     forwards one of its own parameters, unmodified, straight into an
//     already-known wrapper's matching parameter (`request_to_agent`
//     forwards its own `payload:` into `rest_request`'s `payload`) — the
//     same bounded, non-descending-into-nested-defs technique
//     discoverJSTransitiveWrappers (js_wrapper_calls.go) uses for JS.
//  2. Finds every call site of every known wrapper name in the file and
//     mints one http_client node per site, attributed to its own enclosing
//     method with a `calls` edge — never rewriting the wrapper's own
//     shared node, which would be right for one caller and silently wrong
//     for the other 16.
//  3. Resolves each call site's own URL-bearing argument with one bounded
//     hop of local tracing (string/interpolated literal directly; a local
//     var's `<recv>.merge(<url_key>: <expr>)` assignment one level deeper).
//     Anything past that — another wrapper call, a conditional, a bare
//     parameter with no local assignment — abstains: the node still gets
//     minted with Meta["key_dynamic"]="true" and Meta["key_dynamic_raw"]
//     set, exactly the shape ResolveRubyHTTPHosts (ruby_http_hosts.go)
//     already knows how to pick up and resolve further via its host-method
//     registry, so this pass must run before it. A silently guessed URL is
//     never emitted; not chasing further is a deliberate scope line, not a
//     shortcut — full symbolic execution of Ruby method bodies is an
//     explicit non-goal (docs/live-fleet-gap-audit-plan.md, Tier RW).
func ResolveRubyWrapperURLCallSites(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	type level1Fact struct {
		WrapperName string
		ParamName   string
		URLKey      string
	}
	factsByFile := map[string][]level1Fact{}
	svcByFile := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.Meta["pattern"] != "wrapper_url_key_hash_index_ruby" {
			continue
		}
		wname := n.Meta["wrapper_name"]
		pname := n.Meta["param_name"]
		ukey := n.Meta["url_key"]
		if wname == "" || pname == "" || ukey == "" {
			continue
		}
		factsByFile[n.File] = append(factsByFile[n.File], level1Fact{WrapperName: wname, ParamName: pname, URLKey: ukey})
		svcByFile[n.File] = n.Service
	}
	if len(factsByFile) == 0 {
		return nil, nil
	}

	// Enclosing function/method line ranges, for attributing each new
	// http_client node to its containing method with a calls edge — the same
	// line-range containment technique LinkJSAPIWrapperCalls uses.
	type fnRange struct {
		id            string
		line, endLine int
	}
	fnRangesByFile := map[string][]fnRange{}
	for i := range nodes {
		n := &nodes[i]
		if (n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod) && n.EndLine > 0 {
			fnRangesByFile[n.File] = append(fnRangesByFile[n.File], fnRange{id: n.ID, line: n.Line, endLine: n.EndLine})
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

	var newNodes []graph.Node
	var newEdges []graph.Edge

	for file, facts := range factsByFile {
		if !strings.HasSuffix(file, ".rb") {
			continue
		}
		fa := parseRubyFileAST(file)
		if fa == nil {
			continue
		}
		svc := svcByFile[file]

		defs := collectRubyMethodDefNodes(fa.root, fa.src)
		if len(defs) == 0 {
			fa.tree.Close()
			continue
		}

		wrapperTable := map[string]rubyWrapperInfo{}
		for _, f := range facts {
			def := defs[f.WrapperName]
			if def == nil {
				continue
			}
			isKw, idx := rubyClassifyOwnParam(def, f.ParamName, fa.src)
			if !isKw && idx < 0 {
				continue // param not found on the wrapper's own definition
			}
			wrapperTable[f.WrapperName] = rubyWrapperInfo{ParamName: f.ParamName, IsKeyword: isKw, PositionalIndex: idx, URLKey: f.URLKey}
		}
		if len(wrapperTable) == 0 {
			fa.tree.Close()
			continue
		}

		// Transitive relay discovery, capped — mirrors LinkJSAPIWrapperCalls'
		// hop loop for the same "wrapper of a wrapper" shape in Ruby's
		// positional/keyword calling convention instead of JS's index-only one.
		for hop := 0; hop < 5; hop++ {
			added := false
			for name, def := range defs {
				if _, exists := wrapperTable[name]; exists {
					continue
				}
				if _, ownParam, ok := rubyForwardedParamCall(def, wrapperTable, fa.src); ok {
					wrapperTable[name] = ownParam
					added = true
				}
			}
			if !added {
				break
			}
		}

		for name, def := range defs {
			if _, isWrapper := wrapperTable[name]; isWrapper {
				// This method's own body is internal relay machinery already
				// captured by the transitive-discovery loop above; its real
				// call sites are attributed to ITS callers, not to itself.
				continue
			}
			ownPositional, ownKeyword := rubyMethodParamNames(def, fa.src)
			ownParams := map[string]bool{}
			for _, p := range ownPositional {
				ownParams[p] = true
			}
			for k := range ownKeyword {
				ownParams[k] = true
			}
			body := def.ChildByFieldName("body")
			if body == nil {
				continue
			}

			for _, call := range rubyFindCallsToWrappers(body, wrapperTable, fa.src) {
				callee := call.ChildByFieldName("method").Content(fa.src)
				wi := wrapperTable[callee]
				args := call.ChildByFieldName("arguments")
				if args == nil {
					continue
				}
				urlArg := rubyCallArgAt(args, wi.PositionalIndex, wi.IsKeyword, wi.ParamName, fa.src)
				if urlArg == nil {
					continue
				}
				line := int(call.StartPoint().Row) + 1
				relFile := patterns.RelativizeToCwd(file)
				id := fmt.Sprintf("%s:%s:http_client:rw2_wrapper_call:%s:%d", svc, relFile, callee, line)

				meta := map[string]string{
					"pattern": "rw2_wrapper_call",
					"wrapper": callee,
				}
				if verb := rubyCallSiteMethodVerb(args, fa.src); verb != "" {
					meta["method"] = verb
				}
				if url, ok := resolveRubyWrapperURLExpr(body, ownParams, urlArg, wi.URLKey, fa.src, 0); ok {
					meta["url"] = url
				} else {
					meta["key_dynamic"] = "true"
					meta["key_dynamic_raw"] = strings.TrimSpace(urlArg.Content(fa.src))
				}

				n := graph.Node{
					ID:       id,
					Type:     graph.NodeTypeHTTPClient,
					Label:    strings.TrimSpace(meta["method"] + " " + firstNonEmpty(meta["url"], meta["key_dynamic_raw"])),
					Service:  svc,
					File:     relFile,
					Line:     line,
					Language: "ruby",
					Meta:     meta,
				}
				newNodes = append(newNodes, n)
				if fn := enclosingFunc(relFile, line); fn != "" {
					newEdges = append(newEdges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:rw2_wrapper_call", fn, n.ID),
						From:       fn,
						To:         n.ID,
						Type:       graph.EdgeTypeCalls,
						Confidence: graph.ConfidenceInferred,
						Meta:       map[string]string{"via": "rw2_wrapper_call_site"},
					})
				}
			}
		}
		fa.tree.Close()
	}
	return newNodes, newEdges
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

type rubyWrapperInfo struct {
	ParamName       string
	IsKeyword       bool
	PositionalIndex int // valid only when !IsKeyword
	URLKey          string
}

// collectRubyMethodDefNodes maps every top-level-or-nested method/
// singleton_method name in file to its own *sitter.Node. A same-named
// method defined twice (e.g. reopened in a different class body) keeps the
// last one found — same acceptable last-wins tradeoff
// ruby_http_hosts.go's collectMethods already makes for host-method lookup.
func collectRubyMethodDefNodes(root *sitter.Node, src []byte) map[string]*sitter.Node {
	out := map[string]*sitter.Node{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			if name := n.ChildByFieldName("name"); name != nil {
				out[name.Content(src)] = n
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// rubyMethodParamNames splits def's own parameter list into positional
// names (in order; splat/block params contribute a "" placeholder to keep
// indices aligned) and keyword names.
func rubyMethodParamNames(def *sitter.Node, src []byte) (positional []string, keyword map[string]bool) {
	keyword = map[string]bool{}
	params := def.ChildByFieldName("parameters")
	if params == nil {
		return nil, keyword
	}
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		switch p.Type() {
		case "identifier":
			positional = append(positional, p.Content(src))
		case "optional_parameter":
			if name := p.ChildByFieldName("name"); name != nil {
				positional = append(positional, name.Content(src))
			} else {
				positional = append(positional, "")
			}
		case "keyword_parameter":
			if p.NamedChildCount() > 0 {
				keyword[p.NamedChild(0).Content(src)] = true
			}
		default:
			positional = append(positional, "")
		}
	}
	return positional, keyword
}

// rubyClassifyOwnParam reports whether paramName is one of def's own
// keyword params, or its ordinal index among def's own positional params
// (-1 if not found at all).
func rubyClassifyOwnParam(def *sitter.Node, paramName string, src []byte) (isKeyword bool, positionalIndex int) {
	positional, kw := rubyMethodParamNames(def, src)
	if kw[paramName] {
		return true, -1
	}
	for i, p := range positional {
		if p == paramName {
			return false, i
		}
	}
	return false, -1
}

// rubyForwardedParamCall scans def's body (not descending into a nested
// method/singleton_method — a nested def's own forwarding says nothing
// about the outer one) for a call to an already-known wrapper whose
// argument at that wrapper's own registered parameter slot is itself one
// of def's own parameters, passed through unmodified. Mirrors
// jsForwardedParamCall (js_wrapper_calls.go) for Ruby's positional+keyword
// calling convention.
func rubyForwardedParamCall(def *sitter.Node, wrapperTable map[string]rubyWrapperInfo, src []byte) (calledWrapper string, ownParam rubyWrapperInfo, ok bool) {
	body := def.ChildByFieldName("body")
	if body == nil {
		return "", rubyWrapperInfo{}, false
	}
	positional, kw := rubyMethodParamNames(def, src)

	var found bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			return
		}
		if n.Type() == "call" {
			if methodField := n.ChildByFieldName("method"); methodField != nil {
				callee := methodField.Content(src)
				if wi, known := wrapperTable[callee]; known {
					if args := n.ChildByFieldName("arguments"); args != nil {
						arg := rubyCallArgAt(args, wi.PositionalIndex, wi.IsKeyword, wi.ParamName, src)
						if arg != nil && arg.Type() == "identifier" {
							name := arg.Content(src)
							if kw[name] {
								calledWrapper = callee
								ownParam = rubyWrapperInfo{ParamName: name, IsKeyword: true, PositionalIndex: -1, URLKey: wi.URLKey}
								found, ok = true, true
								return
							}
							for i, p := range positional {
								if p == name {
									calledWrapper = callee
									ownParam = rubyWrapperInfo{ParamName: name, IsKeyword: false, PositionalIndex: i, URLKey: wi.URLKey}
									found, ok = true, true
									return
								}
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			if found {
				return
			}
			walk(n.Child(i))
		}
	}
	walk(body)
	return calledWrapper, ownParam, ok
}

// rubyFindCallsToWrappers returns every `call` node inside scope (not
// descending into a nested method/singleton_method) whose callee is a key
// of wrapperTable.
func rubyFindCallsToWrappers(scope *sitter.Node, wrapperTable map[string]rubyWrapperInfo, src []byte) []*sitter.Node {
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "method" || n.Type() == "singleton_method" {
			return
		}
		if n.Type() == "call" {
			if methodField := n.ChildByFieldName("method"); methodField != nil {
				if _, known := wrapperTable[methodField.Content(src)]; known {
					out = append(out, n)
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(scope)
	return out
}

// rubyCallArgAt extracts the argument bound to a wrapper's own parameter at
// a call site: by keyword-pair lookup if the wrapper's own param is a
// keyword param, otherwise by counting only the non-pair (positional)
// arguments in source order.
func rubyCallArgAt(args *sitter.Node, positionalIdx int, isKeyword bool, keywordName string, src []byte) *sitter.Node {
	if isKeyword {
		for i := 0; i < int(args.NamedChildCount()); i++ {
			c := args.NamedChild(i)
			if c.Type() != "pair" {
				continue
			}
			key := c.ChildByFieldName("key")
			if key != nil && key.Content(src) == keywordName {
				return c.ChildByFieldName("value")
			}
		}
		return nil
	}
	if positionalIdx < 0 {
		return nil
	}
	idx := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() == "pair" {
			continue
		}
		if idx == positionalIdx {
			return c
		}
		idx++
	}
	return nil
}

// rubyCallSiteMethodVerb looks for a `method:` keyword pair at a wrapper
// call site whose value is a literal symbol (`:get`, `:post`, ...) and
// normalizes it to a bare upper-cased HTTP verb — a much cheaper answer
// than tracing the URL, since it sits at the outermost call site directly
// rather than several forwarding hops down.
func rubyCallSiteMethodVerb(args *sitter.Node, src []byte) string {
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() != "pair" {
			continue
		}
		key := c.ChildByFieldName("key")
		if key == nil || key.Content(src) != "method" {
			continue
		}
		val := c.ChildByFieldName("value")
		if val == nil || val.Type() != "simple_symbol" {
			return ""
		}
		verb := strings.ToUpper(strings.TrimPrefix(val.Content(src), ":"))
		switch verb {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			return verb
		}
		return ""
	}
	return ""
}

// resolveRubyWrapperURLExpr resolves expr — the raw argument bound to a
// wrapper's URL-carrying parameter at one call site — to a URL template,
// bounded to depth 3 hops:
//
//   - string literal / interpolated string: used directly.
//   - a bare identifier that is one of the enclosing method's OWN
//     parameters: unresolvable within this file — abstain.
//   - a bare identifier locally assigned earlier in the same method:
//     recurse into the assignment's right-hand side.
//   - `<recv>.merge(<urlKey>: <inner>)`: recurse into <inner>.
//   - anything else: abstain.
func resolveRubyWrapperURLExpr(methodBody *sitter.Node, ownParams map[string]bool, expr *sitter.Node, urlKey string, src []byte, depth int) (string, bool) {
	if expr == nil || depth > 3 {
		return "", false
	}
	switch expr.Type() {
	case "string":
		txt := expr.Content(src)
		if len(txt) >= 2 {
			return txt[1 : len(txt)-1], true
		}
		return "", false
	case "identifier":
		name := expr.Content(src)
		if ownParams[name] {
			return "", false
		}
		rhs := rubyFindLocalAssignmentRHS(methodBody, name, src)
		if rhs == nil {
			return "", false
		}
		return resolveRubyWrapperURLExpr(methodBody, ownParams, rhs, urlKey, src, depth+1)
	case "call":
		methodField := expr.ChildByFieldName("method")
		if methodField == nil || methodField.Content(src) != "merge" {
			return "", false
		}
		args := expr.ChildByFieldName("arguments")
		if args == nil {
			return "", false
		}
		for i := 0; i < int(args.NamedChildCount()); i++ {
			a := args.NamedChild(i)
			if a.Type() != "pair" {
				continue
			}
			key := a.ChildByFieldName("key")
			if key != nil && key.Content(src) == urlKey {
				return resolveRubyWrapperURLExpr(methodBody, ownParams, a.ChildByFieldName("value"), urlKey, src, depth+1)
			}
		}
		return "", false
	default:
		return "", false
	}
}

// rubyFindLocalAssignmentRHS returns the right-hand side of the last
// `name = <rhs>` assignment found anywhere in body's document order (no
// control-flow awareness — matches this file's other bounded, non-symbolic
// resolvers rather than modeling branches).
func rubyFindLocalAssignmentRHS(body *sitter.Node, name string, src []byte) *sitter.Node {
	var result *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "assignment" {
			if left := n.ChildByFieldName("left"); left != nil && left.Type() == "identifier" && left.Content(src) == name {
				result = n.ChildByFieldName("right")
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(body)
	return result
}
