package semantic

import "strings"

// This file holds the identifier-shaped-query helpers that let search treat a
// term the caller clearly typed as a symbol name ("do-build", "DoBuild",
// "build.submit") differently from a plain descriptive word ("build"): the
// former may pin an exact hit across separator/case differences and its
// sub-words are used to rank whole-query matches above one-word collisions.

// normalizeIdent collapses an identifier-ish string to a comparison key:
// lowercased, with every separator/punctuation rune removed. "do-build",
// "do_build", "DoBuild" and "do.build" all normalize to "dobuild".
func normalizeIdent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looksLikeIdent reports whether q is a single "identifier-shaped" term the
// caller typed deliberately — one whitespace-free token carrying an internal
// separator or a camelCase hump. A plain descriptive word ("build",
// "payment") is not: it must not get the identifier-exact floor, or every
// symbol sharing that common stem floods rank 0 (see identExact / rrfFuse).
func looksLikeIdent(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" || strings.ContainsAny(q, " \t\n") {
		return false
	}
	if strings.ContainsAny(q, "-_./:") {
		return true
	}
	hasLower, hasUpper := false, false
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		}
	}
	return hasLower && hasUpper
}

// identTokens splits s into lowercased word tokens, breaking on separator
// punctuation and camelCase humps: "DoBuild" and "do_build" both yield
// ["do", "build"]; "POST /api/x/do_build" yields ["post","api","x","do","build"].
func identTokens(s string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	var prev rune
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if prev >= 'a' && prev <= 'z' {
				flush()
			}
			cur.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			cur.WriteRune(r)
		default:
			flush()
		}
		prev = r
	}
	flush()
	return toks
}

// identExact matches an identifier-shaped query against a label ignoring case
// and separator punctuation, so "do-build" pins the http_handler
// "POST /api/x/do_build" or the method "DoBuild" that the case- and
// separator-sensitive isExact misses. For a "VERB /a/b/c" style label the
// last path segment is also compared, since that segment is the action name.
// Gated on looksLikeIdent so a plain word ("build") can never claim the
// exact-match floor this way.
func identExact(label, q string) bool {
	if label == "" || !looksLikeIdent(q) {
		return false
	}
	nq := normalizeIdent(q)
	if len(nq) < 3 {
		return false
	}
	if normalizeIdent(label) == nq {
		return true
	}
	if i := strings.LastIndexByte(label, '/'); i >= 0 && i+1 < len(label) {
		if normalizeIdent(label[i+1:]) == nq {
			return true
		}
	}
	return false
}

// labelCoversQuery reports whether label contains the *whole* query, not just
// one word it happens to share.
//
// For an identifier-shaped query ("do-build") the query's normalized form
// must appear as a CONTIGUOUS run in the normalized label. This is the fix
// for "do-build" ranking "POST /docker-builds/:id/do-cancel" as a strong hit:
// that label only has "build" (via "builds") and "do" (via "do-cancel")
// scattered across the path — normalizeIdent gives "...dockerbuildsbuildid
// docancel", which does not contain "dobuild" — whereas a real
// "POST /x/do_build" normalizes to "...dobuild" and does.
//
// For a plain multi-word query ("cancel build order") every word must still
// appear as a word of the label (coversAllQueryWords).
func labelCoversQuery(label, q string) bool {
	if label == "" {
		return false
	}
	if looksLikeIdent(q) {
		nq := normalizeIdent(q)
		return len(nq) >= 4 && strings.Contains(normalizeIdent(label), nq)
	}
	return coversAllQueryWords(label, identTokens(q))
}

// LabelRelevance scores how directly label answers query q, for the FTS-only
// (kind-filtered) search path that does not run the hybrid ranker:
//
//	2 — identifier-exact (label, or a route's last segment, is the query)
//	1 — the label contains the whole query
//	0 — only incidental shared-token overlap (what BM25 alone rewards)
//
// Callers stable-sort their FTS hits by this descending so a real match
// floats above lexical cousins ("do-build" vs "do-cancel").
func LabelRelevance(label, q string) int {
	switch {
	case identExact(label, q):
		return 2
	case labelCoversQuery(label, q):
		return 1
	default:
		return 0
	}
}

// IdentifierQuery reports whether q is a single symbol-shaped term (an
// exported view of looksLikeIdent for the server's FTS-only path).
func IdentifierQuery(q string) bool { return looksLikeIdent(q) }

// QueryTargetsTest reports whether the caller explicitly asked about test code
// ("cancel build spec", "route registration test"). When true, search must not
// demote test-file hits below production ones — the caller wants them.
func QueryTargetsTest(q string) bool {
	for _, t := range identTokens(q) {
		switch t {
		case "test", "tests", "spec", "specs", "testcase", "testcases":
			return true
		}
	}
	return false
}

// coversAllQueryWords reports whether every sub-word of the query appears as a
// word of label — matched exactly (case-insensitive) for short words, or as a
// >=4-char prefix for longer ones. Only meaningful for multi-word queries; a
// one-word query returns false so it never triggers the coverage rank tier.
// "do-build" covers ["do","build"]; label "cancel-build" misses "do" → false,
// label "POST /x/do_build" hits both → true.
func coversAllQueryWords(label string, queryWords []string) bool {
	if len(queryWords) < 2 || label == "" {
		return false
	}
	labelWords := identTokens(label)
	for _, qw := range queryWords {
		found := false
		for _, lw := range labelWords {
			if lw == qw || (len(qw) >= 4 && strings.HasPrefix(lw, qw)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
