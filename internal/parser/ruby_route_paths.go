package parser

import (
	"context"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// composeRailsRoutePaths walks routes.rb's tree-sitter AST maintaining a
// path-prefix stack through namespace/resources/resource nesting, and
// rewrites every http_verb_route / member_verb_route / collection_verb_route
// node's Meta["path"] (adding it where absent) with the full composed
// absolute path. Runs only on files named routes.rb (or under
// config/routes/, Rails' routes concerns convention) — a cheap filename gate
// avoids the extra parse on every other Ruby file (mirrors
// resolveRubyQueueKeys' needsWork early-out). See
// docs/rails-route-path-composition-plan.md (Tier R).
func composeRailsRoutePaths(file string, src []byte, nodes []graph.Node) {
	if !isRailsRoutesFile(file) || len(nodes) == 0 {
		return
	}

	byLine := map[int]*graph.Node{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		switch n.Meta["pattern"] {
		case "http_verb_route", "member_verb_route", "collection_verb_route",
			"member_verb_route_inline", "collection_verb_route_inline":
			byLine[n.Line] = n
		}
	}
	if len(byLine) == 0 {
		return
	}

	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return
	}
	defer tree.Close()

	walkRoutes(tree.RootNode(), src, nil, byLine)
}

// isRailsRoutesFile is the cheap filename gate: config/routes.rb itself, or
// any file under config/routes/ (Rails' routes-concerns convention).
func isRailsRoutesFile(file string) bool {
	base := filepath.Base(file)
	if base == "routes.rb" {
		return true
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	return strings.HasSuffix(dir, "/config/routes") || strings.Contains(dir, "/config/routes/")
}

// walkRoutes recurses over call nodes, threading a path-segment prefix stack
// through namespace/resources/resource/member/collection nesting. Verb calls
// (get/post/put/patch/delete) are stamped via composeAndStamp against the
// pre-built byLine index.
func walkRoutes(n *sitter.Node, src []byte, prefix []string, byLine map[int]*graph.Node) {
	if n == nil {
		return
	}
	if n.Type() != "call" {
		for i := 0; i < int(n.ChildCount()); i++ {
			walkRoutes(n.Child(i), src, prefix, byLine)
		}
		return
	}
	methodNode := n.ChildByFieldName("method")
	if methodNode == nil {
		return
	}
	method := string(src[methodNode.StartByte():methodNode.EndByte()])
	blockNode := n.ChildByFieldName("block")

	switch method {
	case "namespace", "resources", "resource":
		seg, ok := firstPositionalSegment(n, src)
		if ok && blockNode != nil {
			walkRoutes(blockBody(blockNode), src, append(append([]string{}, prefix...), seg), byLine)
		}
		return
	case "member":
		if blockNode != nil {
			walkRoutes(blockBody(blockNode), src, append(append([]string{}, prefix...), ":id"), byLine)
		}
		return
	case "collection":
		if blockNode != nil {
			walkRoutes(blockBody(blockNode), src, prefix, byLine)
		}
		return
	case "get", "post", "put", "patch", "delete":
		composeAndStamp(n, src, prefix, byLine)
		return
	}
	if blockNode != nil {
		walkRoutes(blockBody(blockNode), src, prefix, byLine)
	}
}

// blockBody returns a do_block's body_statement node (the container of its
// nested calls), or the block node itself as a fallback if the grammar shape
// differs.
func blockBody(block *sitter.Node) *sitter.Node {
	if body := block.ChildByFieldName("body"); body != nil {
		return body
	}
	return block
}

// firstPositionalSegment extracts the first positional argument of a
// namespace/resources/resource call as a bare path segment: a leading `:`
// (simple_symbol) or surrounding quotes (string) are stripped.
func firstPositionalSegment(call *sitter.Node, src []byte) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return "", false
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil || !c.IsNamed() {
			continue
		}
		switch c.Type() {
		case "simple_symbol":
			return strings.TrimPrefix(string(src[c.StartByte():c.EndByte()]), ":"), true
		case "string":
			if lit, ok := rubyConcreteString(c, src); ok {
				return strings.Trim(lit, "/"), true
			}
			return "", false
		default:
			return "", false
		}
	}
	return "", false
}

// composeAndStamp builds the full absolute path for a verb call (get/post/…)
// and stamps it onto the correlated node in byLine, keyed by the verb call's
// own line (matcher.go's r.Line for member_verb_route/collection_verb_route
// is the innermost named capture's line — the verb call itself — not the
// enclosing member/collection block's line).
func composeAndStamp(call *sitter.Node, src []byte, prefix []string, byLine map[int]*graph.Node) {
	line := int(call.StartPoint().Row) + 1
	node, ok := byLine[line]
	if !ok {
		return
	}

	segs := append([]string{}, prefix...)

	switch node.Meta["pattern"] {
	case "member_verb_route", "collection_verb_route":
		// Path segment comes from the already-matched action capture, not a
		// re-parse of the call's own arguments — Rails derives the URL
		// segment from the symbol by convention. The :id segment for
		// member_verb_route is already in prefix (walkRoutes' "member" case
		// pushes it before recursing into the block body) — do not add it
		// again here.
		action := strings.TrimPrefix(node.Meta["action"], ":")
		if action == "" {
			return
		}
		segs = append(segs, action)
	case "member_verb_route_inline", "collection_verb_route_inline":
		// The bare `get :action, on: :member`/`on: :collection` form — no
		// wrapping member/collection call ever ran to push :id into prefix,
		// so member_verb_route_inline must add it here itself.
		action := strings.TrimPrefix(node.Meta["action"], ":")
		if action == "" {
			return
		}
		if node.Meta["pattern"] == "member_verb_route_inline" {
			segs = append(segs, ":id")
		}
		segs = append(segs, action)
	case "http_verb_route":
		// String-literal path — same member :id insertion applies when
		// `on: :member` is present as a keyword argument.
		lit := strings.Trim(node.Meta["path"], `"'`)
		lit = strings.Trim(lit, "/")
		if onMember(call, src) {
			segs = append(segs, ":id")
		}
		if lit != "" {
			segs = append(segs, lit)
		}
	default:
		return
	}

	node.Meta["path"] = "/" + strings.Join(segs, "/")

	// member_verb_route/collection_verb_route (and their inline siblings)
	// capture the HTTP verb into Meta["verb"], not Meta["method"] — a
	// pre-existing pattern-authoring inconsistency (http_verb_route's query
	// captures @method directly). The contract engine's join key reads
	// Meta["method"] on both producer and consumer sides
	// (internal/contract/engine.go, contracts/http.yaml `key: [method, path]`),
	// so a route stamped with a path but no method still never matches.
	if node.Meta["method"] == "" && node.Meta["verb"] != "" {
		node.Meta["method"] = strings.ToUpper(node.Meta["verb"])
	}
}

// onMember reports whether a verb call carries an `on: :member` keyword
// argument (the bare-string-literal form: get "literal", on: :member).
func onMember(call *sitter.Node, src []byte) bool {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return false
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil || c.Type() != "pair" {
			continue
		}
		key := c.ChildByFieldName("key")
		value := c.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		keyStr := string(src[key.StartByte():key.EndByte()])
		valStr := string(src[value.StartByte():value.EndByte()])
		if strings.TrimSuffix(keyStr, ":") == "on" && strings.TrimPrefix(valStr, ":") == "member" {
			return true
		}
	}
	return false
}
