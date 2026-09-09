package artifact

import (
	"regexp"
	"strings"
)

// Normalizer maps a raw leaf (or a raw fact value) to its canonical form. ok is
// false when the input is not a candidate for this fact kind at all — e.g. a
// string that is not an absolute path is not a candidate endpoint. A chain
// stops at the first !ok.
type Normalizer func(string) (out string, ok bool)

var normalizers = map[string]Normalizer{
	// case_fold lower-cases. Used by fact kinds whose relation is
	// case-insensitive (queue names); NOT in the endpoint chain, which is
	// case-sensitive by precedent.
	"case_fold": func(s string) (string, bool) { return strings.ToLower(s), true },

	// query_strip drops everything from the first '?' or '#'.
	"query_strip": func(s string) (string, bool) {
		if i := strings.IndexAny(s, "?#"); i >= 0 {
			s = s[:i]
		}
		return s, true
	},

	// param_wildcard collapses every path-parameter spelling to '*':
	// ${x}, :x, {x}, <x>.
	"param_wildcard": func(s string) (string, bool) {
		s = phDollar.ReplaceAllString(s, "*")
		s = phColon.ReplaceAllString(s, "*")
		s = phBrace.ReplaceAllString(s, "*")
		s = phAngle.ReplaceAllString(s, "*")
		return s, true
	},

	// trim_slash collapses repeated slashes and strips a trailing one.
	"trim_slash": func(s string) (string, bool) {
		s = slashRun.ReplaceAllString(s, "/")
		if len(s) > 1 {
			s = strings.TrimSuffix(s, "/")
		}
		return s, true
	},

	// require_abs_path rejects anything not beginning with '/' after the
	// preceding steps — this is what makes "relative/path" and
	// "https://host/x" non-candidates.
	"require_abs_path": func(s string) (string, bool) {
		if !strings.HasPrefix(s, "/") {
			return "", false
		}
		return s, true
	},

	// url_to_path reduces an absolute URL to its path component, so a leaf
	// written as "https://svc/api/x" still corroborates "/api/x". A bare path
	// passes through unchanged.
	"url_to_path": func(s string) (string, bool) {
		if m := absURL.FindStringSubmatch(s); m != nil {
			return m[1], true
		}
		return s, true
	},
}

var (
	phDollar = regexp.MustCompile(`\$\{[^}]*\}`)
	phColon  = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`)
	phBrace  = regexp.MustCompile(`\{[^}]*\}`)
	phAngle  = regexp.MustCompile(`<[^>]*>`)
	slashRun = regexp.MustCompile(`/{2,}`)
	absURL   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/]+(/[^?#]*)`)
)

// normChain resolves names to functions once. An unknown name yields a chain
// that rejects everything, and names the offender — a mapping bug should be
// loud, not silent.
type normChain struct {
	fns []Normalizer
	bad string
}

func resolveChain(names []string) normChain {
	var c normChain
	for _, n := range names {
		fn, ok := normalizers[n]
		if !ok {
			c.bad = n
			c.fns = nil
			return c
		}
		c.fns = append(c.fns, fn)
	}
	return c
}

// apply runs the chain. ok=false means "not a candidate leaf".
func (c normChain) apply(s string) (string, bool) {
	if c.bad != "" {
		return "", false
	}
	for _, fn := range c.fns {
		var ok bool
		if s, ok = fn(s); !ok {
			return "", false
		}
	}
	return s, true
}
