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
