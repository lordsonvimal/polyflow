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

	// Contributes maps a declared stack name to how this scope's match
	// extracts the segment it pushes onto that stack.
	Contributes map[string]Contribution `yaml:"contributes"`

	// NestParam additionally pushes onto the "path" stack specifically —
	// generalizes Rails' nested-resource id parameter
	// (resources :folders implicitly scoping everything declared inside it
	// under /folders/:folder_id, not just /folders).
	NestParam *Contribution `yaml:"nest_param"`

	// Expand names an ExpandTables entry: table-driven synthesis of the
	// implicit child constructs this scope declares (run with this scope's
	// own OWN contributed stacks, before recursing into any explicit body).
	Expand string `yaml:"expand"`

	// Resets names stacks to clear (to empty, not to the parent's value)
	// before this scope's own Contributes run and before recursing —
	// generalizes Rails' namespace/scope ending the enclosing resource's
	// naming claim (singular/onScope reset to their zero value) without
	// ending its path/module claim (which simply isn't in Resets).
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
	// "helper_name": "a_b_c"). AppendCapture/AppendExtract optionally append
	// one more segment — the leaf's own path/action text — before composing,
	// the same way a Rails verb route's own literal path segment joins onto
	// its enclosing scopes' accumulated prefix.
	Stack         string `yaml:"stack"`
	Compose       string `yaml:"compose"`
	AppendCapture string `yaml:"append_capture"`
	AppendExtract string `yaml:"append_extract"`

	// Capture + Extract read one capture off the match directly, through a
	// "|"-chained verb pipeline (e.g. "segment|upcase").
	Capture string `yaml:"capture"`
	Extract string `yaml:"extract"`

	// Fallback is evaluated, and its result used, only when this arg's own
	// primary source (Capture or Stack) resolves to "". Generalizes Rails'
	// "read on: from a keyword, or else fall back to the lexically enclosing
	// member/collection block" — the keyword is a Capture, the lexical block
	// is a Stack pushed by the member/collection scopes and read with
	// Compose: "top".
	Fallback *EmitArg `yaml:"fallback"`
}

// Contribution is how a ScopeSpec extracts the one segment it pushes onto a
// stack, from its own match's Captures.
type Contribution struct {
	// Literal pushes a fixed value regardless of the match's captures — how
	// the member/collection scopes (which take no arguments of their own)
	// push a fixed "member"/"collection" token onto an on_scope stack.
	Literal  string `yaml:"literal"`
	Capture  string `yaml:"capture"`
	Extract  string `yaml:"extract"`
	Optional bool   `yaml:"optional"`
}

// ExpandTable is one table-driven implicit-construct synthesis spec.
type ExpandTable struct {
	// Pred is the predicate every synthesized row is emitted under.
	Pred string `yaml:"pred"`

	// FilterKeywords name captures on the *scope* match (read by convention,
	// not re-parsed) holding a comma/space-separated symbol list — Rails'
	// only:/except:. A row whose Name isn't in a present "only" list, or is
	// in a present "except" list, is skipped.
	FilterKeywords []string `yaml:"filter_keywords"`

	Rows []ExpandRow `yaml:"rows"`

	// CollisionRule names one of a small fixed registry of
	// framework-idiosyncratic naming-collision rules (Rails' singular==
	// plural "_index" suffix is the only one that exists today). Empty means
	// none apply.
	CollisionRule string `yaml:"collision_rule"`
}

// ExpandRow is one implicit construct an ExpandTable declares.
type ExpandRow struct {
	Name   string `yaml:"name"`
	Method string `yaml:"method"`
	// Member marks a row scoped to one member of the collection (Rails:
	// pushes a path-stack placeholder id segment before Suffix).
	Member bool   `yaml:"member"`
	Suffix string `yaml:"suffix"`
}
