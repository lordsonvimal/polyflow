package patterns

import (
	"sync"
)

// Registry holds all loaded patterns indexed by language.
// Patterns are stored as a slice, not a name-keyed map: multiple query
// variants may legitimately share one pattern name (e.g. the two
// goroutine_call queries for identifier vs selector call targets).
type Registry struct {
	mu       sync.RWMutex
	patterns map[string][]*Pattern // language -> patterns in registration order
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		patterns: make(map[string][]*Pattern),
	}
}

// Register adds a pattern to the registry.
func (r *Registry) Register(language string, p *Pattern) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.patterns[language] = append(r.patterns[language], p)
}

// RegisterFile registers all patterns from a PatternFile.
func (r *Registry) RegisterFile(pf *PatternFile) {
	for i := range pf.Patterns {
		r.Register(pf.Language, &pf.Patterns[i])
	}
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
