package contract

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ruleFile is the top-level structure of a contract YAML file.
type ruleFile struct {
	Version   string `yaml:"version"`
	Contracts []Rule `yaml:"contracts"`
}

// Load merges embedded defaults (from the compiled-in FS) and workspace-custom
// rules (from <workspaceDir>/contracts/*.yaml). It fails fast on unknown
// normalizer names, tiers, or policies — a YAML typo must never silently no-op.
//
// Either argument may be zero (nil / "") to skip that source.
func Load(embedded fs.FS, workspaceDir string) ([]Rule, error) {
	var all []Rule

	if embedded != nil {
		rules, err := loadFromFS(embedded)
		if err != nil {
			return nil, fmt.Errorf("contract: embedded rules: %w", err)
		}
		all = append(all, rules...)
	}

	if workspaceDir != "" {
		contractsDir := filepath.Join(workspaceDir, "contracts")
		if info, statErr := os.Stat(contractsDir); statErr == nil && info.IsDir() {
			rules, err := loadFromDiskDir(contractsDir)
			if err != nil {
				return nil, fmt.Errorf("contract: workspace rules: %w", err)
			}
			all = append(all, rules...)
		}
	}

	for i, r := range all {
		if err := validateRule(r); err != nil {
			return nil, fmt.Errorf("contract: rule %d (kind=%s): %w", i, r.Kind, err)
		}
	}
	return all, nil
}

func loadFromFS(fsys fs.FS) ([]Rule, error) {
	var all []Rule
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}
		rules, parseErr := parseRuleFile(path, data)
		if parseErr != nil {
			return parseErr
		}
		all = append(all, rules...)
		return nil
	})
	return all, err
}

func loadFromDiskDir(dir string) ([]Rule, error) {
	var all []Rule
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rules, err := parseRuleFile(path, data)
		if err != nil {
			return nil, err
		}
		all = append(all, rules...)
	}
	return all, nil
}

func parseRuleFile(name string, data []byte) ([]Rule, error) {
	var rf ruleFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return rf.Contracts, nil
}

func validateRule(r Rule) error {
	for _, name := range r.Normalizers {
		if _, ok := normRegistry[name]; !ok {
			return fmt.Errorf("unknown normalizer %q", name)
		}
	}
	for _, tier := range r.Match {
		switch tier {
		case TierExact, TierNormalized, TierWildcardAnchored:
		default:
			return fmt.Errorf("unknown match tier %q", tier)
		}
	}
	switch r.Unmatched {
	case UnmatchedUnknownEdge, UnmatchedLedger, UnmatchedDrop:
	case "":
		return fmt.Errorf("unmatched policy is required")
	default:
		return fmt.Errorf("unknown unmatched policy %q", r.Unmatched)
	}
	return nil
}
