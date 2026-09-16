package patterns

import (
	"strings"
	"sync"
)

// ownerSep namespaces a pattern name to the framework that registered it
// (XM.1, docs/factpipe-cross-framework-matching-plan.md) so several
// frameworks' patterns can share one Registry/TreeSitterMatcher for a
// language — one combined tree-sitter query per (Run call, language)
// instead of one per framework — while a match can still be routed back to
// its owning Framework. Riding inside PatternName (rather than a new
// Pattern.Owner field) means this touches nothing outside this file and
// pipeline.go: Pattern/MatchResult's shape stays exactly what
// internal/pluginloader, `polyflow pattern synth`, and every
// TestPatternFixtures-style fixture test already expect.
const ownerSep = "::"

// Registry holds all loaded patterns indexed by language.
// Patterns are stored as a slice, not a name-keyed map: multiple query
// variants may legitimately share one pattern name (e.g. the two
// goroutine_call queries for identifier vs selector call targets).
type Registry struct {
	mu       sync.RWMutex
	patterns map[string][]*Pattern // language -> patterns in registration order

	// reflectMethods carries each PatternFile's ReflectDispatchedMethods
	// entries with that file's Package/VersionRange gate copied down, one
	// gate struct per declared method name — mirrors how RegisterFile copies
	// the same gate onto each *Pattern, so ForService can filter both lists
	// through the identical gateSatisfied check.
	reflectMethods map[string][]reflectMethodGate // language -> gated method names
}

// reflectMethodGate is one reflect_dispatched_methods entry plus the
// file-level gate it inherited.
type reflectMethodGate struct {
	Method       string
	Package      string
	VersionRange string
	PathPrefix   string
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		patterns:       make(map[string][]*Pattern),
		reflectMethods: make(map[string][]reflectMethodGate),
	}
}

// Register adds a pattern to the registry.
func (r *Registry) Register(language string, p *Pattern) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns[language] = append(r.patterns[language], p)
}

// RegisterFile registers all patterns from a PatternFile, copying the
// file-level version gate down onto each pattern (and onto each declared
// ReflectDispatchedMethods entry).
func (r *Registry) RegisterFile(pf *PatternFile) {
	for i := range pf.Patterns {
		pf.Patterns[i].Package = pf.Package
		pf.Patterns[i].VersionRange = pf.VersionRange
		pf.Patterns[i].Grammars = pf.Grammars
		r.Register(pf.Language, &pf.Patterns[i])
	}
	if len(pf.ReflectDispatchedMethods) > 0 {
		r.mu.Lock()
		for _, m := range pf.ReflectDispatchedMethods {
			r.reflectMethods[pf.Language] = append(r.reflectMethods[pf.Language], reflectMethodGate{
				Method: m, Package: pf.Package, VersionRange: pf.VersionRange,
				PathPrefix: pf.ReflectDispatchedPathPrefix,
			})
		}
		r.mu.Unlock()
	}
}

// RegisterFileOwned is RegisterFile, but every pattern is registered under
// an owner-namespaced name ("<owner>::<name>") instead of its bare name, so
// several frameworks' patterns can coexist in one Registry without a
// PatternName collision (XM.1's shared cross-framework matcher). It copies
// each Pattern rather than mutating pf.Patterns in place: pf is the same
// *PatternFile a process-wide singleton Framework holds and reuses across
// every concurrent pipeline.Run call, so renaming in place would
// double-prefix on the next call and corrupt every other reader of
// fw.Patterns (pipeline.EvalOnce, Tier PS's synthesizer).
func (r *Registry) RegisterFileOwned(pf *PatternFile, owner string) {
	owned := make([]Pattern, len(pf.Patterns))
	copy(owned, pf.Patterns)
	for i := range owned {
		owned[i].Name = owner + ownerSep + owned[i].Name
		owned[i].Package = pf.Package
		owned[i].VersionRange = pf.VersionRange
		owned[i].Grammars = pf.Grammars
		r.Register(pf.Language, &owned[i])
	}
	if len(pf.ReflectDispatchedMethods) > 0 {
		r.mu.Lock()
		for _, m := range pf.ReflectDispatchedMethods {
			r.reflectMethods[pf.Language] = append(r.reflectMethods[pf.Language], reflectMethodGate{
				Method: m, Package: pf.Package, VersionRange: pf.VersionRange,
				PathPrefix: pf.ReflectDispatchedPathPrefix,
			})
		}
		r.mu.Unlock()
	}
}

// SplitOwner reverses RegisterFileOwned's namespacing on a MatchResult's
// PatternName, returning the owner and the original bare pattern name. ok is
// false for a name that was never owner-namespaced (any Registry populated
// via plain RegisterFile instead).
func SplitOwner(patternName string) (owner, name string, ok bool) {
	i := strings.Index(patternName, ownerSep)
	if i < 0 {
		return "", patternName, false
	}
	return patternName[:i], patternName[i+len(ownerSep):], true
}

// ReflectDispatchedMethods returns the set of method names for language that
// this (already service-filtered, via ForService) registry's pattern files
// declared as reflect-dispatched. Call on the result of ForService, not the
// unfiltered DefaultRegistry/EmbeddedRegistry — an unfiltered registry has
// not applied the package/version gate yet.
func (r *Registry) ReflectDispatchedMethods(language string) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool, len(r.reflectMethods[language]))
	for _, g := range r.reflectMethods[language] {
		out[g.Method] = true
	}
	return out
}

// ReflectDispatchedPathPrefixes returns, for every reflect-dispatched method
// name in language that declared a ReflectDispatchedPathPrefix, that prefix
// (a substring the node's file path must contain for the exclusion to
// apply). A method with no entry here has no path restriction. Call on the
// result of ForService, same as ReflectDispatchedMethods.
func (r *Registry) ReflectDispatchedPathPrefixes(language string) map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string)
	for _, g := range r.reflectMethods[language] {
		if g.PathPrefix != "" {
			out[g.Method] = g.PathPrefix
		}
	}
	return out
}

// AllReflectDispatchedMethods is ReflectDispatchedMethods for every language
// this (already service-filtered) registry has an entry for, keyed by that
// language.
//
// A single service is routinely polyglot — a Rails app with a React
// frontend is one `orion-atlas` service tagged `language: ruby` that also
// indexes `.tsx` files — so a caller keying the lookup off one service-level
// language string (as internal/indexer/indexer.go used to) would silently
// never see a javascript-gated file's reflect_dispatched_methods (e.g.
// patterns/javascript/react.yaml) for that service. Keying per-node by its
// own Language field instead is what stampReflectDispatched needs this for.
func (r *Registry) AllReflectDispatchedMethods() map[string]map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]map[string]bool, len(r.reflectMethods))
	for lang, gs := range r.reflectMethods {
		m := make(map[string]bool, len(gs))
		for _, g := range gs {
			m[g.Method] = true
		}
		out[lang] = m
	}
	return out
}

// AllReflectDispatchedPathPrefixes is ReflectDispatchedPathPrefixes for every
// language this registry has an entry for, keyed by that language — the
// per-language sibling AllReflectDispatchedMethods needs for the same
// polyglot-service reason.
func (r *Registry) AllReflectDispatchedPathPrefixes() map[string]map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]map[string]string, len(r.reflectMethods))
	for lang, gs := range r.reflectMethods {
		m := make(map[string]string)
		for _, g := range gs {
			if g.PathPrefix != "" {
				m[g.Method] = g.PathPrefix
			}
		}
		out[lang] = m
	}
	return out
}

// List returns all patterns for the given language.
func (r *Registry) List(language string) []*Pattern {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*Pattern(nil), r.patterns[language]...)
}

// Languages returns all languages with registered patterns.
func (r *Registry) Languages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	langs := make([]string, 0, len(r.patterns))
	for l := range r.patterns {
		langs = append(langs, l)
	}
	return langs
}
