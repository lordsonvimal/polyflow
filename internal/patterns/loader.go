package patterns

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Capture defines a named capture group within a tree-sitter query.
type Capture struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"` // e.g. "method", "url", "handler"
}

// Pattern is a single named tree-sitter pattern within a file.
type Pattern struct {
	Name     string    `yaml:"name"`
	Query    string    `yaml:"query"`
	Captures []Capture `yaml:"captures"`
}

// PatternFile is the top-level structure of a YAML pattern file.
type PatternFile struct {
	Language string    `yaml:"language"`
	Version  string    `yaml:"version"`
	Patterns []Pattern `yaml:"patterns"`
}

// Load reads and parses all *.yaml pattern files under dir (recursively).
func Load(dir string) ([]*PatternFile, error) {
	var files []*PatternFile

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		pf, err := LoadFile(path)
		if err != nil {
			return fmt.Errorf("load pattern %s: %w", path, err)
		}
		files = append(files, pf)
		return nil
	})

	return files, err
}

// LoadFile reads and parses a single pattern YAML file.
func LoadFile(path string) (*PatternFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pf PatternFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	return &pf, nil
}
