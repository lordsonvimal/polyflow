package workspace

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// Service describes a single service (repo or subdirectory) in the workspace.
type Service struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	Language   string   `yaml:"language"`
	Frameworks []string `yaml:"frameworks,omitempty"`
	Port       int      `yaml:"port,omitempty"`
}

// Link declares a known dependency between two services (HTTP or broker).
type Link struct {
	From     string `yaml:"from"`
	To       string `yaml:"to"`
	Via      string `yaml:"via,omitempty"`      // e.g. "rabbitmq"
	Hint     string `yaml:"hint,omitempty"`     // e.g. "USER_SERVICE_URL=http://localhost:8081"
	BaseURL  string `yaml:"base_url,omitempty"` // e.g. "/api" — stripped from client paths before matching
	Exchange string `yaml:"exchange,omitempty"` // via: rabbitmq — broker exchange connecting the services
}

// IndexConfig holds settings for the index command.
type IndexConfig struct {
	Exclude []string `yaml:"exclude"`
}

// Settings holds workspace-level defaults for the server and display.
type Settings struct {
	SnippetLines  int    `yaml:"snippet_lines"`  // default 30
	DefaultLayout string `yaml:"default_layout"` // default "dagre-lr"
	DefaultDepth  int    `yaml:"default_depth"`  // default 5
	Port          int    `yaml:"port"`           // default 9400
}

// WorkspaceConfig is the parsed representation of workspace.yaml.
type WorkspaceConfig struct {
	Name     string      `yaml:"name"`
	Version  string      `yaml:"version"`
	Services []Service   `yaml:"services"`
	Links    []Link      `yaml:"links"`
	Patterns []string    `yaml:"patterns,omitempty"`
	Index    IndexConfig `yaml:"index"`
	Settings Settings    `yaml:"settings"`
}

// EffectivePort returns the configured port, falling back to the default.
func (cfg *WorkspaceConfig) EffectivePort() int {
	if cfg.Settings.Port > 0 {
		return cfg.Settings.Port
	}
	return meta.DefaultPort
}

// Load reads and parses a workspace.yaml file at path.
func Load(path string) (*WorkspaceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace config %s: %w", path, err)
	}
	var cfg WorkspaceConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse workspace config: %w", err)
	}
	return &cfg, nil
}

// Save writes the config back to path atomically.
func Save(path string, cfg *WorkspaceConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal workspace config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write workspace config tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename workspace config: %w", err)
	}
	return nil
}
