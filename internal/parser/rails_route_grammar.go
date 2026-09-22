package parser

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/patterns/scopefold"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// railsRouteGrammar is Tier SF Phase 2 (docs/scope-fold-engine-plan.md): the
// path/module/method/action/route-name/controller/devise/root composition
// slice of Rails' routes.rb grammar, expressed as a scopefold.Grammar
// instead of ruby_route_paths.go's hand-written recursion. Parity-tested
// against the same fixtures ruby_route_paths_test.go and
// ruby_route_names_test.go already exercise where ground truth exists;
// `root` (Tier SF Phase 2e) has none — composeRailsRoutePaths never
// implemented it — so it is instead built directly off Rails' own
// documented semantics (ActionDispatch::Routing::Mapper#root /
// match_root_route: path "/", via: :get, as: :root by default, target from
// a `to:`/positional-string/controller:+action: option). See this file's
// per-construct comments for exactly what each one covers. Not yet wired
// into composeRailsRoutePaths; buildRailsMatches + railsRouteGrammar exist
// to be Fold-ed and diffed against it, not to replace it.
// controllerModuleArg composes the "module" stack (namespace/scope's own
// nesting) plus, when capture is non-"", an extra module segment split out
// of it via extract — the shared shape both resources'/resource's
// controller: override and http_verb_route's to: target use to contribute
// nesting beyond what namespace/scope already pushed ("admin/db_status#index"
// contributes "admin" on top of the enclosing module stack, the same way
// emitDeviseRoutes' controllers: override hash splits a namespaced basename).
// A "" capture is resource_scoped_verb's/member_verb_route's/etc. own case:
// no override exists for these leaf shapes, so it reads the module stack
// alone.
func controllerModuleArg(capture, extract string) scopefold.EmitArg {
	if capture == "" {
		return scopefold.EmitArg{Stack: "module", Compose: "join"}
	}
	return scopefold.EmitArg{
		Stack: "module", Compose: "join",
		AppendList: []scopefold.EmitArg{{Capture: capture, Extract: extract}},
	}
}

func railsRouteGrammar() *scopefold.Grammar {
	// pending_nest is Rails' single-generation URL nesting parameter
	// (resources :folders scoping its children under :folder_id): resources
	// contributes it for its direct children, any scope-type child absorbs
	// it as the first segment of its own path push, and every scope type —
	// including the one that set it — resets it afterward. See grammar.go's
	// Resets doc comment.
	pendingNestFirst := scopefold.Contribution{Stack: "pending_nest"}

	// pendingSingularFirst is nameScope's parallel single-generation value
	// for route *names*: the enclosing resource's singular form, flattened
	// into the "name" stack by whichever scope-type construct is entered
	// next (namespace/scope/resources/resource), and never by member/
	// collection (which read it directly instead — see memberBase/
	// collectionBase's ports below).
	pendingSingularFirst := scopefold.Contribution{Stack: "res_singular"}

	// asOrLiteralName resolves the qualifier verbRouteHelperName's member/
	// collection/default branches all share: an explicit `as:` keyword, or
	// else the route's own literal-derived name — a bare action symbol for
	// member/collection routes ("segment" strips its leading ":"), a literal
	// path for http_verb_route ("path_helper_name" additionally rejects a
	// dynamic segment and underscore-joins a multi-segment literal, per
	// pathHelperName's own rule).
	asOrLiteralName := func(literalFrom, extract string) scopefold.EmitArg {
		return scopefold.EmitArg{
			Capture: "as_kw",
			Fallback: &scopefold.EmitArg{
				Capture: literalFrom, Extract: extract,
			},
		}
	}

	// memberHelper/collectionHelper build qualifyVerbName's shape (action-or-as
	// PREFIXED to the enclosing resource's singular/collection base) for
	// member_verb_route/collection_verb_route and their inline siblings,
	// which all carry an "action" capture.
	memberHelper := scopefold.EmitArg{
		Stack: "name", Compose: "helper_name",
		AppendList: []scopefold.EmitArg{{Stack: "res_singular", Compose: "top"}},
		Prepend:    []scopefold.EmitArg{asOrLiteralName("action", "segment")},
	}
	collectionHelper := scopefold.EmitArg{
		Stack: "name", Compose: "helper_name",
		AppendList: []scopefold.EmitArg{{Stack: "res_collection", Compose: "top"}},
		Prepend:    []scopefold.EmitArg{asOrLiteralName("action", "segment")},
	}

	// unmoduledLeafArgs is the [controller_module, resource, action] tail
	// every verb-leaf shape composeAndStamp does NOT populate from a `to:`
	// target carries: controller_module is still the plain module stack
	// (Rails resolves the controller from the URL/resource elsewhere), but
	// resource/action stay empty — a fact this walker does not have, not an
	// invented one.
	unmoduledLeafArgs := []scopefold.EmitArg{
		controllerModuleArg("", ""),
		{Literal: ""},
		{Literal: ""},
	}

	verbLeafArgs := func(pathArgs, helper scopefold.EmitArg, ctrlTail []scopefold.EmitArg) []scopefold.EmitArg {
		args := []scopefold.EmitArg{pathArgs, {Capture: "method", Extract: "upcase"}, helper}
		args = append(args, ctrlTail...)
		// resource_style: "singular" inside a `resource` block, "" everywhere
		// else — composeAndStamp only ever stamps the singular case, since
		// "plural" is emitRESTRoutes' own statement about routes it minted,
		// not something an explicit verb call claims for itself.
		args = append(args, scopefold.EmitArg{Stack: "resource_style", Compose: "top"})
		// controller_explicit: never set by an explicit verb call in the Go
		// ground truth — only emitRESTRoutes' own controller: reading sets it.
		args = append(args, scopefold.EmitArg{Literal: ""})
		return args
	}

	// toTargetTail is http_verb_route's controller_module/resource/action
	// triple, read off an explicit `to: "controller#action"` target — the
	// one leaf shape composeAndStamp lets decouple the URL from the
	// controller name (an admin/db_status#index target both names an action
	// and contributes extra module nesting beyond the enclosing namespace/
	// scope stack, the same "namespace/basename" split emitDeviseRoutes'
	// controllers: override and resources' controller: override both use).
	toTargetTail := []scopefold.EmitArg{
		controllerModuleArg("to_kw", "before_hash|before_last_slash"),
		{Capture: "to_kw", Extract: "before_hash|after_last_slash"},
		{Capture: "to_kw", Extract: "after_hash"},
	}

	// rootArgs is `root`'s own rails_route Args: path is the enclosing
	// scope's own path with no extra segment appended (match_root_route's
	// literal "/", scoped the same way any other leaf's path stack read
	// is), method is always GET (root never takes a via: override in real
	// Rails), and the helper name is the enclosing scope's own name prefix
	// with "root" appended — never qualified by res_singular/res_collection,
	// since root has no member/collection concept of its own to prefix or
	// suffix. It shares http_verb_route's toTargetTail for the
	// controller_module/resource/action split (root's target is a
	// "controller#action" string exactly the same shape `to:` uses there),
	// and reads resource_style off the stack like any other leaf — a `root`
	// nested inside a singleton `resource` block still inherits "singular".
	rootArgs := []scopefold.EmitArg{
		{Stack: "path", Compose: "join_segments"},
		{Literal: "GET"},
		{
			Stack: "name", Compose: "helper_name",
			AppendList: []scopefold.EmitArg{
				{Capture: "as_kw", Fallback: &scopefold.EmitArg{Literal: "root"}},
			},
		},
	}
	rootArgs = append(rootArgs, toTargetTail...)
	rootArgs = append(rootArgs,
		scopefold.EmitArg{Stack: "resource_style", Compose: "top"},
		scopefold.EmitArg{Literal: ""},
	)

	return &scopefold.Grammar{
		Stacks: []string{"path", "module", "name", "on_scope", "pending_nest", "res_singular", "res_plural", "res_collection", "resource_style"},
		Scopes: []scopefold.ScopeSpec{
			{
				Match:   "namespace",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path":   {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
					"module": {{Capture: "seg", Extract: "segment"}},
					"name":   {pendingSingularFirst, {Capture: "seg", Extract: "segment"}},
				},
				Resets: []string{"on_scope", "pending_nest", "res_singular", "res_plural", "res_collection", "resource_style"},
			},
			{
				// scope's path comes from an explicit path: keyword, or else
				// the positional argument; module only from module:; name
				// only from as: (scopeSegments'/nameScope's own documented
				// precedence — none of the three subsets agree).
				Match:   "scope",
				Recurse: "block",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {
						Capture: "path_kw", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "pos", Extract: "segment"},
					}},
					"module": {{Capture: "module_kw", Extract: "segment"}},
					"name":   {pendingSingularFirst, {Capture: "as_kw", Extract: "segment"}},
				},
				Resets: []string{"on_scope", "pending_nest", "res_singular", "res_plural", "res_collection", "resource_style"},
			},
			{
				// resources scopes its own segment under any pending_nest it
				// inherited, then sets a fresh nesting id AND fresh singular/
				// plural/collection naming values for its own direct
				// children — module is deliberately NOT pushed (resources/
				// resource never contribute to controller_module, only
				// namespace/scope do), and neither is the resource's own
				// name segment (nameScope folds a resource's *own* identity
				// in only at the point something reads it — a child scope's
				// pendingSingularFirst, or this same frame's own Expand/
				// member/collection naming — never by pushing it onto
				// "name" directly, or a plain verb route sitting beside a
				// nested resources block would wrongly inherit it).
				Match:   "resources",
				Recurse: "block",
				Expand:  "rest_actions_plural",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
					"pending_nest": {{
						Capture: "seg", Extract: "segment|inflect:singularize|suffix_id|colon_prefix",
					}},
					"name": {pendingSingularFirst},
					"res_singular": {{
						Capture: "res_as", Extract: "segment|inflect:singularize",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment|inflect:singularize"},
					}},
					"res_plural": {{
						Capture: "res_as", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment"},
					}},
					"res_collection": {{
						Capture: "res_as", Extract: "segment|inflect:collection_name",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment|inflect:collection_name"},
					}},
				},
				Resets: []string{"on_scope", "pending_nest", "res_singular", "res_plural", "res_collection", "resource_style"},
			},
			{
				// A singleton resource has no member id, so it never sets a
				// fresh pending_nest — only absorbs one it inherited, same
				// as resources. Its singular/plural/collection all equal
				// its own declared name (or `as:`) verbatim: no inflection,
				// no `_index` disambiguation — SingletonResource overrides
				// collection_name back to the singular in real Rails.
				Match:   "resource",
				Recurse: "block",
				Expand:  "rest_actions_singular",
				Contributes: map[string][]scopefold.Contribution{
					"path": {pendingNestFirst, {Capture: "seg", Extract: "segment"}},
					"name": {pendingSingularFirst},
					"res_singular": {{
						Capture: "res_as", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment"},
					}},
					"res_plural": {{
						Capture: "res_as", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment"},
					}},
					"res_collection": {{
						Capture: "res_as", Extract: "segment",
						Fallback: &scopefold.Contribution{Capture: "seg", Extract: "segment"},
					}},
					// A singleton is the one construct that marks resource_style
					// for its own verb-route children (composeAndStamp's `if
					// singular` branch) — resources deliberately contributes
					// nothing here, since "plural" is emitRESTRoutes' own
					// statement about routes it minted, not one an explicit verb
					// call inside a `resources` block claims for itself.
					"resource_style": {{Literal: "singular"}},
				},
				Resets: []string{"on_scope", "pending_nest", "res_singular", "res_plural", "res_collection", "resource_style"},
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
			{
				// devise_for never recurses (Go's own walk returns
				// immediately after emitDeviseRoutes, no block body) and
				// contributes nothing to any stack itself — only its own
				// controllers: hash, resolved entirely inside HashExpand.
				Match: "devise_for",
				HashExpand: &scopefold.HashExpandSpec{
					Entries:  "controllers_hash",
					Skip:     "skip_kw",
					ScopeArg: "seg",
					Rows:     deviseHashRows(),
					Emit: scopefold.EmitSpec{
						Pred: "rails_route",
						Args: []scopefold.EmitArg{
							{Row: "path"},
							{Row: "method"},
							// devise_route never carries a route_helper in
							// the Go ground truth (Devise's own path/url
							// helpers are a fixed Rails convention, not
							// something emitDeviseRoutes derives or stamps).
							{Literal: ""},
							scopefold.EmitArg{
								Stack: "module", Compose: "join",
								AppendList: []scopefold.EmitArg{
									// devise_for's own module: keyword, read
									// before any per-entry namespaced-basename
									// nesting — emitDeviseRoutes' own order
									// (scopeMod := mod, then optionally
									// appendSeg(mod, module:), then the
									// per-entry split on top of THAT).
									{Capture: "module_kw"},
									{Row: "entry_value", Extract: "before_last_slash"},
								},
							},
							{Row: "entry_value", Extract: "after_last_slash"},
							{Row: "name"},
							// resource_style/controller_explicit: emitDeviseRoutes
							// never stamps either — CR (resource_style) and the
							// controller: reading (controller_explicit) are both
							// resources'/resource's own concerns, not devise_for's.
							{Literal: ""},
							{Literal: ""},
						},
					},
				},
			},
		},
		Leaves: []scopefold.LeafSpec{
			{
				Match: "http_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendCapture: "path", AppendExtract: "segment"},
						scopefold.EmitArg{Switch: &scopefold.SwitchSpec{
							On: scopefold.EmitArg{
								Capture:  "on_kw",
								Fallback: &scopefold.EmitArg{Stack: "on_scope", Compose: "top"},
							},
							Cases: map[string]scopefold.EmitArg{
								"member": {
									Stack: "name", Compose: "helper_name",
									AppendList: []scopefold.EmitArg{{Stack: "res_singular", Compose: "top"}},
									Prepend:    []scopefold.EmitArg{asOrLiteralName("path", "path_helper_name")},
								},
								"collection": {
									Stack: "name", Compose: "helper_name",
									AppendList: []scopefold.EmitArg{{Stack: "res_collection", Compose: "top"}},
									Prepend:    []scopefold.EmitArg{asOrLiteralName("path", "path_helper_name")},
								},
							},
							Default: &scopefold.EmitArg{
								Stack: "name", Compose: "helper_name",
								AppendList: []scopefold.EmitArg{
									{Stack: "res_singular", Compose: "top"},
									asOrLiteralName("path", "path_helper_name"),
								},
							},
						}},
						toTargetTail,
					),
				},
			},
			{
				Match: "member_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendCapture: "action", AppendExtract: "segment"},
						memberHelper,
						unmoduledLeafArgs,
					),
				},
			},
			{
				Match: "collection_verb_route",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendCapture: "action", AppendExtract: "segment"},
						collectionHelper,
						unmoduledLeafArgs,
					),
				},
			},
			{
				Match: "member_verb_route_inline",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendList: []scopefold.EmitArg{
							{Literal: ":id"},
							{Capture: "action", Extract: "segment"},
						}},
						memberHelper,
						unmoduledLeafArgs,
					),
				},
			},
			{
				Match: "collection_verb_route_inline",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendList: []scopefold.EmitArg{
							{Capture: "action", Extract: "segment"},
						}},
						collectionHelper,
						unmoduledLeafArgs,
					),
				},
			},
			{
				// A bare `post :add_details` directly inside a `resources`
				// block (no member/collection wrapper, no on: keyword) is a
				// member route by Rails convention — same path shape as
				// member_verb_route_inline, but named as base_action
				// (resourceScopedHelperName's SUFFIX order) rather than
				// action_base (qualifyVerbName's PREFIX order used by the
				// on:/lexical forms), so it needs its own leaf rather than
				// reusing member_verb_route_inline's Emit. Its resource/action
				// tail also diverges from every other leaf's: Go's
				// emitResourceScopedVerb reads Meta["resource"] off
				// names.plural (res_plural), not off any controller: override
				// — a bare verb inside `resources`, unlike the block's own
				// implicit CRUD, never honours controller: at all.
				Match: "resource_scoped_verb",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: verbLeafArgs(
						scopefold.EmitArg{Stack: "path", Compose: "join_segments", AppendList: []scopefold.EmitArg{
							{Literal: ":id"},
							{Capture: "action", Extract: "segment"},
						}},
						scopefold.EmitArg{
							Stack: "name", Compose: "helper_name",
							AppendList: []scopefold.EmitArg{
								{Stack: "res_singular", Compose: "top"},
								asOrLiteralName("action", "segment"),
							},
						},
						[]scopefold.EmitArg{
							controllerModuleArg("", ""),
							{Stack: "res_plural", Compose: "top"},
							{Capture: "action", Extract: "segment"},
						},
					),
				},
			},
			{
				Match: "root",
				Emit: scopefold.EmitSpec{
					Pred: "rails_route",
					Args: rootArgs,
				},
			},
		},
		ExpandTables: map[string]scopefold.ExpandTable{
			"rest_actions_plural":   restActionsTable(true),
			"rest_actions_singular": restActionsTable(false),
		},
	}
}

// restActionsTable builds ExpandTables["rest_actions_plural"/"_singular"]:
// the CRUD routes a bare `resources`/`resource` declaration implies with no
// verb call of its own — emitRESTRoutes' Go loop over pluralRESTActions,
// generalized onto scopefold's existing table-driven Expand mechanism (no
// new engine primitive beyond ExpandRow.Emit, which this is the first user
// of: index/create name off the collection, new/edit prefix their own action
// onto the singular, show/update/destroy name off the bare singular — one
// shared Emit cannot express all three shapes, so each row supplies its own).
//
// plural distinguishes `resources` (index exists, :id inserted for member
// rows) from `resource` (no index — a singleton is never listed — and never
// an :id, since there is only ever one).
func restActionsTable(plural bool) scopefold.ExpandTable {
	base := scopefold.EmitArg{Stack: "name", Compose: "helper_name",
		AppendList: []scopefold.EmitArg{{Stack: "res_singular", Compose: "top"}}}
	collectionBase := scopefold.EmitArg{Stack: "name", Compose: "helper_name",
		AppendList: []scopefold.EmitArg{{Stack: "res_collection", Compose: "top"}}}
	prefixed := func(prefix string) scopefold.EmitArg {
		a := base
		a.Prepend = []scopefold.EmitArg{{Literal: prefix}}
		return a
	}
	style := "plural"
	if !plural {
		style = "singular"
	}
	// resource/controller_module read the scope match's own "ctrl_kw"
	// capture (emitRESTRoutes' ctrlSeg/ctrlMod, split the same
	// "namespace/basename" way emitDeviseRoutes' controllers: override and
	// http_verb_route's to: target both split it) — absent controller:, the
	// resource falls back to the declaration's own raw segment, unmodified
	// by any inflection (emitRESTRoutes' seg parameter, not singularName).
	resourceArg := scopefold.EmitArg{
		Capture: "ctrl_kw", Extract: "after_last_slash",
		Fallback: &scopefold.EmitArg{Capture: "seg"},
	}
	emit := func(helper scopefold.EmitArg) *scopefold.EmitSpec {
		return &scopefold.EmitSpec{
			Pred: "rails_route",
			Args: []scopefold.EmitArg{
				{Stack: "path", Compose: "join_segments"},
				{Row: "method"},
				helper,
				controllerModuleArg("ctrl_kw", "before_last_slash"),
				resourceArg,
				{Row: "name"},
				{Literal: style},
				{Capture: "ctrl_kw", Extract: "flag"},
			},
		}
	}

	rows := []scopefold.ExpandRow{}
	if plural {
		rows = append(rows, scopefold.ExpandRow{Name: "index", Method: "GET", Emit: emit(collectionBase)})
	}
	rows = append(rows,
		// create shares index's name (restHelperName's own rule) for both
		// resources and resource — a singleton still has a create action,
		// only never an index to list it.
		scopefold.ExpandRow{Name: "create", Method: "POST", Emit: emit(collectionBase)},
		scopefold.ExpandRow{Name: "new", Method: "GET", Suffix: "new", Emit: emit(prefixed("new"))},
		scopefold.ExpandRow{Name: "edit", Method: "GET", Member: plural, Suffix: "edit", Emit: emit(prefixed("edit"))},
		scopefold.ExpandRow{Name: "show", Method: "GET", Member: plural, Emit: emit(base)},
		scopefold.ExpandRow{Name: "update", Method: "PATCH", Member: plural, Emit: emit(base)},
		scopefold.ExpandRow{Name: "update", Method: "PUT", Member: plural, Emit: emit(base)},
		scopefold.ExpandRow{Name: "destroy", Method: "DELETE", Member: plural, Emit: emit(base)},
	)

	return scopefold.ExpandTable{
		Emit:           *emit(base),
		FilterKeywords: []string{"only", "except"},
		Rows:           rows,
		MemberSegment:  ":id",
	}
}

// deviseHashRows converts railsinflect.DeviseScopeActions — Devise's own
// fixed per-scope action/path table, already shared with DV.2
// (internal/linker/rails_devise.go) — into HashExpandSpec.Rows' shape: one
// fixed Go map, generalized onto grammar data exactly the way
// restActionsTable already generalized pluralRESTActions.
func deviseHashRows() map[string][]scopefold.HashExpandRow {
	out := make(map[string][]scopefold.HashExpandRow, len(railsinflect.DeviseScopeActions))
	for scope, actions := range railsinflect.DeviseScopeActions {
		rows := make([]scopefold.HashExpandRow, len(actions))
		for i, a := range actions {
			rows[i] = scopefold.HashExpandRow{Name: a.Name, Method: a.Method, PathTemplate: a.Path}
		}
		out[scope] = rows
	}
	return out
}

// railsVerbMethods are the Rails route-verb method names composeRailsRoutePaths's
// own walk switches on.
var railsVerbMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// buildRailsMatches walks a routes.rb AST once and produces the
// []scopefold.Match railsRouteGrammar dispatches on, reusing
// ruby_route_paths.go's/ruby_route_names.go's existing keywordSegment/
// firstPositionalSegment helpers to populate captures rather than
// reimplementing AST navigation scope-fold-side.
func buildRailsMatches(root *sitter.Node, file string, src []byte) []scopefold.Match {
	b := &railsMatchBuilder{file: file, src: src}
	b.walk(root, "", "")
	return b.out
}

type railsMatchBuilder struct {
	file string
	src  []byte
	out  []scopefold.Match
}

// walk mirrors routeWalker.walk's dispatch. pendingNest tracks the same
// single-generation value ruby_route_paths.go's nestParam does — non-""
// only for a call sitting directly in a plural resources/resource block's
// own body (every intervening construct resets or clears it before
// recursing) — used here only to classify a bare `post :action` as
// resource_scoped_verb.
func (b *railsMatchBuilder) walk(n *sitter.Node, onScope, pendingNest string) {
	if n == nil {
		return
	}
	if n.Type() != "call" {
		for i := 0; i < int(n.ChildCount()); i++ {
			b.walk(n.Child(i), onScope, pendingNest)
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
			// nextPending marks whether *this* construct's own direct
			// children see a pending member id — only a plural `resources`
			// sets one (nestingParam's plural gate; a singleton has no
			// member id to scope a bare verb under).
			nextPending := ""
			if method == "resources" {
				nextPending = "x"
			}
			b.walk(blockBody(block), "", nextPending)
		}
		return
	case "member":
		if block != nil {
			b.emitBare(n, "member", line)
			b.walk(blockBody(block), "member", "")
		}
		return
	case "collection":
		if block != nil {
			b.emitBare(n, "collection", line)
			b.walk(blockBody(block), "collection", "")
		}
		return
	case "devise_for":
		// Never recurses — Go's own walk returns right after
		// emitDeviseRoutes, no block body to fold into.
		b.emitDeviseFor(n, line)
		return
	case "root":
		// Never recurses — root takes no block in real Rails routes.rb.
		b.emitRoot(n, line)
		return
	}

	if railsVerbMethods[method] {
		b.emitVerb(n, method, line, onScope, pendingNest)
	}
	if block != nil {
		b.walk(blockBody(block), onScope, pendingNest)
	}
}

func (b *railsMatchBuilder) emitScope(call *sitter.Node, method string, line int) {
	seg, _ := firstPositionalSegment(call, b.src)
	caps := map[string]string{"seg": seg}
	switch method {
	case "scope":
		// pos re-derives the same positional segment under a name
		// scopeSegments' own precedence expects: an explicit path: wins,
		// falling back to the positional argument.
		caps["pos"] = seg
		caps["path_kw"] = keywordSegment(call, b.src, "path")
		caps["module_kw"] = keywordSegment(call, b.src, "module")
		caps["as_kw"] = keywordSegment(call, b.src, "as")
	case "resources", "resource":
		caps["res_as"] = keywordSegment(call, b.src, "as")
		caps["ctrl_kw"] = keywordSegment(call, b.src, "controller")
		// only:/except: capture must exist (even as "") whenever the
		// keyword itself is present — restActionsTable's FilterKeywords
		// relies on scopefold's filterSet distinguishing an absent only:
		// from an explicitly empty one (`only: []` means "generate
		// nothing", not "no filter"), which only works if the capture key
		// is set at all. keywordSegment can't be reused here: only:/
		// except: take a bare symbol OR an array/%i[] of them, and
		// keywordSegment's literalSegment only resolves the former —
		// symbolNames (already shared by restActionFilters) flattens
		// either shape into scopefold's comma-separated capture string.
		if v := keywordValueNode(call, b.src, "only"); v != nil {
			caps["only"] = strings.Join(symbolNames(v, b.src), ",")
		}
		if v := keywordValueNode(call, b.src, "except"); v != nil {
			caps["except"] = strings.Join(symbolNames(v, b.src), ",")
		}
	}
	b.out = append(b.out, scopefold.Match{PatternName: method, File: b.file, Line: line, Captures: caps})
}

func (b *railsMatchBuilder) emitBare(call *sitter.Node, method string, line int) {
	b.out = append(b.out, scopefold.Match{PatternName: method, File: b.file, Line: line})
}

// emitDeviseFor flattens a devise_for call's controllers:/skip: hashes into
// the "key1=value1;key2=value2" / "key1,key2" capture shapes
// scopefold.HashExpandSpec's own generic parsing expects — reusing
// ruby_route_paths.go's deviseControllersHash/deviseSkipSet AST walks
// directly rather than re-deriving them, the same "buildRailsMatches owns
// Ruby-specific extraction, scopefold stays AST-agnostic" split every other
// capture in this file already follows.
func (b *railsMatchBuilder) emitDeviseFor(call *sitter.Node, line int) {
	seg, ok := firstPositionalSegment(call, b.src)
	if !ok {
		return
	}
	controllers := deviseControllersHash(call, b.src)
	if len(controllers) == 0 {
		return
	}
	keys := make([]string, 0, len(controllers))
	for k := range controllers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+controllers[k])
	}

	skip := deviseSkipSet(call, b.src)
	skipNames := make([]string, 0, len(skip))
	for s := range skip {
		skipNames = append(skipNames, s)
	}
	sort.Strings(skipNames)

	caps := map[string]string{
		"seg":              seg,
		"controllers_hash": strings.Join(pairs, ";"),
		"skip_kw":          strings.Join(skipNames, ","),
		"module_kw":        keywordSegment(call, b.src, "module"),
	}
	b.out = append(b.out, scopefold.Match{PatternName: "devise_for", File: b.file, Line: line, Captures: caps})
}

// emitRoot resolves root's target the same three ways Rails' own #root
// accepts it: an explicit `to:` keyword, a bare positional string
// ("root 'pages#home'", sugar for to:), or a `controller:`/`action:` pair
// (root's Hash-argument form) — first match wins, mirroring Mapper#root's
// own if/elsif chain. A route with no resolvable target (a bare `root` with
// nothing, which Rails itself would raise ArgumentError on) emits nothing.
func (b *railsMatchBuilder) emitRoot(call *sitter.Node, line int) {
	target := keywordSegment(call, b.src, "to")
	if target == "" {
		if seg, ok := firstPositionalSegment(call, b.src); ok {
			target = seg
		}
	}
	if target == "" {
		ctrl := keywordSegment(call, b.src, "controller")
		action := keywordSegment(call, b.src, "action")
		if ctrl != "" && action != "" {
			target = ctrl + "#" + action
		}
	}
	if target == "" {
		return
	}
	caps := map[string]string{
		"to_kw": target,
		"as_kw": keywordSegment(call, b.src, "as"),
	}
	b.out = append(b.out, scopefold.Match{PatternName: "root", File: b.file, Line: line, Captures: caps})
}

func (b *railsMatchBuilder) emitVerb(call *sitter.Node, method string, line int, onScope, pendingNest string) {
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

	caps := map[string]string{"method": method, "as_kw": keywordSegment(call, b.src, "as")}
	on := keywordSegment(call, b.src, "on")
	caps["on_kw"] = on

	switch first.Type() {
	case "string":
		caps["path"] = string(b.src[first.StartByte():first.EndByte()])
		// to: only decouples the controller from the URL for the string-literal
		// form (composeAndStamp gates its whole to: block on pattern ==
		// "http_verb_route") — the symbol-argument forms below never read it.
		caps["to_kw"] = keywordSegment(call, b.src, "to")
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
		case pendingNest != "":
			// A bare verb directly in a resources/resource block, wrapped
			// in neither member nor collection and carrying no on: — Rails
			// still treats it as a member action (resourceScopedHelperName).
			b.out = append(b.out, scopefold.Match{PatternName: "resource_scoped_verb", File: b.file, Line: line, Captures: caps})
		}
	}
}
