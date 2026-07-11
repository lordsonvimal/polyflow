package patterns

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	patterndata "github.com/lordsonvimal/polyflow/patterns"
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

// LoadFS reads and parses all *.yaml pattern files in fsys (recursively). It is
// the io/fs analogue of Load, used to read the patterns embedded in the binary.
func LoadFS(fsys fs.FS) ([]*PatternFile, error) {
	var files []*PatternFile
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("load pattern %s: %w", path, err)
		}
		var pf PatternFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			return fmt.Errorf("load pattern %s: %w", path, err)
		}
		files = append(files, &pf)
		return nil
	})
	return files, err
}

// registryFromFiles builds a Registry from parsed pattern files.
func registryFromFiles(files []*PatternFile) *Registry {
	reg := NewRegistry()
	for _, pf := range files {
		reg.RegisterFile(pf)
	}
	return reg
}

// DefaultRegistry loads all YAML patterns from patternsDir and returns a populated Registry.
func DefaultRegistry(patternsDir string) (*Registry, error) {
	files, err := Load(patternsDir)
	if err != nil {
		return nil, err
	}
	return registryFromFiles(files), nil
}

// EmbeddedRegistry loads the built-in patterns compiled into the binary. This
// is the default source so `polyflow` works from any working directory,
// independent of the polyflow source tree.
func EmbeddedRegistry() (*Registry, error) {
	files, err := LoadFS(patterndata.FS)
	if err != nil {
		return nil, err
	}
	return registryFromFiles(files), nil
}
