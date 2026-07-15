// Package eval provides the ground-truth recall evaluation harness (Tier E).
// A corpus is a directory of manifest.yaml files describing hand-verified
// impact cases for one repository; the runner executes them against the
// current graph and scores the results.
package eval

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest is the top-level corpus manifest for one repository.
type Manifest struct {
	Repo  RepoRef `yaml:"repo"`
	Cases []Case  `yaml:"cases"`
}

// RepoRef identifies the target repository.
type RepoRef struct {
	Name      string `yaml:"name"`
	URL       string `yaml:"url,omitempty"`
	Path      string `yaml:"path,omitempty"` // path: for local repos
	SHA       string `yaml:"sha"`
	Workspace string `yaml:"workspace"`
}

// Case is one eval test case.
type Case struct {
	ID               string   `yaml:"id"`
	Kind             string   `yaml:"kind"`              // node | file | diff
	Target           string   `yaml:"target,omitempty"`  // node search query or file path
	DiffFile         string   `yaml:"diff_file,omitempty"`
	ExpectedImpacted []string `yaml:"expected_impacted"`
	MustNotMiss      []string `yaml:"must_not_miss"`
}

// LoadManifest reads a corpus manifest from <dir>/manifest.yaml.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}
