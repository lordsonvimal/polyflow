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

// ExtractConfig describes how to map pattern captures to graph node/edge attributes.
type ExtractConfig struct {
	NodeType   string            `yaml:"node_type"`
	EdgeType   string            `yaml:"edge_type"`
	Attributes map[string]string `yaml:"attributes"` // attribute name -> capture ref like "@method"
}

// Pattern is a single named tree-sitter pattern within a file.
type Pattern struct {
	Name     string              `yaml:"name"`
	Query    string              `yaml:"query"`
	Match    map[string][]string `yaml:"match"`    // capture name -> allowed values (optional filter)
	Extract  ExtractConfig       `yaml:"extract"`
	Captures []Capture           `yaml:"captures"` // kept for backward compat

	// Version gate, copied down from the PatternFile at registration time so
	// per-service filtering and match metadata don't need the file context.
	Package      string `yaml:"-"`
	VersionRange string `yaml:"-"`
}

// PatternFile is the top-level structure of a YAML pattern file.
type PatternFile struct {
	Language string    `yaml:"language"`
	Version  string    `yaml:"version"`
	Patterns []Pattern `yaml:"patterns"`

	// Optional version gate: when both are set, the patterns in this file only
	// activate for services whose resolved version of Package satisfies
	// VersionRange (Masterminds semver syntax, e.g. ">=1.0.0 <2.0.0").
	// When only Package is set, the patterns activate for any version of the
	// package but are skipped entirely if the service does not depend on it.
	Package      string `yaml:"package"`
	VersionRange string `yaml:"version_range"`
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

// DefaultRegistry loads all YAML patterns from patternsDir and returns a populated Registry.
func DefaultRegistry(patternsDir string) (*Registry, error) {
	files, err := Load(patternsDir)
	if err != nil {
		return nil, err
	}
	reg := NewRegistry()
	for _, pf := range files {
		reg.RegisterFile(pf)
	}
	return reg, nil
}
