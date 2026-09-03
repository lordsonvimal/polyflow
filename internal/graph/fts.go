package graph

import (
	"strings"
	"unicode"
)

// FTS5PrefixQuery converts an arbitrary user query into a safe FTS5 MATCH
// expression: everything that is not a letter, digit, or underscore is replaced
// with a space, each resulting token gets a trailing '*' for prefix matching,
// and the tokens are OR-joined so any term match returns a result.
//
// This uses an allowlist rather than a blocklist of special characters, so no
// unhandled punctuation can ever reach the FTS5 parser. Raw inputs such as
// "user's checkout-flow" or the AMQP routing key "build.submit" are otherwise
// syntax errors in FTS5 (`fts5: syntax error near "."`). The default unicode61
// tokenizer used by nodes_fts/entities_fts splits indexed content on these same
// separators, so splitting the query the same way is what makes term matches
// line up (bug-class rule 6 — captured text is sanitised before an engine).
//
// Returns "" for a query with no usable tokens; callers must treat that as "no
// results" rather than passing it to MATCH (an empty MATCH string errors).
func FTS5PrefixQuery(query string) string {
	var b strings.Builder
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	tokens := strings.Fields(b.String())
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = t + "*"
	}
	return strings.Join(parts, " OR ")
}

// FTS5IdentPhraseQuery builds a phrase-anchored FTS5 MATCH expression: the
// query's word tokens must occur as a CONTIGUOUS run in the indexed text, so
// "do build" matches a label ending ".../do-build" but not one merely
// containing "docker-builds" and "do-cancel" scattered across a path. Returns
// "" for a query that yields fewer than two tokens — a single token has no
// phrase to anchor and FTS5PrefixQuery already covers it.
//
// This is the retrieval-side complement to the ranker's whole-query coverage
// check: an OR-of-prefixes query buries a compound symbol like "do-build"
// under every node sharing one common word ("build"), often past the fetch
// limit, so the ranker never gets to see it. The phrase query pulls it back
// into the candidate pool.
func FTS5IdentPhraseQuery(query string) string {
	var b strings.Builder
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	tokens := strings.Fields(b.String())
	if len(tokens) < 2 {
		return ""
	}
	return `"` + strings.Join(tokens, " ") + `"`
}
