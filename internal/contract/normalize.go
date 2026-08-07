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
	RegisterNormalizer("dynamic_host_strip", normDynamicHostStrip)
	RegisterNormalizer("amqp_topic_wildcard", normAMQPTopicWildcard)
}

var (
	reParamColon = regexp.MustCompile(`:[^/]+`)
	reParamBrace = regexp.MustCompile(`\{[^}]+\}`)
	reParamRegex = regexp.MustCompile(`\[[^\]]+\][+*?]?`)
	// reParamPrintf matches a Go printf verb used as a path parameter in a
	// `fmt.Sprintf("/x/%d", id)` client URL. The verb set is limited to d/s/v
	// (with an optional width, `%02d`) so it can never match a URL-encoded
	// octet `%XX` — the hex-pair form always ends in a hex digit, and d/s/v as
	// a lone trailing verb (`%d/`, `%s?`) is unambiguously not a `%`+2-hex
	// sequence. Extend the verb set only with evidence.
	reParamPrintf = regexp.MustCompile(`%\d*[dsv]`)
	// reInterpSegment matches a whole path segment (a run of non-slash chars)
	// that embeds a JS template-literal interpolation `${…}` — e.g.
	// `${configType}-configs` or a bare `${configId}` from
	// `fetch(`/api/v1/${configType}-configs/${configId}/dependent-apps`)`. The
	// entire segment is collapsed to `*` (not just the `${…}` span): a segment
	// carrying an interpolation is dynamic, and its literal decoration
	// (`-configs`) is a runtime concatenation that cannot be relied on for
	// matching. This lets the client path reduce to `/api/v1/*/*/dependent-apps`
	// and meet the composed handler `/api/v1/exec-configs/:config_id/dependent-apps`
	// on the wildcard tier (X.10c).
	reInterpSegment = regexp.MustCompile(`[^/]*\$\{[^}]*\}[^/]*`)
)

// normParamWildcard replaces path parameter segments with *.
// Handles :id, {id}, [pattern]+/*/? and Go printf-verb (%d/%s/%v) styles, so a
// `fmt.Sprintf`-built client path (`/dsw/roles/%d/update`) reduces to the same
// `/dsw/roles/*/update` shape a `:id` handler does and the two match.
func normParamWildcard(value string, _ NormalizeEnv) string {
	p := reInterpSegment.ReplaceAllString(value, "*")
	p = reParamColon.ReplaceAllString(p, "*")
	p = reParamBrace.ReplaceAllString(p, "*")
	p = reParamRegex.ReplaceAllString(p, "*")
	p = reParamPrintf.ReplaceAllString(p, "*")
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

// normDynamicHostStrip drops a single leading "*" segment (the dynamic
// scheme/host/base produced by X.1b template reconstruction — e.g.
// "*/api/v1/builds/*" from fmt.Sprintf("%s/api/v1/builds/%s", base, id))
// so it aligns with the handler side's "/api/v1/builds/*". No-op when the
// value has no leading wildcard segment — channel/topic keys (pusher, amqp,
// kafka) never have one, so it is safe to add to those normalizer chains
// too.
//
// X.5: a cross-repo templated call whose base resolves via a workspace Link
// to a target service mounted at a non-empty base_url needs no special
// handling here — this normalizer always reduces to the bare path, and
// normBaseURLStrip (placed after it in contracts/http.yaml's chain) is what
// consults env.Links to strip that base_url from the handler side, so the
// two compose correctly with zero engine changes (verified end to end by
// TestEngine_DynamicHostStrip_ReconciledWithBaseURL in
// internal/contract/engine_test.go).
func normDynamicHostStrip(value string, _ NormalizeEnv) string {
	if strings.HasPrefix(value, "*/") {
		return value[1:]
	}
	return value
}

// normAMQPTopicWildcard collapses every wildcard segment of a dot-separated AMQP
// routing key to "*", so the two sides of a topic binding meet: a producer key
// reconstructed from `fmt.Sprintf("container.%s", ev)` reduces to `container.*`
// (X.11), while the consumer binds `container.#`. Both become `container.*`.
// Literal segments are untouched (`logs.build.*` stays `logs.build.*`,
// `build.submit` stays `build.submit`), and a value with no wildcard segment —
// every exchange name, and any non-AMQP field this chain is applied to — is
// returned unchanged.
//
// AMQP's two wildcards differ in arity (`*` is one segment, `#` is zero or more),
// so collapsing them together is a deliberate widening: it can join a producer to
// a binding whose pattern would not have matched at runtime. That is the
// recall-first trade, and the resulting edge is never more than `inferred`.
func normAMQPTopicWildcard(value string, _ NormalizeEnv) string {
	if !strings.ContainsAny(value, "#*") {
		return value
	}
	segs := strings.Split(value, ".")
	for i, s := range segs {
		if s == "#" || s == "*" {
			segs[i] = "*"
		}
	}
	return strings.Join(segs, ".")
}

// topicIsOpen reports whether a routing key constrains nothing: it is empty
// (a fanout publish or an unresolvable key) or every one of its segments is a
// wildcard. Such a key carries no routing information, which is what admits the
// exchange_only match tier.
func topicIsOpen(key string) bool {
	if key == "" {
		return true
	}
	for _, seg := range strings.Split(key, ".") {
		if seg != "*" && seg != "#" {
			return false
		}
	}
	return true
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
