package workspace

import (
	"os"
	"path/filepath"
)

// FrameworkHint describes a detected framework in a directory.
type FrameworkHint struct {
	Name       string
	Language   string
	Confidence float64 // 0.0-1.0
}

// DetectFrameworks inspects path and returns likely frameworks in use.
// It looks for go.mod, package.json, Gemfile, etc.
func DetectFrameworks(path string) ([]FrameworkHint, error) {
	var hints []FrameworkHint

	checks := []struct {
		file      string
		framework string
		language  string
	}{
		{"go.mod", "go-module", "go"},
		{"package.json", "node", "javascript"},
		{"Gemfile", "bundler", "ruby"},
		{"requirements.txt", "pip", "python"},
		{"Cargo.toml", "cargo", "rust"},
	}

	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(path, c.file)); err == nil {
			hints = append(hints, FrameworkHint{
				Name:       c.framework,
				Language:   c.language,
				Confidence: 1.0,
			})
		}
	}

	return hints, nil
}
