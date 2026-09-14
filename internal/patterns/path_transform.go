package patterns

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

// path_transform.go is FX.8.P5 — the path_transform verb (plan § FX.0 / §1f,
// docs/factpipe-vocabulary.md). It converts a filename or path string to a
// route key: split on a delimiter, drop known-noise segments (index files,
// framework markers), rewrite the rest through an ordered match/replace table.
// Every filesystem router (Next pages/app, SvelteKit, Nuxt, Nuxt-server,
// Remix) is this same table with a different delimiter and rule set — the
// framework-specific part is ~8 lines of YAML, never Go
// (internal/linker/file_routes.go's buildSegmentPath, one hand-written
// version of exactly this).
//
// Unlike every other verb, its config is not a "(arg)" string — the rules are
// structured (a match pattern, a replacement or an action), so it is a
// dedicated `path_transform:` sub-block on the arg spec, not a string a
// generic parser splits. evalArg special-cases `extract: path_transform`
// before calling into the string-verb dispatch in extract.go's runVerb.

// PathTransformSpec is one `path_transform:` block on a `facts[].args` entry.
type PathTransformSpec struct {
	// Delimiter splits the captured text into segments. Defaults to "/".
	Delimiter string `yaml:"delimiter"`
	// DropSet lists segments removed unconditionally before rule matching —
	// index files, framework markers ("index", "+page", "+server", "").
	DropSet []string `yaml:"drop_set"`
	// Rules apply in order; the first one whose Match pattern fits a segment
	// wins. A pattern has at most one "*" wildcard, matched against the whole
	// segment (`[*]` matches "[id]", capturing "id"). Action ("drop" |
	// "unresolved") short-circuits Replace; "unresolved" abandons the whole
	// path (an optional catch-all or parallel route the dialect table cannot
	// map), "drop" removes just this segment. Otherwise Replace's "$1" is
	// substituted with the wildcard's capture ("[*]" → ":$1", "[...*]" → "*").
	Rules []PathTransformRule `yaml:"rules"`
}

// PathTransformRule is one ordered rewrite in a PathTransformSpec.
type PathTransformRule struct {
	Match   string `yaml:"match"`
	Replace string `yaml:"replace"`
	Action  string `yaml:"action"` // "" (replace) | "drop" | "unresolved"
}

// pathTransformUnresolvedMarker is what applyPathTransform returns when a
// rule's action is "unresolved" — the same non-match convention every other
// verb uses ("" for a string verb), so a `.dl` rule tests it the same way.
const pathTransformUnresolvedMarker = ""

// applyPathTransform is the verb body: split text on spec's delimiter, drop
// noise segments, rewrite the rest, and join the survivors into a route key.
// A nil spec (malformed YAML the loader should have rejected first) is the
// documented non-match: "".
func applyPathTransform(text string, spec *PathTransformSpec) []verbVal {
	if spec == nil {
		return []verbVal{{Str: pathTransformUnresolvedMarker, Kind: factpipe.AtomStr}}
	}
	delim := spec.Delimiter
	if delim == "" {
		delim = "/"
	}
	drop := make(map[string]bool, len(spec.DropSet))
	for _, d := range spec.DropSet {
		drop[d] = true
	}
	var out []string
	for _, seg := range strings.Split(text, delim) {
		if drop[seg] {
			continue
		}
		mapped, action, matched := matchPathSegment(seg, spec.Rules)
		if !matched {
			out = append(out, seg)
			continue
		}
		switch action {
		case "unresolved":
			return []verbVal{{Str: pathTransformUnresolvedMarker, Kind: factpipe.AtomStr}}
		case "drop":
			continue
		default:
			out = append(out, mapped)
		}
	}
	if len(out) == 0 {
		return []verbVal{{Str: "/", Kind: factpipe.AtomStr}}
	}
	return []verbVal{{Str: "/" + strings.Join(out, "/"), Kind: factpipe.AtomStr}}
}

// matchPathSegment applies the first rule whose Match pattern fits seg.
func matchPathSegment(seg string, rules []PathTransformRule) (mapped, action string, matched bool) {
	for _, r := range rules {
		cap, ok := matchGlob(seg, r.Match)
		if !ok {
			continue
		}
		if r.Action != "" {
			return "", r.Action, true
		}
		return strings.ReplaceAll(r.Replace, "$1", cap), "", true
	}
	return "", "", false
}

// matchGlob matches seg against a pattern with at most one "*" wildcard,
// returning the substring the wildcard captured. A pattern with no "*"
// requires an exact match and captures nothing.
func matchGlob(seg, pattern string) (capture string, ok bool) {
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return "", seg == pattern
	}
	prefix, suffix := pattern[:i], pattern[i+1:]
	if len(seg) < len(prefix)+len(suffix) ||
		!strings.HasPrefix(seg, prefix) || !strings.HasSuffix(seg, suffix) {
		return "", false
	}
	return seg[len(prefix) : len(seg)-len(suffix)], true
}
