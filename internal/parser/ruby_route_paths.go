package parser

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
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
// It additionally *synthesizes* the routes Rails generates implicitly, which no
// verb call names and which therefore have no matcher node to stamp: `resources
// :users` alone declares index/create/new/edit/show/update/destroy. Those are the
// bulk of a real REST surface — in nextGen's client_api the entire user, study and
// folder CRUD lives there — so without them a caller's `GET /client_api/v1/users`
// has nothing in the graph to match, however well its own URL resolved (Tier K.1).
// Returns the synthesized nodes for the caller to append.
func composeRailsRoutePaths(file, service string, src []byte, nodes []graph.Node) []graph.Node {
	if !isRailsRoutesFile(file) {
		return nil
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

	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	w := &routeWalker{
		src:     src,
		file:    file,
		service: service,
		byLine:  byLine,
		seen:    map[string]bool{},
	}
	w.walk(tree.RootNode(), nil, "")
	return w.out
}

// routeWalker threads the traversal state that walkRoutes used to pass as
// arguments, plus the accumulator for synthesized REST routes.
type routeWalker struct {
	src     []byte
	file    string
	service string
	byLine  map[int]*graph.Node
	out     []graph.Node
	seen    map[string]bool
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

// receiverlessRoutePatterns are the Rails route patterns whose tree-sitter
// query matches a *receiverless* call — `get "x"`, or a verb call inside a
// `member do` / `collection do` block. Inside config/routes.rb that shape is
// unambiguously a route declaration; anywhere else it is ordinary Ruby, since
// calling a private helper named `get` is receiverless too.
var receiverlessRoutePatterns = map[string]bool{
	"http_verb_route":              true,
	"member_verb_route":            true,
	"collection_verb_route":        true,
	"member_verb_route_inline":     true,
	"collection_verb_route_inline": true,
}

// routeScaffoldingPatterns are the Rails route-*group* patterns: they declare a
// path prefix for the routes nested inside them rather than an endpoint. Unlike
// receiverlessRoutePatterns their queries match a call regardless of receiver,
// so HH.1's gate never covered them and they need their own entry here.
//
// They are gated for a different reason than the receiverless family. `namespace
// :db do` in a .rake file is Rake's *task* namespace — a different DSL that
// happens to share a method name, and 85 of the fleet's 100 namespace_route
// nodes were exactly that. More generally, composeRailsRoutePaths is itself
// gated on isRailsRoutesFile, so a scaffolding match outside a routes file can
// never contribute a prefix to anything; gating on the same predicate keeps the
// node set aligned with the pass that consumes it.
var routeScaffoldingPatterns = map[string]bool{
	"resources_route": true,
	"resource_route":  true,
	"namespace_route": true,
}

// dropNonRoutesFileRouteMatches discards receiverless Rails route matches in
// files that are not routes files (Tier HH.1), and route-group scaffolding
// matches in the same files (Tier HH.3).
//
// The canonical offender is nextGen/app/services/cam/user_category_rules_client.rb,
// where `get("#{base_url}/client_api/v1/user_category_rules/#{id}")` calls a
// private helper defined ten lines below, and was indexed as an http_handler
// owned by nextGen — a phantom endpoint in the wrong service, and the only node
// in the fleet whose path kept a raw `#{` (composeRailsRoutePaths is gated on
// the same predicate, so it never ran to reduce it).
//
// This filters MatchResults rather than dropping finished nodes so the gate
// runs *before* MatchToGraph's pass 1b, where an http_handler suppresses any
// http_client at the same file:line. No Ruby client pattern is receiverless
// today (the only other `!receiver` queries are link_to/form_with wrappers, so
// they can never be named `get`), meaning no client is currently recovered by
// ordering it this way — but a post-hoc node drop would silently eat one the
// day a receiverless client pattern is added.
func dropNonRoutesFileRouteMatches(file string, results []patterns.MatchResult) []patterns.MatchResult {
	if isRailsRoutesFile(file) {
		return results
	}
	out := results[:0]
	for _, r := range results {
		if receiverlessRoutePatterns[r.PatternName] || routeScaffoldingPatterns[r.PatternName] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// walk recurses over call nodes, threading a path-segment prefix stack through
// namespace/resources/resource/member/collection nesting. Verb calls
// (get/post/put/patch/delete) are stamped via composeAndStamp against the
// pre-built byLine index; `resources`/`resource` additionally synthesize their
// implicit REST routes.
//
// nestParam carries the enclosing resource's nesting parameter (`:folder_id` for
// `resources :folders`). Rails scopes a resource's *own* actions under a bare
// `/folders` — `get :details_by_path, on: :collection` is `/folders/details_by_path`
// — but scopes anything *declared inside* it under `/folders/:folder_id`. Conflating
// the two dropped the parent's id entirely, so `resources :files` nested in
// `resources :folders` composed as `/folders/files/:id/copy` instead of
// `/folders/:folder_id/files/:id/copy`.
func (w *routeWalker) walk(n *sitter.Node, prefix []string, nestParam string) {
	if n == nil {
		return
	}
	if n.Type() != "call" {
		for i := 0; i < int(n.ChildCount()); i++ {
			w.walk(n.Child(i), prefix, nestParam)
		}
		return
	}
	methodNode := n.ChildByFieldName("method")
	if methodNode == nil {
		return
	}
	method := string(w.src[methodNode.StartByte():methodNode.EndByte()])
	blockNode := n.ChildByFieldName("block")

	// A declaration nested inside a resource sits under that resource's member id.
	base := append([]string{}, prefix...)
	if nestParam != "" {
		base = append(base, nestParam)
	}

	switch method {
	case "namespace":
		seg, ok := firstPositionalSegment(n, w.src)
		if ok && blockNode != nil {
			w.walk(blockBody(blockNode), append(base, seg), "")
		}
		return
	case "resources", "resource":
		seg, ok := firstPositionalSegment(n, w.src)
		if !ok {
			return
		}
		plural := method == "resources"
		scope := append(base, seg)
		w.emitRESTRoutes(n, scope, seg, plural)
		if blockNode != nil {
			w.walk(blockBody(blockNode), scope, nestingParam(seg, plural))
		}
		return
	case "member":
		if blockNode != nil {
			w.walk(blockBody(blockNode), append(append([]string{}, prefix...), ":id"), "")
		}
		return
	case "collection":
		if blockNode != nil {
			w.walk(blockBody(blockNode), prefix, "")
		}
		return
	case "get", "post", "put", "patch", "delete":
		composeAndStamp(n, w.src, prefix, w.byLine)
		return
	}
	if blockNode != nil {
		w.walk(blockBody(blockNode), prefix, nestParam)
	}
}

// restAction describes one of the seven routes Rails derives from `resources`.
type restAction struct {
	name   string
	method string
	member bool   // path carries the member `:id` segment
	suffix string // trailing literal segment (`new`, `edit`), if any
}

// pluralRESTActions is Rails' default route set for `resources :x`, in the order
// `rails routes` prints them. `update` maps to both PATCH and PUT; PATCH is the
// canonical verb and PUT is emitted alongside it so a client using either matches.
var pluralRESTActions = []restAction{
	{name: "index", method: "GET"},
	{name: "create", method: "POST"},
	{name: "new", method: "GET", suffix: "new"},
	{name: "edit", method: "GET", member: true, suffix: "edit"},
	{name: "show", method: "GET", member: true},
	{name: "update", method: "PATCH", member: true},
	{name: "update", method: "PUT", member: true},
	{name: "destroy", method: "DELETE", member: true},
}

// emitRESTRoutes synthesizes the implicit routes for a `resources`/`resource`
// declaration, honouring `only:` and `except:`.
//
// A singular `resource :profile` has no index and no `:id`: there is only ever one
// of it, so show/update/destroy address the collection path directly.
func (w *routeWalker) emitRESTRoutes(call *sitter.Node, scope []string, seg string, plural bool) {
	only, hasOnly, except := restActionFilters(call, w.src)
	if hasOnly && len(only) == 0 {
		// `resources :users, only: []` declares the resource purely as a nesting
		// scope and generates no routes of its own — a real idiom in the fleet
		// (nextGen-CDR-Agent's config/routes.rb:44). Treating an empty list as
		// "no filter" invented all seven.
		return
	}
	line := int(call.StartPoint().Row) + 1

	for _, a := range pluralRESTActions {
		if !plural && a.name == "index" {
			continue
		}
		if hasOnly && !only[a.name] {
			continue
		}
		if except[a.name] {
			continue
		}
		segs := append([]string{}, scope...)
		if a.member && plural {
			segs = append(segs, ":id")
		}
		if a.suffix != "" {
			segs = append(segs, a.suffix)
		}
		path := "/" + strings.Join(segs, "/")

		key := a.method + " " + path
		if w.seen[key] {
			continue
		}
		w.seen[key] = true

		w.out = append(w.out, graph.Node{
			ID:       w.service + ":" + w.file + ":" + string(graph.NodeTypeHTTPHandler) + ":" + key + ":" + strconv.Itoa(line),
			Type:     graph.NodeTypeHTTPHandler,
			Label:    key,
			Service:  w.service,
			File:     w.file,
			Line:     line,
			Language: "ruby",
			Meta: map[string]string{
				"pattern":  "rest_resource_route",
				"path":     path,
				"method":   a.method,
				"action":   a.name,
				"resource": seg,
			},
		})
	}
}

// restActionFilters reads the `only:`/`except:` keyword arguments of a
// resources/resource call. Both accept a single symbol or an array of them
// (`only: :show`, `only: [:index, :show]`, `only: %i[index show]`).
//
// hasOnly distinguishes an absent `only:` from an empty one: `only: []` is a
// deliberate "generate nothing", not "generate everything".
func restActionFilters(call *sitter.Node, src []byte) (only map[string]bool, hasOnly bool, except map[string]bool) {
	only, except = map[string]bool{}, map[string]bool{}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return only, false, except
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil || c.Type() != "pair" {
			continue
		}
		key, value := c.ChildByFieldName("key"), c.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		var target map[string]bool
		switch strings.TrimSuffix(string(src[key.StartByte():key.EndByte()]), ":") {
		case "only":
			target, hasOnly = only, true
		case "except":
			target = except
		default:
			continue
		}
		for _, name := range symbolNames(value, src) {
			target[name] = true
		}
	}
	return only, hasOnly, except
}

// symbolNames collects the action names out of a symbol, an array of symbols, or a
// `%i[...]` symbol-array literal.
func symbolNames(v *sitter.Node, src []byte) []string {
	switch v.Type() {
	case "simple_symbol":
		return []string{strings.TrimPrefix(string(src[v.StartByte():v.EndByte()]), ":")}
	case "bare_symbol":
		return []string{string(src[v.StartByte():v.EndByte()])}
	}
	var out []string
	for i := 0; i < int(v.NamedChildCount()); i++ {
		out = append(out, symbolNames(v.NamedChild(i), src)...)
	}
	return out
}

// nestingParam builds the parameter Rails uses for routes nested inside a
// resource: `resources :folders` scopes its children under `:folder_id`. Singular
// resources contribute no parameter at all.
//
// Matching never depends on the exact name — param_wildcard reduces `:folder_id`
// and the caller's `%d` alike to `*` — but the segment must be there, or every
// nested path comes out one segment short.
func nestingParam(seg string, plural bool) string {
	if !plural {
		return ""
	}
	return ":" + singularize(seg) + "_id"
}

// singularize applies the handful of English inflections Rails' own defaults cover
// for route parameters. It is deliberately not a full inflector: an irregular noun
// yields a cosmetically wrong parameter name in a segment that always normalizes to
// a wildcard anyway.
func singularize(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"),
		strings.HasSuffix(s, "zes"), strings.HasSuffix(s, "ches"),
		strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "us"):
		// Already singular: status, campus, bonus.
		return s
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
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

	// Idempotency guard, same construction as setPath in
	// internal/contract/routegroup.go: a node whose recorded full_path still
	// equals its path has already been composed, and re-composing would treat
	// the composed path as a fresh literal and prepend the prefix a second time
	// ("/api/v1/api/v1/users"). Its Go twin shipped that bug once.
	if fp := node.Meta["full_path"]; fp != "" && fp == node.Meta["path"] {
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

	// http_verb_route captures @method straight from source, so it keeps Ruby's
	// lowercase `get`/`post`. That is not cosmetic: the contract engine's exact
	// tier indexes on the *raw* key, before case folding, and the first tier
	// that hits wins. A CAM route keyed "post /login" therefore misses the
	// exact tier entirely while a Gin route elsewhere in the fleet keyed
	// "POST /login" hits it — the wrong service wins at the highest confidence
	// and the normalized tier, where CAM would have matched, never runs.
	node.Meta["method"] = strings.ToUpper(node.Meta["method"])

	// Record the composed path (the idempotency marker above) and refresh the
	// label, which was minted from the raw capture at node-creation time and
	// still reads "post login" rather than "POST /login" like every other
	// handler in the graph.
	node.Meta["full_path"] = node.Meta["path"]
	if m := node.Meta["method"]; m != "" {
		node.Label = m + " " + node.Meta["path"]
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
