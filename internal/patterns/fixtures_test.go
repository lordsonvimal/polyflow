package patterns_test

// TestPatternFixtures enforces the design doc requirement:
// every YAML pattern file must have a corresponding <name>_test/ directory
// containing:
//
//   - input.*        — positive fixture; matched pattern names must equal
//     the multiset listed in expected.json, and each match
//     must produce the node type listed alongside it.
//   - expected.json  — {"patterns": [...], "node_types": [...]} parallel arrays.
//   - negative.*     — negative fixture; must produce ZERO matches. This is
//     the "no false positives" gate: code that superficially
//     resembles the pattern (other frameworks, other SDK
//     versions) must not match.
//
// CI fails if any piece is missing or diverges.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

const patternsRoot = "../../patterns"

type expectedFixture struct {
	Patterns  []string `json:"patterns"`
	NodeTypes []string `json:"node_types"`
}

// grammarForExt maps a fixture file extension to the tree-sitter grammar name.
func grammarForExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx", ".jsx":
		return "tsx"
	case ".rb":
		return "ruby"
	case ".html":
		return "html"
	default:
		return ""
	}
}

// matchFixture runs all patterns of pf against the fixture file at path.
func matchFixture(t *testing.T, pf *patterns.PatternFile, path string) []patterns.MatchResult {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	grammar := grammarForExt(filepath.Ext(path))
	if grammar == "" {
		t.Fatalf("fixture %s: unsupported extension", path)
	}
	reg := patterns.NewRegistry()
	reg.RegisterFile(pf)
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.MatchWithGrammar(pf.Language, grammar, path, src)
	if err != nil {
		t.Fatalf("match fixture %s: %v", path, err)
	}
	return results
}

func TestPatternFixtures(t *testing.T) {
	yamlFiles, err := filepath.Glob(filepath.Join(patternsRoot, "*", "*.yaml"))
	if err != nil || len(yamlFiles) == 0 {
		t.Fatalf("cannot glob pattern files: %v", err)
	}

	for _, yamlPath := range yamlFiles {
		rel, _ := filepath.Rel(patternsRoot, yamlPath)
		t.Run(rel, func(t *testing.T) {
			pf, err := patterns.LoadFile(yamlPath)
			if err != nil {
				t.Fatalf("load pattern file: %v", err)
			}

			fixtureDir := strings.TrimSuffix(yamlPath, ".yaml") + "_test"
			if _, err := os.Stat(fixtureDir); os.IsNotExist(err) {
				t.Fatalf("missing fixture dir %s", fixtureDir)
			}

			inputs, _ := filepath.Glob(filepath.Join(fixtureDir, "input.*"))
			if len(inputs) == 0 {
				t.Fatalf("fixture dir %s has no input.* file", fixtureDir)
			}
			negatives, _ := filepath.Glob(filepath.Join(fixtureDir, "negative.*"))
			if len(negatives) == 0 {
				t.Fatalf("fixture dir %s has no negative.* file (required: no-false-positives gate)", fixtureDir)
			}

			// ── Positive: matched pattern-name multiset must equal expected ──
			expPath := filepath.Join(fixtureDir, "expected.json")
			expData, err := os.ReadFile(expPath)
			if err != nil {
				t.Fatalf("fixture dir %s has no expected.json", fixtureDir)
			}
			var exp expectedFixture
			if err := json.Unmarshal(expData, &exp); err != nil {
				t.Fatalf("parse %s: %v", expPath, err)
			}

			var got []string
			var results []patterns.MatchResult
			for _, input := range inputs {
				rs := matchFixture(t, pf, input)
				results = append(results, rs...)
				for _, r := range rs {
					got = append(got, r.PatternName)
				}
			}

			want := append([]string(nil), exp.Patterns...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("matched patterns diverge from expected.json\n  got:  %v\n  want: %v", got, want)
			}

			// ── Node types: each expected (pattern, node_type) pair must be
			// producible. Matches that emit no nodes (call refs, suppressed
			// type declarations) are skipped.
			allowed := make(map[string]map[string]bool) // pattern name -> allowed node types
			for i, name := range exp.Patterns {
				if i >= len(exp.NodeTypes) {
					break
				}
				if allowed[name] == nil {
					allowed[name] = make(map[string]bool)
				}
				allowed[name][exp.NodeTypes[i]] = true
			}
			for _, r := range results {
				nodes, _ := patterns.MatchToGraph("svc", []patterns.MatchResult{r})
				if len(nodes) == 0 {
					continue
				}
				want := allowed[r.PatternName]
				if len(want) == 0 {
					continue
				}
				for _, n := range nodes {
					if n.Type == graph.NodeTypeChannel {
						continue // synthesized channel nodes are a side effect, not the match itself
					}
					if n.Label == "(module)" {
						continue // synthesized module-scope caller node, not the match itself
					}
					if !want[string(n.Type)] {
						t.Errorf("pattern %s produced node type %q, expected one of %v (line %d)",
							r.PatternName, n.Type, keys(want), r.Line)
					}
				}
			}

			// ── Negative: zero matches allowed ──
			for _, neg := range negatives {
				rs := matchFixture(t, pf, neg)
				for _, r := range rs {
					t.Errorf("FALSE POSITIVE: pattern %s matched negative fixture %s at line %d (captures: %v)",
						r.PatternName, filepath.Base(neg), r.Line, r.Captures)
				}
			}
		})
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
