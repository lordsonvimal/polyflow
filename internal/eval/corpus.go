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
	Repo       RepoRef     `yaml:"repo"`
	Cases      []Case      `yaml:"cases"`
	AgentCases []AgentCase `yaml:"agent_cases,omitempty"` // T.2: agent-correctness cases, additive (absent = not agent-evaluated)
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
	Kind             string   `yaml:"kind"`              // node | file | diff | flow | semantic | rank1 | feature_add | test_impact | regression
	Target           string   `yaml:"target,omitempty"`  // node search query or file path (node|file|diff|feature_add|test_impact|regression)
	Service          string   `yaml:"service,omitempty"` // pre-filter target resolution to this service (B.3)
	NodeType         string   `yaml:"node_type,omitempty"` // pre-filter target resolution to this node type (B.3)
	// TargetFile pins a node case to one declaration when the label is shared.
	// `target: index` matches 20 Rails controllers, and neither Service nor
	// NodeType separates them — they are all `function` in one service — so
	// resolution silently picked whichever sorted first and the case measured a
	// controller nobody wrote it for. Matched as a path suffix.
	TargetFile string `yaml:"target_file,omitempty"`
	DiffFile         string   `yaml:"diff_file,omitempty"`
	ExpectedImpacted []string `yaml:"expected_impacted,omitempty"`
	MustNotMiss      []string `yaml:"must_not_miss"`
	// MustNotInclude (D.1) is the precision half of MustNotMiss: hand-verified
	// files that must NOT appear in the result. Any hit is a hard failure.
	// Cheap to author from a false-positive audit, and it catches the fan-out
	// phantom class directly — the failure mode a recall-only corpus is blind to.
	MustNotInclude []string `yaml:"must_not_include,omitempty"`
	// Exhaustive (D.1) declares that ExpectedImpacted is the COMPLETE truth set
	// for this case, not a sample. Precision is computed and reported only for
	// these cases; for every other case it is left unset rather than emitted as
	// a number, because hits/returned against a partial truth set measures how
	// short the sample is, not how precise the tool is.
	Exhaustive bool `yaml:"exhaustive,omitempty"`
	// feature_add cases: the new capability to add, anchored to Target (the existing related feature).
	NewCapability string `yaml:"new_capability,omitempty"`
	// RegressionSubject (kind=regression, E.1) is the *other* thing the
	// question asks about: "I'm changing Target — does that break
	// RegressionSubject?" The known answer lives in the truth set, not here.
	// A yes-case names the connecting files in ExpectedImpacted; a no-case
	// puts RegressionSubject's files in MustNotInclude, so an agent that
	// hedges "everything is connected" fails rather than scores.
	RegressionSubject string `yaml:"regression_subject,omitempty"`
	// Semantic search cases (kind=semantic, S.4):
	Query       string   `yaml:"query,omitempty"`         // natural-language query
	Section     string   `yaml:"section,omitempty"`       // nodes | flows | docs
	ExpectAnyOf []string `yaml:"expect_any_of,omitempty"` // entity labels; a hit in top-10 of Section counts as recall=1
	// ExpectRank1 (kind=rank1, C.6) is the entity label that must come back
	// FIRST for Query — not merely somewhere in the top 10, which is all a
	// semantic case asserts. Pin it to a declaration with TargetFile when the
	// label is shared (`.cell` occurs 30× in one orion view alone).
	ExpectRank1 string `yaml:"expect_rank1,omitempty"`
}

// AgentCase (T.2) poses a natural-language question to an agent restricted
// to polyflow's MCP tools and scores the answer deterministically:
// RequiredFacts must ALL appear, ForbiddenFacts must NONE appear.
type AgentCase struct {
	ID             string   `yaml:"id"`
	Question       string   `yaml:"question"`
	RequiredFacts  []string `yaml:"required_facts"`
	ForbiddenFacts []string `yaml:"forbidden_facts,omitempty"`
	MaxTurns       int      `yaml:"max_turns,omitempty"`
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
		case "node", "file", "diff", "flow", "feature_add", "test_impact", "regression":
			if len(c.ExpectedImpacted) == 0 {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "expected_impacted must not be empty"})
			}
			if c.Kind == "diff" && c.DiffFile == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "diff cases require diff_file"})
			}
			if c.Kind != "diff" && c.Target == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: c.Kind + " cases require target"})
			}
			if c.Kind == "feature_add" && c.NewCapability == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "feature_add cases require new_capability"})
			}
			if c.Kind == "regression" && c.RegressionSubject == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "regression cases require regression_subject"})
			}
		case "semantic":
			if c.Query == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "semantic cases require query"})
			}
			switch c.Section {
			case "nodes", "flows", "docs":
			default:
				errs = append(errs, ValidationError{CaseID: c.ID, Message: fmt.Sprintf("semantic cases require section (nodes|flows|docs), got %q", c.Section)})
			}
			if len(c.ExpectAnyOf) == 0 {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "semantic cases require expect_any_of"})
			}
		case "rank1":
			if c.Query == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "rank1 cases require query"})
			}
			switch c.Section {
			case "nodes", "flows", "docs":
			default:
				errs = append(errs, ValidationError{CaseID: c.ID, Message: fmt.Sprintf("rank1 cases require section (nodes|flows|docs), got %q", c.Section)})
			}
			if c.ExpectRank1 == "" {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "rank1 cases require expect_rank1"})
			}
			if len(c.ExpectAnyOf) > 0 {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "rank1 cases use expect_rank1, not expect_any_of (top-10 presence is a semantic case)"})
			}
		default:
			errs = append(errs, ValidationError{CaseID: c.ID, Message: fmt.Sprintf("unknown kind %q (must be node|file|diff|flow|semantic|rank1|feature_add|test_impact|regression)", c.Kind)})
		}
		// D.1 precision keys assert about file paths, so they are meaningful
		// only for the impact kinds. A semantic or rank1 case scores entity
		// labels; accepting them there would create a key that reads as a
		// precision assertion and silently does nothing.
		if c.Kind == "semantic" || c.Kind == "rank1" {
			if len(c.MustNotInclude) > 0 {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: c.Kind + " cases cannot use must_not_include (it asserts about file paths, not entity labels)"})
			}
			if c.Exhaustive {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: c.Kind + " cases cannot be exhaustive (top-10 search results are never a complete truth set)"})
			}
		}
		// A file cannot be both required and forbidden. Written down separately
		// these two lists look independent, and a case that contradicts itself
		// would hard-fail whatever the tool did — an unfixable red case teaches
		// the reader to ignore the gate.
		forbidden := toSet(c.MustNotInclude)
		for _, f := range append(append([]string{}, c.ExpectedImpacted...), c.MustNotMiss...) {
			if forbidden[f] {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: fmt.Sprintf("%q is in both must_not_include and the expected set", f)})
			}
		}
		if c.Exhaustive && len(c.ExpectedImpacted) == 0 {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "exhaustive requires expected_impacted (an empty complete truth set means the case expects nothing)"})
		}

		// Lint rule: every case must have at least one must_not_miss entry.
		// rank1 is exempt: its single assertion IS the hard failure, so a
		// must_not_miss list would only restate expect_rank1, and a list that
		// can disagree with the assertion beside it is worse than no list.
		if c.Kind == "rank1" {
			if len(c.MustNotMiss) > 0 {
				errs = append(errs, ValidationError{CaseID: c.ID, Message: "rank1 cases must not set must_not_miss (expect_rank1 is already the hard failure)"})
			}
			continue
		}
		if len(c.MustNotMiss) == 0 {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "must_not_miss is required (every case needs ≥1 hard-failure entry)"})
		}
	}
	agentSeen := make(map[string]bool)
	for _, c := range m.AgentCases {
		if c.ID == "" {
			errs = append(errs, ValidationError{Message: "agent case is missing id"})
		}
		if agentSeen[c.ID] {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "duplicate agent case id"})
		}
		agentSeen[c.ID] = true
		if c.Question == "" {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "agent case requires question"})
		}
		if len(c.RequiredFacts) == 0 {
			errs = append(errs, ValidationError{CaseID: c.ID, Message: "agent case requires required_facts"})
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
