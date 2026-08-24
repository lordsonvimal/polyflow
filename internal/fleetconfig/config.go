// Package fleetconfig loads and saves the git-tracked fleet definition file
// (docs/global-fleet-registry-plan.md, "The two files" §1) — a small,
// shareable YAML document listing a fleet's member repos by git URL (never
// by local path, so it resolves identically for a developer or a CI
// runner) plus the cross-service Links between them.
//
// This package is a leaf: it must not import internal/indexer or
// cmd/polyflow. Everything in Tier GR from GR.1 onward depends on it, never
// the reverse.
package fleetconfig

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Service describes one fleet member, resolved by git URL rather than local
// path so the same definition works for any machine or CI runner.
type Service struct {
	Name string `yaml:"name" json:"name"`
	// Git is the clone URL, e.g. "https://github.com/Nordic/juniper.git".
	Git string `yaml:"git" json:"git"`
	// Subpath locates the service inside a monorepo clone. Empty when the
	// repo IS the service (e.g. "willow").
	Subpath string `yaml:"subpath,omitempty" json:"subpath,omitempty"`
	// Ref is the default branch/tag every resolution uses unless overridden
	// per-invocation (docs/global-fleet-registry-plan.md, "Ref overrides").
	Ref      string `yaml:"ref" json:"ref"`
	Language string `yaml:"language" json:"language"`
}

// Config is the parsed representation of a fleet definition file.
type Config struct {
	Name     string           `yaml:"name" json:"name"`
	Version  string           `yaml:"version" json:"version"`
	Services []Service        `yaml:"services" json:"services"`
	Links    []workspace.Link `yaml:"links,omitempty" json:"links,omitempty"`
}

// HasService reports whether name matches a member of this fleet.
func (c *Config) HasService(name string) bool {
	for _, svc := range c.Services {
		if svc.Name == name {
			return true
		}
	}
	return false
}

// Load reads and parses a fleet definition file at path. Unknown fields are
// rejected (same strict-decode contract as workspace.Load) to catch a typo'd
// key rather than silently dropping it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fleet config %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse fleet config: %w", err)
	}

	seenNames := make(map[string]bool, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc.Name == "" {
			return nil, fmt.Errorf("fleet config: a service is missing name")
		}
		if svc.Git == "" {
			return nil, fmt.Errorf("service %s: git is required", svc.Name)
		}
		if svc.Ref == "" {
			return nil, fmt.Errorf("service %s: ref is required", svc.Name)
		}
		if seenNames[svc.Name] {
			return nil, fmt.Errorf("duplicate service name %q — service names must be unique", svc.Name)
		}
		seenNames[svc.Name] = true
	}

	return &cfg, nil
}

// Save writes cfg back to path atomically (write-tmp-then-rename, same
// pattern as workspace.Save).
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal fleet config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write fleet config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename fleet config: %w", err)
	}
	return nil
}
