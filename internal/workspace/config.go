package workspace

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Service describes a single service (repo or subdirectory) in the workspace.
type Service struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Language string `yaml:"language"`
	Port     int    `yaml:"port,omitempty"`
}

// Link declares a known HTTP dependency between two services.
type Link struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
	Hint string `yaml:"hint,omitempty"` // e.g. "USER_SERVICE_URL=http://localhost:8081"
}

// WorkspaceConfig is the parsed representation of workspace.yaml.
type WorkspaceConfig struct {
	Version  string    `yaml:"version"`
	Services []Service `yaml:"services"`
	Links    []Link    `yaml:"links"`
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
