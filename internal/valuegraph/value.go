// Package valuegraph answers one question: "what string can this expression
// be?" — for any language, driven by a per-language binding spec.
//
// It is the engine half of Tier VG (docs/js-value-graph-pilot-plan.md). The
// load-bearing rule of the tier is that the engine returns a Value and nothing
// else: fan-out caps, abstention, ledger-vs-mint and one-node-per-URL branching
// all stay at the caller's mint site, because two shipped passes hold
// deliberately contradictory policies over the same lattice shape. There is no
// policy vocabulary in this package, so there is nowhere for a policy to leak
// into it.
//
// The package is a leaf: it imports go-tree-sitter and the standard library,
// and nothing else from this repository.
package valuegraph

import (
	"sort"
	"strings"
)

// Kind is the lattice. Four cases, closed set.
type Kind uint8

const (
	KindLiteral Kind = iota // a concrete string
	KindConcat              // ordered parts, joined
	KindUnion               // alternatives, deduped, order-independent
	KindOpaque              // resolution stopped; Origin says why
)

// Reasons resolution can stop. Anything the spec cannot follow becomes
// KindOpaque with one of these — never a silent empty result, because "we could
// not follow this" and "there is nothing here" have different ledger
// consequences.
//
// The three cap reasons are deliberately distinct from each other and from
// every syntactic reason: a slow or over-broad run has to be diagnosable from
// the ledger rather than by bisection.
const (
	ReasonParam       = "param"       // an unbound parameter of the enclosing scope
	ReasonMember      = "member"      // a member/property read the spec declines
	ReasonCall        = "call"        // a call the spec declines
	ReasonImport      = "import"      // the binding is an import; crossing it is VG.4
	ReasonNoBinding   = "no_binding"  // the name has no binding in any enclosing scope
	ReasonDepth       = "depth"       // Options.MaxDepth binding hops exhausted
	ReasonCycle       = "cycle"       // the binding refers to itself
	ReasonWidth       = "width"       // Options.MaxUnionWidth alternatives exceeded
	ReasonFiles       = "files"       // Options.MaxFiles files opened in one Resolve
	ReasonUnsupported = "unsupported" // no rule in the spec addresses this node
)

// Origin records where and why resolution stopped. Never empty on KindOpaque.
type Origin struct {
	Reason string // one of the Reason* constants
	File   string
	Line   int
	Text   string // the source text of the expression we could not follow
}

// Value is one node of the lattice. The zero Value is Literal(""), which no
// resolution path produces: Resolve never returns a zero Value.
type Value struct {
	Kind   Kind
	Text   string  // KindLiteral only
	Parts  []Value // KindConcat (ordered) / KindUnion (deduped, sorted by String())
	Origin Origin  // KindOpaque only
}

// DefaultMaxStrings caps Strings when its max argument is 0.
const DefaultMaxStrings = 32

// wildcard is how an unresolved segment renders. It is the convention the
// contract engine's param_wildcard and dynamic_host_strip normalizers already
// consume; this tier introduces no new downstream vocabulary.
const wildcard = "*"

// Literal builds a concrete string value.
func Literal(text string) Value { return Value{Kind: KindLiteral, Text: text} }

// Opaque builds a stopped value. A caller that passes an empty Reason gets
// ReasonUnsupported rather than an Origin that says nothing.
func Opaque(o Origin) Value {
	if o.Reason == "" {
		o.Reason = ReasonUnsupported
	}
	return Value{Kind: KindOpaque, Origin: o}
}

// Concat joins parts in order. Nested concats are flattened and adjacent
// literals merged, so two expressions that differ only in how the grammar
// happened to associate `+` produce the same Value — which is what makes the
// memo cache and the union dedup agree with each other.
func Concat(parts ...Value) Value {
	flat := make([]Value, 0, len(parts))
	for _, p := range parts {
		if p.Kind == KindConcat {
			flat = append(flat, p.Parts...)
			continue
		}
		flat = append(flat, p)
	}
	merged := make([]Value, 0, len(flat))
	for _, p := range flat {
		if p.Kind == KindLiteral {
			if p.Text == "" {
				continue
			}
			if n := len(merged); n > 0 && merged[n-1].Kind == KindLiteral {
				merged[n-1].Text += p.Text
				continue
			}
		}
		merged = append(merged, p)
	}
	switch len(merged) {
	case 0:
		return Literal("")
	case 1:
		return merged[0]
	}
	return Value{Kind: KindConcat, Parts: merged}
}

// Union collects alternatives. Nested unions are flattened, duplicates removed
// and the result sorted by String(), so union membership is order-independent:
// two branches discovered in a different file order compare equal.
//
// A union of nothing is Opaque — it means the caller found no alternatives at
// all, which is a stopped resolution, not the empty string.
func Union(parts ...Value) Value {
	flat := make([]Value, 0, len(parts))
	for _, p := range parts {
		if p.Kind == KindUnion {
			flat = append(flat, p.Parts...)
			continue
		}
		flat = append(flat, p)
	}
	seen := make(map[string]bool, len(flat))
	out := make([]Value, 0, len(flat))
	for _, p := range flat {
		k := p.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	switch len(out) {
	case 0:
		return Opaque(Origin{Reason: ReasonUnsupported, Text: "empty union"})
	case 1:
		return out[0]
	}
	return Value{Kind: KindUnion, Parts: out}
}

// String is a canonical, lossless-enough rendering used for dedup, union
// ordering and test failure messages. It is not the resolved URL — that is
// Strings.
func (v Value) String() string {
	switch v.Kind {
	case KindLiteral:
		return v.Text
	case KindConcat:
		var b strings.Builder
		for _, p := range v.Parts {
			b.WriteString(p.String())
		}
		return b.String()
	case KindUnion:
		parts := make([]string, 0, len(v.Parts))
		for _, p := range v.Parts {
			parts = append(parts, p.String())
		}
		return "(" + strings.Join(parts, "|") + ")"
	default:
		return "<" + v.Origin.Reason + ":" + v.Origin.Text + ">"
	}
}

// Strings enumerates the concrete strings this Value can take, with each
// unresolved segment rendered as "*". Union multiplies out; the result is
// sorted and deduped, and truncated to max (0 = the package default, 32).
//
// ok is false when the Value is wholly opaque, or when truncation occurred —
// callers must ledger rather than mint in that case. out is still populated
// when ok is false, because a ledger message is better for naming what it could
// not read; branch on ok, never on len(out).
func (v Value) Strings(max int) (out []string, ok bool) {
	if max <= 0 {
		max = DefaultMaxStrings
	}
	got, truncated := v.enumerate(max)
	sort.Strings(got)
	return got, !truncated && v.hasLiteral()
}

// enumerate produces the string set, capping the working set at max and
// reporting whether anything was dropped. The cap is applied to intermediate
// products too: a union of unions multiplies out, and holding the full product
// before truncating is how a pathological site turns into a memory problem
// instead of a ledger row.
func (v Value) enumerate(max int) (out []string, truncated bool) {
	switch v.Kind {
	case KindLiteral:
		return []string{v.Text}, false
	case KindOpaque:
		return []string{wildcard}, false
	case KindUnion:
		seen := make(map[string]bool)
		for _, p := range v.Parts {
			got, tr := p.enumerate(max)
			truncated = truncated || tr
			for _, s := range got {
				if seen[s] {
					continue
				}
				if len(seen) >= max {
					return dedupeKeys(seen), true
				}
				seen[s] = true
			}
		}
		return dedupeKeys(seen), truncated
	case KindConcat:
		acc := []string{""}
		for _, p := range v.Parts {
			got, tr := p.enumerate(max)
			truncated = truncated || tr
			next := make([]string, 0, len(acc))
			seen := make(map[string]bool, len(acc))
			for _, prefix := range acc {
				for _, suffix := range got {
					s := joinSegments(prefix, suffix)
					if seen[s] {
						continue
					}
					if len(next) >= max {
						return next, true
					}
					seen[s] = true
					next = append(next, s)
				}
			}
			acc = next
		}
		return acc, truncated
	}
	return []string{""}, false
}

// joinSegments concatenates two rendered segments, collapsing a run of
// wildcards into one: `${a}${b}` is one unknown segment to a route matcher, not
// two, and "**" matches nothing the normalizers understand.
func joinSegments(a, b string) string {
	if strings.HasSuffix(a, wildcard) && strings.HasPrefix(b, wildcard) {
		return a + strings.TrimPrefix(b, wildcard)
	}
	return a + b
}

func dedupeKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasLiteral reports whether any leaf carries concrete text. A Value with none
// is wholly opaque: every string it enumerates is made of wildcards, which is a
// site to ledger and not a URL to mint.
func (v Value) hasLiteral() bool {
	switch v.Kind {
	case KindLiteral:
		return strings.TrimSpace(v.Text) != ""
	case KindConcat, KindUnion:
		for _, p := range v.Parts {
			if p.hasLiteral() {
				return true
			}
		}
	}
	return false
}

// IsDynamic reports whether any part is Opaque.
func (v Value) IsDynamic() bool {
	if v.Kind == KindOpaque {
		return true
	}
	for _, p := range v.Parts {
		if p.IsDynamic() {
			return true
		}
	}
	return false
}

// Origins returns every Opaque origin in the tree, in traversal order, for
// ledger messages.
func (v Value) Origins() []Origin {
	var out []Origin
	var walk func(Value)
	walk = func(n Value) {
		if n.Kind == KindOpaque {
			out = append(out, n.Origin)
			return
		}
		for _, p := range n.Parts {
			walk(p)
		}
	}
	walk(v)
	return out
}
