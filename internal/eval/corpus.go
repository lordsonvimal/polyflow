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

// ValidationError is one schema or lint violation found in a manifest.
type ValidationError struct {
	CaseID  string
	Message string
}

func (e ValidationError) Error() string {
	if e.CaseID != "" {
		return fmt.Sprintf("case %s: %s", e.CaseID, e.Message)
	}
	return e.Message
}

// ValidateManifest checks manifest schema integrity and the must_not_miss lint rule.
// Returns all violations so callers can report them at once.
func ValidateManifest(m *Manifest) []ValidationError {
	var errs []ValidationError
	if m.Repo.Name == "" {
		errs = append(errs, ValidationError{Message: "repo.name is required"})
	}
	if m.Repo.SHA == "" {
		errs = append(errs, ValidationError{Message: "repo.sha is required (pin the commit for reproducible eval)"})
	}
	if m.Repo.Workspace == "" {
		errs = append(errs, ValidationError{Message: "repo.workspace is required"})
	}
	if m.Repo.URL == "" && m.Repo.Path == "" {
		errs = append(errs, ValidationError{Message: "repo.url or repo.path is required"})
	}
	seen := make(map[string]bool)
	for _, c := range m.Cases {
		if c.ID == "" {
			errs = append(errs, ValidationError{Message: "case is missing id"})
		}
		if seen[c.ID] {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "duplicate case id"})
		}
		seen[c.ID] = true
		switch c.Kind {
		case "node", "file", "diff":
		default:
			errs = append(errs, ValidationError{CaseID: c.ID, Message: fmt.Sprintf("unknown kind %q (must be node|file|diff)", c.Kind)})
		}
		if len(c.ExpectedImpacted) == 0 {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "expected_impacted must not be empty"})
		}
		// Lint rule: every case must have at least one must_not_miss file.
		if len(c.MustNotMiss) == 0 {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "must_not_miss is required (every case needs ≥1 hard-failure file)"})
		}
		if c.Kind == "diff" && c.DiffFile == "" {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "diff cases require diff_file"})
		}
		if c.Kind == "node" && c.Target == "" {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "node cases require target"})
		}
		if c.Kind == "file" && c.Target == "" {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "file cases require target"})
		}
	}
	return errs
}

// FindCorpusDirs returns all directories under root that contain a manifest.yaml.
// If root itself has a manifest.yaml, it is returned as the sole entry.
func FindCorpusDirs(root string) ([]string, error) {
	// If root is itself a corpus dir, return it directly.
	if _, err := os.Stat(filepath.Join(root, "manifest.yaml")); err == nil {
		return []string{root}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read corpus root %s: %w", root, err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "manifest.yaml")); err == nil {
			dirs = append(dirs, sub)
		}
	}
	return dirs, nil
}
