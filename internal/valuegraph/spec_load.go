package valuegraph

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// specFS holds the built-in binding specs, one YAML file per language. The glob
// keeps every grammar name out of this file — which is the point: the engine
// and its loader know nothing about any specific language.
//
//go:embed *.yaml
var specFS embed.FS

// LoadSpec parses a binding spec from its YAML source. An unknown top-level key
// is an error rather than a silent no-op: a misspelled `binding:` that resolved
// to nothing would look identical to a language that genuinely has no bindings.
// The spec is validated before it is returned, so a structural mistake surfaces
// here and not as an unresolvable expression at run time.
func LoadSpec(data []byte) (*Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var s Spec
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("valuegraph: parse spec: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// EmbeddedSpec returns a built-in spec by language name or by one of its
// declared grammars, so a language and each of its grammar dialects all resolve
// to the same spec. It is the runtime entry point; tests that need a throwaway
// spec build a *Spec literal directly.
func EmbeddedSpec(language string) (*Spec, error) {
	want := strings.ToLower(strings.TrimSpace(language))

	names, err := fs.Glob(specFS, "*.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := specFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		s, err := LoadSpec(raw)
		if err != nil {
			return nil, fmt.Errorf("valuegraph: %s: %w", name, err)
		}
		if strings.EqualFold(s.Language, want) {
			return s, nil
		}
		for _, g := range s.Grammars {
			if strings.EqualFold(g, want) {
				return s, nil
			}
		}
	}
	return nil, fmt.Errorf("valuegraph: no embedded spec for %q", language)
}
