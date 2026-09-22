package parser

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/patterns/scopefold"
)

// railsRouteGrammar is Tier SF Phase 2 (docs/scope-fold-engine-plan.md): the
// path/module/method/action composition slice of Rails' routes.rb grammar,
// expressed as a scopefold.Grammar instead of ruby_route_paths.go's
// hand-written recursion. Deliberately scoped to what's provably correct
// today (parity-tested against the same inline fixtures
// TestComposeRailsRoutePaths_NestedNamespaceAndMemberCollection and
// TestComposeRailsRoutePaths_InlineOnMemberCollection already exercise) —
// route-name synthesis (nameScope), controller:/to:/devise_for overrides,
// root, and the bare-verb-in-resources synthesis stay on
// ruby_route_paths.go's plate until a later increment covers them the same
// way. Not yet wired into composeRailsRoutePaths; buildRailsMatches +
// railsRouteGrammar exist to be Fold-ed and diffed against it, not to
// replace it.
func railsRouteGrammar() *scopefold.Grammar {
	// pending_nest is Rails' single-generation nesting parameter
	// (resources :folders scoping its children under :folder_id): resources
	// contributes it for its direct children, any scope-type child (not
	// member/collection/a verb leaf) absorbs it as the first segment of its
	// own path push, and every scope type — including the one that set it —
	// resets it afterward, so it never reaches a grandchild uninvited. See
	// grammar.go's Resets doc comment.
	pendingNestFirst := scopefold.Contribution{Stack: "pending_nest"}

	return &scopefold.Grammar{
		Stacks: []string{"path", "module", "on_scope", "pending_nest"},
		Scopes: []scopefold.ScopeSpec{
			{
				Match:   "namespace",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path":   {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
					"module": {{Capture: "seg", Extract: "segment"}},
				},
				Resets: []string{"on_scope", "pending_nest"},
			},
			{
				// scope's path comes from an explicit path: keyword, or else
				// the positional argument (scopeSegments' documented
				// precedence); module only from module:.
				Match:   "scope",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {
						Capture: "path_kw", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "pos", Extract: "segment"},
					}},
					"module": {{Capture: "module_kw", Extract: "segment"}},
				},
				Resets: []string{"on_scope", "pending_nest"},
			},
			{
				// resources scopes its own segment under any pending_nest it
				// inherited, then sets a fresh one for its own direct
				// children — module is deliberately NOT pushed here:
				// composeAndStamp's controller_module note is that
				// resources/resource never contribute to it, only
				// namespace/scope do.
				Match:   "resources",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
					"pending_nest": {{
						Capture: "seg", Extract: "segment|inflect:singularize|suffix_id|colon_prefix",
					}},
				},
				Resets: []string{"on_scope", "pending_nest"},
			},
			{
				// A singleton resource has no member id, so it never sets a
				// fresh pending_nest for its children — only absorbs one it
				// inherited, same as resources.
				Match:   "resource",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
				},
				Resets: []string{"on_scope", "pending_nest"},
			},
			{
				Match:   "member",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path":     {{Literal: ":id"}},
					"on_scope": {{Literal: "member"}},
				},
				Resets: []string{"pending_nest"},
			},
			{
				Match:   "collection",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"on_scope": {{Literal: "collection"}},
				},
				Resets: []string{"pending_nest"},
			},
		},
		Leaves: []scopefold.LeafSpec{
			{
				Match: "http_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: []scopefold.EmitArg{
						{Stack: "path", Compose: "join_segments", AppendCapture: "path", AppendExtract: "segment"},
						{Capture: "method", Extract: "upcase"},
					},
				},
			},
			{
				Match: "member_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: []scopefold.EmitArg{
						{Stack: "path", Compose: "join_segments", AppendCapture: "action", AppendExtract: "segment"},
						{Capture: "method", Extract: "upcase"},
					},
				},
			},
			{
				Match: "collection_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: []scopefold.EmitArg{
						{Stack: "path", Compose: "join_segments", AppendCapture: "action", AppendExtract: "segment"},
						{Capture: "method", Extract: "upcase"},
					},
				},
			},
			{
				Match: "member_verb_route_inline",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: []scopefold.EmitArg{
						{Stack: "path", Compose: "join_segments", AppendList: []scopefold.EmitArg{
							{Literal: ":id"},
							{Capture: "action", Extract: "segment"},
						}},
						{Capture: "method", Extract: "upcase"},
					},
				},
			},
			{
				Match: "collection_verb_route_inline",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: []scopefold.EmitArg{
						{Stack: "path", Compose: "join_segments", AppendList: []scopefold.EmitArg{
							{Capture: "action", Extract: "segment"},
						}},
						{Capture: "method", Extract: "upcase"},
					},
				},
			},
		},
	}
}

// railsVerbMethods are the Rails route-verb method names composeRailsRoutePaths's
// own walk switches on.
var railsVerbMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// buildRailsMatches walks a routes.rb AST once and produces the
// []scopefold.Match railsRouteGrammar dispatches on — the boundary adapter
// internal/patterns/scopefold/match.go's own doc comment anticipates: reusing
// ruby_route_paths.go's/ruby_route_names.go's existing keywordSegment/
// firstPositionalSegment helpers to populate captures, never re-implementing
// AST navigation scope-fold-side.
//
// This performs its own small lexical walk (tracking only "are we directly
// inside a member/collection block's own body") purely to classify each verb
// call into the right leaf pattern name — the same classification
// http_verb_route/member_verb_route/collection_verb_route's distinct
// tree-sitter query *shapes* already encode declaratively; a single
// recursive-descent walk is the natural way to answer "what AST shape is
// this call nested in" without matching a fresh query per shape.
func buildRailsMatches(root *sitter.Node, file string, src []byte) []scopefold.Match {
	b := &railsMatchBuilder{file: file, src: src}
	b.walk(root, "")
	return b.out
}

type railsMatchBuilder struct {
	file string
	src  []byte
	out  []scopefold.Match
}

func (b *railsMatchBuilder) walk(n *sitter.Node, onScope string) {
	if n == nil {
		return
	}
	if n.Type() != "call" {
		for i := 0; i < int(n.ChildCount()); i++ {
			b.walk(n.Child(i), onScope)
		}
		return
	}
	methodNode := n.ChildByFieldName("method")
	if methodNode == nil {
		return
	}
	method := string(b.src[methodNode.StartByte():methodNode.EndByte()])
	block := n.ChildByFieldName("block")
	line := int(n.StartPoint().Row) + 1

	switch method {
	case "namespace", "scope", "resources", "resource":
		b.emitScope(n, method, line)
		if block != nil {
			b.walk(blockBody(block), "")
		}
		return
	case "member":
		if block != nil {
			b.emitBare(n, "member", line)
			b.walk(blockBody(block), "member")
		}
		return
	case "collection":
		if block != nil {
			b.emitBare(n, "collection", line)
			b.walk(blockBody(block), "collection")
		}
		return
	}

	if railsVerbMethods[method] {
		b.emitVerb(n, method, line, onScope)
	}
	if block != nil {
		b.walk(blockBody(block), onScope)
	}
}

func (b *railsMatchBuilder) emitScope(call *sitter.Node, method string, line int) {
	seg, _ := firstPositionalSegment(call, b.src)
	caps := map[string]string{"seg": seg}
	if method == "scope" {
		// pos re-derives the same positional segment under a name
		// scopeSegments' own precedence expects: an explicit path: wins,
		// falling back to the positional argument.
		caps["pos"] = seg
		caps["path_kw"] = keywordSegment(call, b.src, "path")
		caps["module_kw"] = keywordSegment(call, b.src, "module")
	}
	b.out = append(b.out, scopefold.Match{PatternName: method, File: b.file, Line: line, Captures: caps})
}

func (b *railsMatchBuilder) emitBare(call *sitter.Node, method string, line int) {
	b.out = append(b.out, scopefold.Match{PatternName: method, File: b.file, Line: line})
}

func (b *railsMatchBuilder) emitVerb(call *sitter.Node, method string, line int, onScope string) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return
	}
	var first *sitter.Node
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c != nil && c.IsNamed() {
			first = c
			break
		}
	}
	if first == nil {
		return
	}

	caps := map[string]string{"method": method}
	on := keywordSegment(call, b.src, "on")

	switch first.Type() {
	case "string":
		caps["path"] = string(b.src[first.StartByte():first.EndByte()])
		b.out = append(b.out, scopefold.Match{PatternName: "http_verb_route", File: b.file, Line: line, Captures: caps})
	case "simple_symbol":
		caps["action"] = string(b.src[first.StartByte():first.EndByte()])
		switch {
		case on == "member":
			b.out = append(b.out, scopefold.Match{PatternName: "member_verb_route_inline", File: b.file, Line: line, Captures: caps})
		case on == "collection":
			b.out = append(b.out, scopefold.Match{PatternName: "collection_verb_route_inline", File: b.file, Line: line, Captures: caps})
		case onScope == "member":
			b.out = append(b.out, scopefold.Match{PatternName: "member_verb_route", File: b.file, Line: line, Captures: caps})
		case onScope == "collection":
			b.out = append(b.out, scopefold.Match{PatternName: "collection_verb_route", File: b.file, Line: line, Captures: caps})
			// A bare `post :action` directly in a resources block (no
			// member/collection wrapper, no on:) is resource_scoped_verb —
			// not yet covered by this grammar; left unmatched rather than
			// misclassified.
		}
	}
}
