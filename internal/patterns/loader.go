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

	// Grammar allow-list, copied down from the PatternFile at registration
	// time. Empty means "compile against every grammar the pattern language
	// is normally cross-compiled into" (existing behavior, e.g. javascript
	// patterns also run against typescript/tsx source). Set it when a
	// pattern's query shape is grammar-specific — e.g. it walks
	// formal_parameters' immediate child node types, which differ between
	// the plain javascript grammar (bare identifier/object_pattern) and
	// typescript/tsx (identifier wrapped in required_parameter) such that no
	// single query text is valid tree-sitter syntax in both.
	Grammars []string `yaml:"-"`
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

	// Optional grammar allow-list restricting which grammars this file's
	// patterns compile against; see Pattern.Grammars.
	Grammars []string `yaml:"grammars"`

	// ReflectDispatchedMethods names methods this file's Package/VersionRange
	// gate (or no gate at all, for a language's own stdlib interfaces) is
	// known to invoke through an interface value or reflection rather than a
	// literal call site — e.g. gorm.yaml declares GORM's TableName/Before*/
	// After* hooks, gated on gorm.io/gorm. Framework-specific reflect-dispatch
	// knowledge lives here, declaratively and per-language/per-package,
	// instead of as a hardcoded name list in the deadcode algorithm: a new
	// framework's hook methods are one YAML entry away, no Go code change.
	ReflectDispatchedMethods []string `yaml:"reflect_dispatched_methods"`

	// ReflectDispatchedPathPrefix additionally restricts this file's
	// ReflectDispatchedMethods to nodes whose file path contains this
	// substring. Empty (the default gorm.yaml/devise.yaml use) means no
	// restriction — those hook names are specific enough on their own.
	// Needed for names that are common English words with no gem-specific
	// spelling (ActiveRecord migrations' change/up/down, see
	// active_record_migration.yaml): without a path constraint, any unrelated
	// method sharing that name anywhere in a Rails service would be
	// incorrectly excluded from deadcode.
	ReflectDispatchedPathPrefix string `yaml:"reflect_dispatched_path_prefix"`
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

// PatternFileInfo pairs a parsed PatternFile with the path it was loaded
// from — Load/LoadFS discard this once WalkDir moves on, but the UO.7
// patterns API needs it to show each pattern's source file.
type PatternFileInfo struct {
	Path string
	File *PatternFile
}

// LoadWithPaths is like Load but retains each file's source path.
func LoadWithPaths(dir string) ([]PatternFileInfo, error) {
	var files []PatternFileInfo
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
		files = append(files, PatternFileInfo{Path: path, File: pf})
		return nil
	})
	return files, err
}

// EmbeddedFilesWithPaths is the LoadWithPaths analogue for the built-in
// patterns compiled into the binary (see EmbeddedRegistry).
func EmbeddedFilesWithPaths() ([]PatternFileInfo, error) {
	var files []PatternFileInfo
	err := fs.WalkDir(patterndata.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".yaml" {
			return nil
		}
		data, err := fs.ReadFile(patterndata.FS, path)
		if err != nil {
			return fmt.Errorf("load pattern %s: %w", path, err)
		}
		var pf PatternFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			return fmt.Errorf("load pattern %s: %w", path, err)
		}
		files = append(files, PatternFileInfo{Path: "embedded:" + path, File: &pf})
		return nil
	})
	return files, err
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
