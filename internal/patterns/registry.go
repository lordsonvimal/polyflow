package patterns

import (
	"fmt"
	"sync"
)

// Registry holds all loaded patterns indexed by language and name.
type Registry struct {
	mu       sync.RWMutex
	patterns map[string]map[string]*Pattern // language -> name -> pattern
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		patterns: make(map[string]map[string]*Pattern),
	}
}

// Register adds a pattern to the registry.
func (r *Registry) Register(language string, p *Pattern) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.patterns[language] == nil {
		r.patterns[language] = make(map[string]*Pattern)
	}
	r.patterns[language][p.Name] = p
}

// RegisterFile registers all patterns from a PatternFile.
func (r *Registry) RegisterFile(pf *PatternFile) {
	for i := range pf.Patterns {
		r.Register(pf.Language, &pf.Patterns[i])
	}
}

// Get retrieves a pattern by language and name.
func (r *Registry) Get(language, name string) (*Pattern, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lang, ok := r.patterns[language]
	if !ok {
		return nil, fmt.Errorf("no patterns for language %q", language)
	}
	p, ok := lang[name]
	if !ok {
		return nil, fmt.Errorf("pattern %q not found for language %q", name, language)
	}
	return p, nil
}

// List returns all patterns for the given language.
func (r *Registry) List(language string) []*Pattern {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lang := r.patterns[language]
	out := make([]*Pattern, 0, len(lang))
	for _, p := range lang {
		out = append(out, p)
	}
	return out
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
