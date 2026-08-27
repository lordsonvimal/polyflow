package graph

import (
	"fmt"
	"slices"
	"strings"
)

// NoiseClass labels *why* an edge is structural/plumbing rather than a
// direct behavioral hop. The empty class ("") is always visible; every
// other class is hidden unless explicitly included.
type NoiseClass string

const (
	NoiseNone        NoiseClass = ""             // default-visible: calls, reads, writes, http_call, publishes, navigates_to, flows_to, ...
	NoiseFilterChain NoiseClass = "filter_chain" // Rails before_action/after_action, Gin/Express middleware wiring
	NoiseMixin       NoiseClass = "mixin"        // include/extend/prepend — class-wide, not call-site-specific
	NoiseContainment NoiseClass = "containment"  // "this file/class also declares..." — not a call at all
	NoiseRenderTree  NoiseClass = "render_tree"  // JSX/DOM render target (CSS-selector `element` nodes)
	NoiseTestCode    NoiseClass = "test_code"    // edge touches a _test.go/.spec.ts/_spec.rb/... file — a test exercising production code, not a production call chain
)

// testFileSuffixes/testFileMarkers flag a file as test code by name
// convention across the languages this indexer parses. A file matching
// either is never a "real" production call site — tracing through it just
// enumerates every test that happens to exercise the node on the other end
// of the edge, which fans a single shared production function out to
// dozens of unrelated callers (the same shape filter_chain hides for Rails
// before_action/Gin middleware, just triggered by test frameworks instead).
var testFileSuffixes = []string{
	"_test.go",
	".test.ts", ".test.tsx", ".test.js", ".test.jsx",
	".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx",
	"_spec.rb", "_test.rb",
	"_test.py", "_test.py3",
}

func isTestFile(path string) bool {
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, suf := range testFileSuffixes {
		if strings.HasSuffix(path, suf) {
			return true
		}
	}
	if strings.HasPrefix(base, "test_") {
		return true
	}
	for _, seg := range []string{"/spec/", "/tests/", "/__tests__/"} {
		if strings.Contains(path, seg) {
			return true
		}
	}
	return false
}

// ClassifyEdgeNoise reports which class an edge belongs to, given its
// source and destination nodes. Every signal below is already present in
// the graph — no parser or linker change is required for classification
// itself. src may be nil when unavailable to the caller (test-file
// detection then falls back to dst alone).
func ClassifyEdgeNoise(e *Edge, src, dst *Node) NoiseClass {
	if e.Meta != nil {
		switch e.Meta["via"] {
		case "rails_filter":
			return NoiseFilterChain // internal/linker/rails_filters.go:818, reused verbatim from Tier IR
		case "gin_middleware_use":
			return NoiseFilterChain // internal/linker/gin_middleware.go:210 — Gin's own before_action equivalent
		case "express_middleware_use":
			return NoiseFilterChain // internal/linker/express_middleware.go — Express's own before_action equivalent
		case "closure_param":
			// internal/parser/go_semantic.go: a generic wrapper (e.g.
			// withID(ctx, name, func(id uint){...})) invoking a func-typed
			// parameter. The wrapper is a single shared node reused across
			// every call site, so the reverse edge fans backward-traversal
			// out to every OTHER unrelated function that also passes the
			// wrapper a callback — the same shape as Gin/Express middleware.
			return NoiseFilterChain
		}
	}
	if e.Type == EdgeTypeInherits {
		return NoiseMixin // internal/linker/ruby_type_relations.go:233,266, reused verbatim from Tier IR
	}
	if structuralEdgeTypes[e.Type] { // query.go:33-38, existing map, same package
		return NoiseContainment
	}
	if dst != nil && dst.Type == NodeTypeElement { // model.go:25
		return NoiseRenderTree
	}
	if (src != nil && isTestFile(src.File)) || (dst != nil && isTestFile(dst.File)) {
		return NoiseTestCode
	}
	return NoiseNone
}

// NoiseInclude is the set of noise classes a caller has opted into seeing.
type NoiseInclude map[NoiseClass]bool

// Allows reports whether c should be visible under this include-set.
// NoiseNone is always visible.
func (n NoiseInclude) Allows(c NoiseClass) bool {
	if c == NoiseNone {
		return true
	}
	return n[c]
}

var allNoiseClasses = []NoiseClass{NoiseFilterChain, NoiseMixin, NoiseContainment, NoiseRenderTree, NoiseTestCode}

// AllNoiseInclude returns an include-set containing every noise class —
// today's unfiltered behavior ("all"), for callers that predate Tier NV
// (search-corpus generation, the web flows API) and still want every chain,
// not just the agent-facing default view.
func AllNoiseInclude() NoiseInclude {
	out := NoiseInclude{}
	for _, c := range allNoiseClasses {
		out[c] = true
	}
	return out
}

// ParseNoiseInclude turns CLI/MCP keys into an include-set. "all" includes
// every class (today's unfiltered behavior — the escape hatch). "none"
// (or omitted with no intent) is the empty set. Unknown keys are a hard
// error, not a silent no-op — an agent that mistypes a class name must be
// told, not silently given unfiltered or over-filtered output.
func ParseNoiseInclude(keys []string) (NoiseInclude, error) {
	out := NoiseInclude{}
	for _, k := range keys {
		if k == "all" {
			for _, c := range allNoiseClasses {
				out[c] = true
			}
			return out, nil
		}
		if k == "none" || k == "" {
			continue
		}
		c := NoiseClass(k)
		if !slices.Contains(allNoiseClasses, c) {
			return nil, fmt.Errorf("unknown noise class %q, want one of %v or \"all\"", k, allNoiseClasses)
		}
		out[c] = true
	}
	return out, nil
}

// ResolveNoiseInclude computes an include-set from an explicit CLI/MCP
// --include value (rawInclude, possibly empty) and a task/intent string, per
// the explicit-override model: a non-empty rawInclude always replaces the
// intent-based default entirely rather than merging with it.
func ResolveNoiseInclude(rawInclude []string, task string) (NoiseInclude, error) {
	if len(rawInclude) > 0 {
		return ParseNoiseInclude(rawInclude)
	}
	return DefaultNoiseInclude(task), nil
}

// DefaultNoiseInclude is where "everything depends on intent" is decided.
// It is the ONLY place default visibility differs by task — every other
// mechanism in this plan is intent-agnostic classification. Explicit
// --include always overrides this table entirely (never merges with it).
func DefaultNoiseInclude(intent string) NoiseInclude {
	switch intent {
	case "generate":
		// Building/understanding UI structure: the render tree IS the
		// signal here, not noise. Filter-chain/mixin/containment stay
		// hidden — "generate" still means "what do I call", not "what
		// classes does this file declare".
		return NoiseInclude{NoiseRenderTree: true}
	default: // "impact", "debug", "refactor", "" (trace's bare default)
		return NoiseInclude{}
	}
}
