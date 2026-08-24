// Package registry loads and saves the local, never-committed machine
// registry (docs/global-fleet-registry-plan.md, "The two files" §2) —
// ~/.polyflow/registry.yml (or $POLYFLOW_HOME/registry.yml) — mapping a
// fleet member's service name to where THIS machine already has it checked
// out. It is written automatically as a side effect of `polyflow index` run
// standalone inside a repo (GR.1); nothing here hand-edits it.
//
// This package is a leaf: it must not import internal/indexer or
// cmd/polyflow.
package registry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// polyflowHomeEnv overrides the default ~/.polyflow home directory, e.g. for
// test isolation or a machine with an unusual home dir (same reasoning as
// the FR-plan's other env-var overrides).
const polyflowHomeEnv = "POLYFLOW_HOME"

// registryFileName is the file name inside the polyflow home directory.
const registryFileName = "registry.yml"

// Entry records where this machine has one fleet member service checked out.
type Entry struct {
	Service   string    `yaml:"service" json:"service"`
	LocalPath string    `yaml:"local_path" json:"local_path"`
	IndexedAt time.Time `yaml:"indexed_at" json:"indexed_at"`
	// Fleets lists the fleet(s), if any, whose definition names this
	// service as a member — GR.3's reverse index, populated by
	// RecordFleetMembership (called from `polyflow fleet sync`, the only
	// command that ever reads a fleet definition, never by a standalone
	// `polyflow index`).
	Fleets []string `yaml:"fleets,omitempty" json:"fleets,omitempty"`
}

// Registry is the parsed representation of registry.yml.
type Registry struct {
	Version string  `yaml:"version" json:"version"`
	Entries []Entry `yaml:"entries" json:"entries"`
	// FleetConfigPaths maps a fleet name to the local path of its git-tracked
	// definition file last used to sync it — recorded alongside Fleets so
	// GR.3's query-time resolver can find and reload that file to rebuild a
	// stale bridge without the caller having to know or pass it again.
	FleetConfigPaths map[string]string `yaml:"fleet_config_paths,omitempty" json:"fleet_config_paths,omitempty"`
}

// DefaultPath returns the path to this machine's registry.yml, honoring
// $POLYFLOW_HOME when set, else ~/.polyflow.
func DefaultPath() (string, error) {
	if home := os.Getenv(polyflowHomeEnv); home != "" {
		return filepath.Join(home, registryFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".polyflow", registryFileName), nil
}

// Load reads and parses the registry at path. A missing file is not an
// error — it returns an empty, ready-to-use Registry, since the registry is
// self-populating and does not exist until the first standalone index run.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Registry{Version: "1"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}
	var reg Registry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return &reg, nil
}

// Save writes reg back to path atomically, creating parent directories as
// needed.
func Save(path string, reg *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create registry dir: %w", err)
	}
	data, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write registry tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename registry: %w", err)
	}
	return nil
}

// Lookup returns the entry for service, if any.
func (r *Registry) Lookup(service string) (*Entry, bool) {
	for i := range r.Entries {
		if r.Entries[i].Service == service {
			return &r.Entries[i], true
		}
	}
	return nil, false
}

// Sync upserts an entry for service (by name) at path, setting localPath and
// indexedAt to now. It loads path, mutates, and saves — safe to call
// repeatedly for the same service without accumulating duplicates.
func Sync(path, service, localPath string) error {
	reg, err := Load(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if e, ok := reg.Lookup(service); ok {
		e.LocalPath = localPath
		e.IndexedAt = now
	} else {
		reg.Entries = append(reg.Entries, Entry{
			Service:   service,
			LocalPath: localPath,
			IndexedAt: now,
		})
	}
	if reg.Version == "" {
		reg.Version = "1"
	}
	return Save(path, reg)
}

// RecordFleetMembership upserts fleetName into service's Fleets list (no
// duplicates across repeat calls) and records fleetConfigPath as that
// fleet's last-known local definition-file location. Called by `polyflow
// fleet sync` for every member it resolves — the only place a fleet
// definition is ever read, so it is also the only place that can populate
// this reverse index. A service with no existing entry gets a bare one
// created (LocalPath/IndexedAt left zero) rather than erroring, since a
// build-cache-resolved member never goes through registry.Sync itself.
func RecordFleetMembership(path, service, fleetName, fleetConfigPath string) error {
	reg, err := Load(path)
	if err != nil {
		return err
	}
	e, ok := reg.Lookup(service)
	if !ok {
		reg.Entries = append(reg.Entries, Entry{Service: service})
		e, _ = reg.Lookup(service)
	}
	if !containsString(e.Fleets, fleetName) {
		e.Fleets = append(e.Fleets, fleetName)
	}
	if fleetConfigPath != "" {
		if reg.FleetConfigPaths == nil {
			reg.FleetConfigPaths = make(map[string]string)
		}
		reg.FleetConfigPaths[fleetName] = fleetConfigPath
	}
	if reg.Version == "" {
		reg.Version = "1"
	}
	return Save(path, reg)
}

// FleetsForPath returns the fleet names, if any, that claim the service
// whose local checkout is at absPath (matched by exact LocalPath equality —
// registry.Sync always writes a workspace root, never a subdirectory of
// one).
func (r *Registry) FleetsForPath(absPath string) []string {
	var fleets []string
	for _, e := range r.Entries {
		if e.LocalPath != absPath {
			continue
		}
		for _, f := range e.Fleets {
			if !containsString(fleets, f) {
				fleets = append(fleets, f)
			}
		}
	}
	return fleets
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
