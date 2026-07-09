package deps

import (
	"os"
	"regexp"
	"strings"
)

// gemSpecRe matches exact-version spec lines in Gemfile.lock's GEM specs
// section: exactly 4 spaces of indent, `name (1.2.3)`. Transitive dependency
// listings are indented 6 spaces and have version *ranges*, not exact pins.
var gemSpecRe = regexp.MustCompile(`^ {4}([A-Za-z0-9_.-]+) \(([^)<>=~ ]+)\)$`)

// resolveGemfileLock reads exact resolved gem versions from Gemfile.lock.
// All gems in the lockfile (direct and transitive) are recorded; Bundler
// does not distinguish dev-only gems in the lock, so kind is always prod.
func resolveGemfileLock(path string) ([]Dependency, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []Dependency
	inSpecs := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		switch trimmed {
		case "  specs:":
			inSpecs = true
			continue
		case "", "GEM", "GIT", "PATH", "PLATFORMS", "DEPENDENCIES", "BUNDLED WITH", "RUBY VERSION":
			if !strings.HasPrefix(trimmed, " ") {
				inSpecs = false
			}
			continue
		}
		if !inSpecs {
			continue
		}
		if m := gemSpecRe.FindStringSubmatch(trimmed); m != nil {
			out = append(out, Dependency{
				Ecosystem: EcosystemRuby,
				Name:      m[1],
				Version:   m[2],
				Kind:      KindProd,
			})
		}
	}
	return out, nil
}
