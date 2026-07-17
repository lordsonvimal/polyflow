package contract

import (
	"fmt"
	"regexp"
	"strings"
)

var normRegistry = map[string]Normalizer{}

// RegisterNormalizer wires a named transform (from init()). Load fails fast
// on an unknown name — a YAML typo must never silently no-op.
func RegisterNormalizer(name string, fn Normalizer) {
	if _, exists := normRegistry[name]; exists {
		panic(fmt.Sprintf("contract: normalizer %q already registered", name))
	}
	normRegistry[name] = fn
}

// NormalizerByName returns the registered normalizer with the given name, or
// nil if not found. Intended for use in tests and diagnostics.
func NormalizerByName(name string) Normalizer {
	return normRegistry[name]
}

func init() {
	RegisterNormalizer("param_wildcard", normParamWildcard)
	RegisterNormalizer("query_strip", normQueryStrip)
	RegisterNormalizer("quote_strip", normQuoteStrip)
	RegisterNormalizer("case_fold", normCaseFold)
	RegisterNormalizer("trim_slash", normTrimSlash)
	RegisterNormalizer("base_url_strip", normBaseURLStrip)
	RegisterNormalizer("shared_anchor_guard", normSharedAnchorGuard)
	RegisterNormalizer("url_to_path", normURLToPath)
}

var (
	reParamColon = regexp.MustCompile(`:[^/]+`)
	reParamBrace = regexp.MustCompile(`\{[^}]+\}`)
	reParamRegex = regexp.MustCompile(`\[[^\]]+\][+*?]?`)
)

// normParamWildcard replaces path parameter segments with *.
// Handles :id, {id}, and [pattern]+/*/? styles.
func normParamWildcard(value string, _ NormalizeEnv) string {
	p := reParamColon.ReplaceAllString(value, "*")
	p = reParamBrace.ReplaceAllString(p, "*")
	p = reParamRegex.ReplaceAllString(p, "*")
	return p
}

// normQueryStrip removes the query string from a URL or path.
func normQueryStrip(value string, _ NormalizeEnv) string {
	if i := strings.Index(value, "?"); i >= 0 {
		return value[:i]
	}
	return value
}

// normQuoteStrip removes surrounding single-quotes, double-quotes, or backticks.
func normQuoteStrip(value string, _ NormalizeEnv) string {
	if len(value) >= 2 {
		c := value[0]
		if (c == '"' || c == '\'' || c == '`') && value[len(value)-1] == c {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// normCaseFold lowercases the value.
func normCaseFold(value string, _ NormalizeEnv) string {
	return strings.ToLower(value)
}

// normTrimSlash removes trailing slashes, preserving "/" as the root.
func normTrimSlash(value string, _ NormalizeEnv) string {
	p := strings.TrimRight(value, "/")
	if p == "" {
		return "/"
	}
	return p
}

// normBaseURLStrip strips the workspace-declared base_url prefix from a
// consumer path when the (FromService, ToService) pair has a base_url link.
// Applied to consumer key fields so both producer and consumer resolve to the
// same bare path for matching.
func normBaseURLStrip(value string, env NormalizeEnv) string {
	for _, link := range env.Links {
		if link.From == env.FromService && link.To == env.ToService && link.BaseURL != "" {
			if strings.HasPrefix(value, link.BaseURL) {
				stripped := value[len(link.BaseURL):]
				if stripped == "" {
					return "/"
				}
				return stripped
			}
			return value
		}
	}
	return value
}

// normSharedAnchorGuard returns "" when the value (after param_wildcard) is
// entirely wildcards, preventing fully-wildcarded paths from entering
// wildcard_anchored matching and spuriously matching every same-shape handler.
func normSharedAnchorGuard(value string, _ NormalizeEnv) string {
	if value == "" {
		return ""
	}
	segs := splitPath(value)
	if len(segs) == 0 {
		return value
	}
	for _, seg := range segs {
		if seg != "*" {
			return value
		}
	}
	return "" // all wildcards: block matching
}

// normURLToPath extracts the path from an absolute URL. Non-URL, non-path
// values (e.g. an HTTP method "GET") are returned unchanged so the normalizer
// is a no-op when applied to non-path key fields.
func normURLToPath(value string, _ NormalizeEnv) string {
	if i := strings.Index(value, "://"); i >= 0 {
		rest := value[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j:]
		}
		return "/"
	}
	return value
}

// NormalizeFields applies the named normalizer chain to each field independently
// and returns the space-joined channel key.  This is the canonical way for
// non-static evidence providers (F.1+) to produce a join key that matches the
// keys the engine computes from static call sites.
//
// Example: NormalizeFields([]string{"GET", "/games/{gameID}"},
//
//	[]string{"case_fold", "param_wildcard", "trim_slash"}, NormalizeEnv{})
//
// → "get /games/*"
func NormalizeFields(fields []string, normNames []string, env NormalizeEnv) (string, error) {
	norms := make([]Normalizer, 0, len(normNames))
	for _, name := range normNames {
		fn := NormalizerByName(name)
		if fn == nil {
			return "", fmt.Errorf("contract: unknown normalizer %q", name)
		}
		norms = append(norms, fn)
	}
	return strings.Join(applyNormsToFields(fields, norms, env), " "), nil
}

// splitPath splits a path (or path-prefixed key) on "/" after trimming edges.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// hasLiteralSegment reports whether the value has at least one non-wildcard
// segment when split by "/". Used to guard wildcard_anchored matching.
func hasLiteralSegment(value string) bool {
	for _, seg := range splitPath(value) {
		if seg != "*" {
			return true
		}
	}
	return false
}

// pathMatchesPattern does segment-for-segment matching where "*" on either
// side matches any single non-empty segment. When the candidate key itself
// contains wildcards (e.g. a datastar partial path), at least one concrete
// segment must match — otherwise two routes of different meaning but the same
// shape would spuriously match on wildcards alone.
func pathMatchesPattern(key, pattern string) bool {
	ks := splitPath(key)
	ps := splitPath(pattern)
	if len(ks) != len(ps) {
		return false
	}
	keyHasWild := false
	sharedConcrete := false
	for i := range ks {
		kw := ks[i] == "*"
		pw := ps[i] == "*"
		if kw {
			keyHasWild = true
		}
		if !kw && !pw {
			if ks[i] != ps[i] {
				return false
			}
			sharedConcrete = true
		}
	}
	if keyHasWild && !sharedConcrete {
		return false
	}
	return true
}
