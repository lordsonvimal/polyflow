// Package toolchain provides a declarative registry mapping each integrated
// tool (parser engine or interpretation target) to versioned backends. It
// drives both version-gated rule selection (mechanism A) and sidecar
// dispatch (mechanism B) through one shared gate.
package toolchain

import "github.com/lordsonvimal/polyflow/internal/patterns"

// Tool identifies an integrated parser-engine or interpretation target.
type Tool string

const (
	ToolGo         Tool = "go"
	ToolJavaScript Tool = "javascript"
	ToolTypeScript Tool = "typescript"
	ToolTempl      Tool = "templ"
	ToolDatastar   Tool = "datastar"
	ToolHTML       Tool = "html"
	ToolRuby       Tool = "ruby"
)

// Backend is one registry row: a version range mapped to EITHER a rule/pattern
// variant (mechanism A) OR a sidecar build (mechanism B) — never both.
type Backend struct {
	VersionRange   string // semver expr, evaluated via patterns.VersionInRange; "" = catch-all
	RuleVariant    string // pattern/contract variant id; "" when sidecar'd
	SidecarBackend string // sidecar build id;            "" when rule-gated
}

// Registry: ordered rows per tool; first satisfied range wins. No row
// satisfied → nearest-NEWEST backend + confidence=inferred + coverage note
// (the fail-safe — never an error, never silent).
type Registry map[Tool][]Backend

// Selection is the outcome of a registry lookup, stamped into graph meta
// (profile_used / backend_version).
type Selection struct {
	Tool     Tool
	Version  string  // resolved from the target project
	Backend  Backend
	Inferred bool    // true when nearest-fallback was used
}

// Select returns the first Backend whose VersionRange is satisfied by version.
// An empty VersionRange is a catch-all (always matches, Inferred=false).
// If no row matches, the first row (nearest-newest, ordered newest-first)
// is returned with Inferred=true.
// If the tool has no registry entry at all, a zero Backend is returned with
// Inferred=true.
func (r Registry) Select(tool Tool, version string) Selection {
	rows := r[tool]
	if len(rows) == 0 {
		return Selection{Tool: tool, Version: version, Inferred: true}
	}
	for _, b := range rows {
		if b.VersionRange == "" || patterns.VersionInRange(version, b.VersionRange) {
			return Selection{Tool: tool, Version: version, Backend: b}
		}
	}
	// Nearest-newest fallback: first row (registry ordered newest-first).
	return Selection{Tool: tool, Version: version, Backend: rows[0], Inferred: true}
}

// DefaultRegistry returns the toolchain Registry seeded with today's single
// known version per tool. Adding a new tool version = one new Backend row
// prepended to the slice; core code is never modified.
//
// Rows ordered newest-first per tool so the nearest-newest fallback selects
// the most capable known backend when an out-of-range version is resolved.
func DefaultRegistry() Registry {
	return Registry{
		// Living standards: VersionRange "" = catch-all. ResolveToolchain
		// returns "living" for these; no semver comparison needed.
		ToolHTML:       {{VersionRange: "", RuleVariant: "html-living"}},
		ToolJavaScript: {{VersionRange: "", RuleVariant: "javascript-living"}},

		// Versioned tools — single row today; prepend new rows as versions land.
		ToolGo:         {{VersionRange: ">=1.21", RuleVariant: "go-v1"}},
		ToolTypeScript: {{VersionRange: ">=4.0.0", RuleVariant: "typescript-living"}},
		// Templ uses a sidecar backend (parser-engine isolation); no RuleVariant.
		ToolTempl:    {{VersionRange: ">=0.3.0", SidecarBackend: "templ-v0.3"}},
		// datastar-v1 (colon syntax, >=1.0.0 <2.0.0); datastar-v0 (hyphen
		// syntax, <1.0.0). Upper bounds ensure that a future v2 triggers the
		// nearest-newest fallback (Inferred=true) rather than silently using
		// stale semantics — V.4 fail-safe contract.
		ToolDatastar: {
			{VersionRange: ">=1.0.0,<2.0.0", RuleVariant: "datastar-v1"},
			{VersionRange: ">=0.0.0,<1.0.0", RuleVariant: "datastar-v0"},
		},
		ToolRuby:     {{VersionRange: ">=3.0", RuleVariant: "ruby-v3"}},
	}
}
