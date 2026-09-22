// Package scopefold is Tier SF (docs/scope-fold-engine-plan.md): a generic
// engine for the one shape Tier FX's existing two extraction tiers cannot
// express — a recursive fold over nested scope-opening constructs, each
// declaring a contribution to one or more named accumulator stacks, that
// terminates in a composed flat fact at a leaf construct. This is how
// nearly every backend framework's routing works (Rails
// namespace/scope/resources, Express Router.use(prefix, sub), Django
// include(), FastAPI include_router(prefix=...), Spring @RequestMapping,
// ...), and today each one is a bespoke hand-written Go walker
// (internal/parser/ruby_route_paths.go is the reference case this package
// is scoped to eventually replace).
//
// A Grammar is per-language-and-framework config (eventually loaded from a
// framework's pattern YAML, same as patterns.LoadFramework's `patterns:`/
// `hub:`/`emit:` blocks); the Engine (engine.go) that walks it is written
// once and shared by every framework. Phase 1 (this package, current state)
// proves the engine mechanics against an in-memory toy grammar with no
// cedar/Rails dependency; wiring a real Grammar to a framework's YAML and
// porting Rails routes.rb is a later phase — see the plan doc's §8.
package scopefold

// Grammar is one language/framework's scope-fold configuration.
type Grammar struct {
	// Stacks declares which named accumulator stacks this framework uses
	// (e.g. "path", "module", "name"). A framework that needs fewer than
	// Rails' three just declares a subset — the engine itself is agnostic
	// to how many there are.
	Stacks []string `yaml:"stacks"`

	// CallNodeTypes are the tree-sitter node types eligible to be checked
	// against a scope/leaf pattern match (Ruby: "call"; JS: "call_expression";
	// Python: "call"). Defaults to ["call"] when empty.
	CallNodeTypes []string `yaml:"call_node_types"`

	Scopes []ScopeSpec `yaml:"scopes"`
	Leaves []LeafSpec  `yaml:"leaves"`

	// ExpandTables are table-driven implicit-construct synthesis specs
	// (Rails' `resources :x` implying 8 CRUD routes is the reference case),
	// named and referenced by a ScopeSpec's Expand field.
	ExpandTables map[string]ExpandTable `yaml:"expand_tables"`
}

// ScopeSpec is one scope-opening construct: a pattern match that contributes
// segments to the threaded stacks and (usually) recurses into a nested body.
type ScopeSpec struct {
	// Match is the name of an existing patterns: entry (or, for a caller
	// supplying MatchResults directly as this package's tests do, any
	// caller-assigned name) — scope-fold never defines its own tree-sitter
	// query language; it reuses whatever produced the MatchResult.
	Match string `yaml:"match"`

	// Recurse names the call node's child field (via ChildByFieldName) that
	// holds the nested body to fold into with the updated stacks — "block"
	// for a Ruby do...end, for example. A scope with no Recurse contributes
	// its stack pushes but folds no further beneath it.
	Recurse string `yaml:"recurse"`

	// Contributes maps a declared stack name to the ordered list of segments
	// this scope's match pushes onto that stack (usually one; Rails'
	// namespace/scope push two onto its name stack — the enclosing
	// resource's singular form flattened in first, via a Contribution.Stack
	// read, then the construct's own literal segment).
	Contributes map[string][]Contribution `yaml:"contributes"`

	// Expand names an ExpandTables entry: table-driven synthesis of the
	// implicit child constructs this scope declares (run with this scope's
	// own OWN contributed stacks, before recursing into any explicit body).
	Expand string `yaml:"expand"`

	// Resets names stacks to clear (to empty, not to the parent's value)
	// before this scope's own Contributes run and before recursing —
	// generalizes Rails' namespace/scope ending the enclosing resource's
	// naming claim (singular/onScope reset to their zero value) without
	// ending its path/module claim (which simply isn't in Resets).
	//
	// There is deliberately no dedicated "nest param" field: Rails'
	// resources :folders scoping its children under :folder_id is a
	// single-generation pending value — set for direct children, read (via
	// Contribution.Stack) only by a child that is itself a scope-opening
	// construct, and discarded by every scope (including the one that set
	// it, for its own use) via Resets. Contributes + Resets on an ordinary
	// named stack already expresses this; a special field would duplicate
	// it under different rules.
	Resets []string `yaml:"resets"`
}

// LeafSpec is one terminal construct: a pattern match that composes the
// current stack state (plus its own captures) into one emitted Fact.
type LeafSpec struct {
	Match string   `yaml:"match"`
	Emit  EmitSpec `yaml:"emit"`
}

// EmitSpec names the predicate and argument list one leaf (or expand-table
// row) composes into a factpipe.Fact.
type EmitSpec struct {
	Pred string    `yaml:"pred"`
	Args []EmitArg `yaml:"args"`
}

// EmitArg is one fact argument. Exactly one of Literal / Stack / Capture
// should be set; Stack composes a threaded stack, Capture reads directly off
// the leaf match's own Captures.
type EmitArg struct {
	// Literal is a constant value.
	Literal string `yaml:"literal"`

	// Stack + Compose read a threaded stack ("join_segments": "/a/b/c";
	// "join": "a/b/c", no leading slash — a module-nesting path rather than
	// a URL; "helper_name": "a_b_c"). AppendCapture/AppendExtract optionally append
	// one more segment — the leaf's own path/action text — before composing,
	// the same way a Rails verb route's own literal path segment joins onto
	// its enclosing scopes' accumulated prefix.
	Stack         string `yaml:"stack"`
	Compose       string `yaml:"compose"`
	AppendCapture string `yaml:"append_capture"`
	AppendExtract string `yaml:"append_extract"`

	// AppendList appends zero or more further computed segments (each
	// evaluated the same as any EmitArg, typically Literal or Capture) after
	// AppendCapture and before Compose — Rails' bare `get :action, on:
	// :member` inline form pushes both a literal ":id" placeholder AND the
	// action segment, in that order, neither of which came from an
	// enclosing scope's stack push.
	AppendList []EmitArg `yaml:"append_list"`

	// Prepend inserts zero or more further computed segments (each its own
	// EmitArg, evaluated the same as AppendList's) at the FRONT of the
	// composed segment list, before Compose — the mirror image of AppendList,
	// needed because Rails' two route-naming conventions disagree on which
	// side the qualifier goes: `sync_user` (a member action prefixes the
	// resource base) versus `study_deliverables` (a nested resource's own
	// collection name suffixes its parent's). One EmitArg can only build one
	// of those shapes; needing both was the reason to add a second, distinct
	// field rather than reversing AppendList's order for everyone.
	Prepend []EmitArg `yaml:"prepend"`

	// Capture + Extract read one capture off the match directly, through a
	// "|"-chained verb pipeline (e.g. "segment|upcase").
	Capture string `yaml:"capture"`
	Extract string `yaml:"extract"`

	// Row reads a field ("name" or "method") off the ExpandRow currently
	// being synthesized — only meaningful inside an ExpandTable's Emit,
	// nil/ignored for an ordinary LeafSpec's Emit.
	Row string `yaml:"row"`

	// Fallback is evaluated, and its result used, only when this arg's own
	// primary source (Capture or Stack) resolves to "". Generalizes Rails'
	// "read on: from a keyword, or else fall back to the lexically enclosing
	// member/collection block" — the keyword is a Capture, the lexical block
	// is a Stack pushed by the member/collection scopes and read with
	// Compose: "top".
	Fallback *EmitArg `yaml:"fallback"`

	// Switch dispatches to one of several EmitArg templates by the resolved
	// value of On — Rails' verbRouteHelperName reads a route's on:/lexical
	// scope and names it completely differently depending on whether that
	// value is "member", "collection", or neither; a chain of Fallbacks
	// cannot express a 3-way branch because Fallback only asks "is this
	// empty", never "what is this".
	Switch *SwitchSpec `yaml:"switch"`
}

// SwitchSpec is EmitArg's 3-or-more-way branch: On is evaluated first (using
// only its own Capture/Stack/Fallback — On's own Switch, if any, is honored
// too, letting cases nest), and its resolved value selects a case from Cases
// by exact string match, falling back to Default when nothing matches (or to
// "" when Default is nil).
type SwitchSpec struct {
	On      EmitArg            `yaml:"on"`
	Cases   map[string]EmitArg `yaml:"cases"`
	Default *EmitArg           `yaml:"default"`
}

// Contribution is how a ScopeSpec extracts the one segment it pushes onto a
// stack, from its own match's Captures.
type Contribution struct {
	// Literal pushes a fixed value regardless of the match's captures — how
	// the member/collection scopes (which take no arguments of their own)
	// push a fixed "member"/"collection" token onto an on_scope stack.
	Literal string `yaml:"literal"`
	Capture string `yaml:"capture"`
	Extract string `yaml:"extract"`

	// Stack reads another named stack's current top (the parent's state,
	// before this scope's own pushes) and contributes it verbatim —
	// nameScope's "flatten the enclosing resource's singular form into the
	// name stack before pushing this construct's own segment" (Rails: a
	// namespace/scope opened inside `resources :users` names its own
	// contents user_admin_... not admin_...).
	Stack string `yaml:"stack"`

	// Fallback is evaluated, and used, only when Literal/Capture/Stack all
	// resolve to "" — the same combinator as EmitArg.Fallback (Rails' scope
	// path: an explicit path: keyword wins over the positional argument).
	Fallback *Contribution `yaml:"fallback"`
}

// ExpandTable is one table-driven implicit-construct synthesis spec. Each
// non-filtered row runs through Emit exactly as a LeafSpec's own match would,
// with EmitArg.Row available to read the row's own Name/Method and the
// "path" stack temporarily extended per Member/Suffix.
type ExpandTable struct {
	Emit EmitSpec `yaml:"emit"`

	// FilterKeywords name captures on the *scope* match (read by convention,
	// not re-parsed) holding a comma/space-separated symbol list — Rails'
	// only:/except:. A row whose Name isn't in a present "only" list, or is
	// in a present "except" list, is skipped.
	FilterKeywords []string `yaml:"filter_keywords"`

	Rows []ExpandRow `yaml:"rows"`

	// MemberSegment is the placeholder pushed onto "path" for a Member row,
	// before Suffix. Defaults to "*" (Rails' actual token is ":id" — set
	// explicitly by the Rails grammar; "*" is only the toy-grammar default).
	MemberSegment string `yaml:"member_segment"`
}

// ExpandRow is one implicit construct an ExpandTable declares.
type ExpandRow struct {
	Name   string `yaml:"name"`
	Method string `yaml:"method"`
	// Member marks a row scoped to one member of the collection (Rails:
	// pushes MemberSegment onto the path stack before Suffix).
	Member bool   `yaml:"member"`
	Suffix string `yaml:"suffix"`

	// Emit overrides the ExpandTable's own Emit for this row alone — Rails'
	// seven implicit REST routes share a path/method shape but not a name
	// shape (index/create name off the collection, new/edit prefix the
	// singular with their own action, show/update/destroy name off the bare
	// singular), so the table needs a per-row override rather than one
	// formula every row bends to fit.
	Emit *EmitSpec `yaml:"emit"`
}
