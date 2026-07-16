package contract

import (
	"encoding/json"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
)

// KeyWalker enumerates literal alternatives for one producer key
// expression in one language. Implementations honor the shared bounds
// (≤8 branches, depth ≤2, all-literal) and never partially enumerate.
type KeyWalker interface {
	Language() string // matches parser.Parser.Language()
	// WalkKey inspects the tree-sitter node holding a key-field value.
	// Returns (candidates, dynamic): len(candidates) >= 2 => emit
	// key_candidates meta; dynamic=true => emit key_dynamic meta +
	// ledger entry; (1 literal, false) => plain static key, no meta.
	WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool)
}

// ConstResolver resolves same-service constant references (shape b).
// Returns ("", false) for anything reassigned or non-literal.
type ConstResolver func(name string) (string, bool)

var (
	keyWalkerRegistry = map[string]KeyWalker{}
	noOpWalkers       = map[string]bool{}
)

// RegisterKeyWalker wires a walker (from init()), keyed by Language().
func RegisterKeyWalker(w KeyWalker) {
	lang := w.Language()
	if _, exists := keyWalkerRegistry[lang]; exists {
		panic(fmt.Sprintf("contract: KeyWalker for language %q already registered", lang))
	}
	keyWalkerRegistry[lang] = w
}

// RegisterNoOpKeyWalker registers a walker that is explicitly no-op:
// the language's key fields are always static literals (e.g. HTML attributes).
// Distinguishes "considered, not needed" from "forgotten" in doctor output.
func RegisterNoOpKeyWalker(w KeyWalker) {
	noOpWalkers[w.Language()] = true
	RegisterKeyWalker(w)
}

// KeyWalkerFor returns the walker for a language, or nil. Callers treat
// nil as "literal-only recognition" — and doctor reports it as MISSING.
func KeyWalkerFor(lang string) KeyWalker {
	return keyWalkerRegistry[lang]
}

// KeyWalkerStatus returns the registration status for a language: "yes",
// "no-op", or "MISSING". Used by doctor to render the walker-coverage row.
func KeyWalkerStatus(lang string) string {
	if _, ok := keyWalkerRegistry[lang]; !ok {
		return "MISSING"
	}
	if noOpWalkers[lang] {
		return "no-op"
	}
	return "yes"
}

// WalkerLanguages returns every language that has a registered KeyWalker.
func WalkerLanguages() []string {
	langs := make([]string, 0, len(keyWalkerRegistry))
	for lang := range keyWalkerRegistry {
		langs = append(langs, lang)
	}
	return langs
}

// ParseKeyCandidates parses the key_candidates meta value into a string slice.
// Returns nil for empty or invalid JSON.
func ParseKeyCandidates(s string) []string {
	if s == "" {
		return nil
	}
	var vals []string
	if err := json.Unmarshal([]byte(s), &vals); err != nil {
		return nil
	}
	if len(vals) == 0 {
		return nil
	}
	return vals
}

// MarshalKeyCandidates serializes candidates to the key_candidates meta format.
func MarshalKeyCandidates(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	data, _ := json.Marshal(vals)
	return string(data)
}

// keyWalkerMaxBranches is the branch-count ceiling enforced by all walkers.
const keyWalkerMaxBranches = 8

// keyWalkerMaxDepth is the conditional-nesting depth ceiling.
const keyWalkerMaxDepth = 2

// stripKeyLiteral removes surrounding quotes from a string literal value.
func stripKeyLiteral(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
