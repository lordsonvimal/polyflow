package valuegraph

import "fmt"

// A binding spec is the per-language half of the engine: the grammar facts the
// hand-written backtrackers currently hard-code, expressed as data.
//
// The spec addresses node types and field names directly — it does not compile
// tree-sitter queries. That is deliberate: a bare `_` in a tree-sitter query
// binds anonymous token nodes too, so `(argument_list _ (string))` matches the
// `(` and shifts every argument left. Field-addressed traversal has no such
// trap, and it is what the existing backtrackers already do by hand.
//
// VG.1 defines the types and the matching semantics. VG.2 adds the loader and
// the first language's data file; VG.4 adds the crossings engine. No node type named
// in a spec appears anywhere in this package's Go.

// Spec is one language's binding rules.
type Spec struct {
	Language  string        `yaml:"language"`
	Grammars  []string      `yaml:"grammars"`
	Literals  []LiteralRule `yaml:"literals"`
	Concat    []ConcatRule  `yaml:"concat"`
	Union     []UnionRule   `yaml:"union"`
	Bindings  []BindingRule `yaml:"bindings"`
	Scopes    []ScopeRule   `yaml:"scopes"`
	Opaque    []OpaqueRule  `yaml:"opaque"`
	Crossings []CrossRule   `yaml:"crossings"` // empty until VG.4
}

// Text modes for LiteralRule.
const (
	// TextRaw takes the whole node's source text.
	TextRaw = "raw"
	// TextInner strips the delimiters: the span between the end of the first
	// child and the start of the last. This covers both grammars that make the
	// quotes anonymous tokens and grammars that give them named nodes.
	TextInner = "inner"
	// TextTemplate reads an interpolated string: childless named children are
	// literal fragments, and a named child that has named children of its own is
	// a hole whose first named child resolves recursively.
	TextTemplate = "template"
)

// LiteralRule says which nodes are concrete strings.
type LiteralRule struct {
	Node string `yaml:"node"`
	Text string `yaml:"text"` // raw | inner | template
}

// ConcatRule says which nodes join values in order.
//
// Parts names the fields to read, in order. An empty Parts means "every named
// child, in order", which is how a variadic join is written.
type ConcatRule struct {
	Node     string   `yaml:"node"`
	Operator string   `yaml:"operator"` // optional; matched against the operator field or an anonymous child
	Parts    []string `yaml:"parts"`
}

// UnionRule says which nodes offer alternatives — a ternary, a match arm list.
// Parts behaves as in ConcatRule.
//
// Union is a first-class lattice kind with two producers: a node like this one,
// and a name bound more than once in a scope. Without the rule the second
// producer would be the only one, and a ternary right-hand side — which every
// current backtracker already reads as several real request paths — would
// resolve to Opaque.
type UnionRule struct {
	Node     string   `yaml:"node"`
	Operator string   `yaml:"operator"`
	Parts    []string `yaml:"parts"`
}

// BindingRule says which nodes bind a name to a value.
//
// Name and Value are field names. When a field name is empty the corresponding
// *Child index is used against NamedChild instead — some grammars expose no
// fields on the node that matters. Never Child: Child(1) is as likely to be the
// `=` token as the value. The indices are pointers so that "absent" and "index
// 0" stay distinguishable; a rule with neither form is rejected by Validate
// rather than resolving to nothing at run time.
type BindingRule struct {
	Node       string `yaml:"node"`
	Name       string `yaml:"name"`
	Value      string `yaml:"value"`
	NameChild  *int   `yaml:"name_child"`
	ValueChild *int   `yaml:"value_child"`
}

// ScopeRule says which nodes introduce a scope and where their parameters are.
//
// ParamsAlt is the declared alternative for a grammar that shapes the same
// construct two ways — a single unparenthesised parameter that hangs off a
// different field than the parameter list does. The loader treats a missing
// field as "try the next one", never as an error: this class of quirk is why
// the spec has declared alternatives at all.
type ScopeRule struct {
	Node      string `yaml:"node"`
	Params    string `yaml:"params"`
	ParamsAlt string `yaml:"params_alt"`
}

// OpaqueRule says where resolution deliberately stops.
//
// Reason is optional. When empty it is derived from the node type by dropping a
// trailing `_expression` or `_statement`, so `call_expression` stops with
// ReasonCall and `member_expression` with ReasonMember without the YAML having
// to restate it. A derived reason outside the Reason* set is kept verbatim: an
// unfamiliar reason in the ledger is more useful than ReasonUnsupported for
// everything.
type OpaqueRule struct {
	Node   string `yaml:"node"`
	Reason string `yaml:"reason"`
}

// CrossRule is a binding that crosses a file boundary and therefore needs an
// index rather than a local walk. VG.1 parses and validates nothing here; the
// field exists so the spec schema is stable across the tier. VG.4 gives it
// meaning.
type CrossRule struct {
	Kind      string        `yaml:"kind"`
	Direction string        `yaml:"direction"`
	Producer  CrossEndpoint `yaml:"producer"`
	Consumer  CrossEndpoint `yaml:"consumer"`
}

// CrossEndpoint is one side of a CrossRule.
type CrossEndpoint struct {
	Node        string   `yaml:"node"`
	NameChild   int      `yaml:"name_child"`
	ValueChild  int      `yaml:"value_child"`
	Owner       string   `yaml:"owner"`
	ValueIs     string   `yaml:"value_is"`
	Roots       []string `yaml:"roots"`
	Destructure bool     `yaml:"destructure"`
	CallArg     string   `yaml:"call_argument"`
}

// Validate reports the structural mistakes that would otherwise show up as a
// silently unresolvable expression: a rule that names no node, a binding with
// no value to bind, a literal with an unknown text mode.
func (s *Spec) Validate() error {
	if s == nil {
		return fmt.Errorf("valuegraph: nil spec")
	}
	if s.Language == "" {
		return fmt.Errorf("valuegraph: spec has no language")
	}
	for i, r := range s.Literals {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: literals[%d] has no node", i)
		}
		switch r.Text {
		case TextRaw, TextInner, TextTemplate:
		default:
			return fmt.Errorf("valuegraph: literals[%d] (%s) has unknown text mode %q", i, r.Node, r.Text)
		}
	}
	for i, r := range s.Concat {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: concat[%d] has no node", i)
		}
	}
	for i, r := range s.Union {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: union[%d] has no node", i)
		}
	}
	for i, r := range s.Bindings {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: bindings[%d] has no node", i)
		}
		if r.Name == "" && r.NameChild == nil {
			return fmt.Errorf("valuegraph: bindings[%d] (%s) has no name", i, r.Node)
		}
		if r.Value == "" && r.ValueChild == nil {
			return fmt.Errorf("valuegraph: bindings[%d] (%s) has no value", i, r.Node)
		}
	}
	for i, r := range s.Scopes {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: scopes[%d] has no node", i)
		}
	}
	for i, r := range s.Opaque {
		if r.Node == "" {
			return fmt.Errorf("valuegraph: opaque[%d] has no node", i)
		}
	}
	return nil
}

// index is the compiled form the engine traverses: node type → rule. The spec
// is small enough that a linear scan would do; the map exists so that adding a
// language does not make resolution slower for every other one.
type index struct {
	literal map[string]LiteralRule
	concat  map[string]ConcatRule
	union   map[string]UnionRule
	opaque  map[string]OpaqueRule
	scope   map[string]ScopeRule
	binding map[string][]BindingRule
}

func newIndex(s *Spec) *index {
	ix := &index{
		literal: map[string]LiteralRule{},
		concat:  map[string]ConcatRule{},
		union:   map[string]UnionRule{},
		opaque:  map[string]OpaqueRule{},
		scope:   map[string]ScopeRule{},
		binding: map[string][]BindingRule{},
	}
	if s == nil {
		return ix
	}
	for _, r := range s.Literals {
		ix.literal[r.Node] = r
	}
	for _, r := range s.Concat {
		ix.concat[r.Node] = r
	}
	for _, r := range s.Union {
		ix.union[r.Node] = r
	}
	for _, r := range s.Opaque {
		if r.Reason == "" {
			r.Reason = derivedReason(r.Node)
		}
		ix.opaque[r.Node] = r
	}
	for _, r := range s.Scopes {
		ix.scope[r.Node] = r
	}
	for _, r := range s.Bindings {
		ix.binding[r.Node] = append(ix.binding[r.Node], r)
	}
	return ix
}

// derivedReason turns a node type into a stop reason by dropping the suffix
// that says it is an expression. See OpaqueRule.Reason.
func derivedReason(node string) string {
	for _, suffix := range []string{"_expression", "_statement", "_operator"} {
		if len(node) > len(suffix) && node[len(node)-len(suffix):] == suffix {
			return node[:len(node)-len(suffix)]
		}
	}
	if node == "" {
		return ReasonUnsupported
	}
	return node
}
