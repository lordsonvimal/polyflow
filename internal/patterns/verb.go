package patterns

import "strings"

// httpVerbs is the closed, spec-fixed set of HTTP methods that appear as
// language-level constants or symbols. Hard-coded rather than resolved: the
// set cannot grow without an RFC, and a general cross-package constant
// resolver is far larger machinery than this one closed enum warrants.
var httpVerbs = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "CONNECT": true, "OPTIONS": true, "TRACE": true,
}

// normalizeHTTPVerb resolves a captured HTTP method expression to its bare
// verb, upper-cased. It recognizes three source forms and declines everything
// else:
//
//	http.MethodGet   (Go: net/http's constants, the idiomatic spelling)
//	:get             (Ruby: a symbol, e.g. `RestClient::Request.execute(method: :get)`)
//	get / GET        (a plain literal, normalized for case)
//
// Declining matters as much as resolving. The captured text is stored verbatim
// in meta["method"], and the contract engine's case_fold normalizer reduces
// `http.MethodGet` to `http.methodget` — which equals no handler's verb, and
// is not *empty*, so http.yaml's method_fallback (which only fires on an empty
// method) never retries it. The producer then falls to `unmatched:
// unknown_edge` and emits a junk edge to the synthetic `unresolved` node. A
// value this function declines is left exactly as it was, so an unrecognized
// expression keeps today's behavior rather than being guessed at.
//
// One narrow, pattern-scoped exception lives in matcher.go (RC.1): Ruby's
// `RestClient::Request.execute(method: method, ...)` keyword-argument form
// has a grammar ambiguity this function cannot see from raw text alone (a
// bare identifier value vs. a literal symbol both arrive as plain source
// text), so the caller blanks the decline there instead of keeping it. That
// is a caller-level decision scoped to two pattern names, not a change to
// this function's own contract.
//
// The Go operand is matched by *name*, not by import path: the tree-sitter
// matcher has no type information here, and `http` is the overwhelmingly
// dominant alias for net/http. A package aliased to `http` that defined its
// own `MethodFoo` would have to name it after a real HTTP verb to reach the
// verb set at all, in which case the resolved string is still that verb — a
// benign failure mode.
func normalizeHTTPVerb(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	// Ruby symbol: `:get`. Also covers a qualified Go constant once the
	// package qualifier is dropped below, so order does not matter.
	v = strings.TrimPrefix(v, ":")
	if i := strings.LastIndex(v, "."); i >= 0 {
		// `http.MethodGet` → `MethodGet`. Only a `Method`-prefixed field is a
		// verb constant; a bare selector like `c.method` must not resolve.
		field := v[i+1:]
		if !strings.HasPrefix(field, "Method") {
			return "", false
		}
		v = strings.TrimPrefix(field, "Method")
	}
	v = strings.ToUpper(v)
	if !httpVerbs[v] {
		return "", false
	}
	return v, true
}
