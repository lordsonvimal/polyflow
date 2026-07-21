package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// PromotionResult is returned by RunPromotion.
type PromotionResult struct {
	// RuleDest is where the rule YAML was written on success.
	RuleDest string
	// FixtureDest is where the fixture JSON was written on success.
	FixtureDest string
	// Diff is the human-readable mismatch on failure (empty on success).
	Diff string
}

// RunPromotion validates and promotes a proposed rule:
//
//  1. Loads and validates the rule YAML at proposalPath.
//  2. Loads the companion fixture (same base, .json extension).
//  3. Runs the contract engine against the fixture nodes.
//  4. On pass: copies rule → <wsDir>/contracts/ and fixture → <wsDir>/testdata/contracts/.
//  5. On fail: returns a PromotionResult with Diff set (rule 3, no silent drops).
//
// wsDir is the workspace root directory (where workspace.yaml lives).
func RunPromotion(proposalPath, wsDir string) (PromotionResult, error) {
	// 1. Load and validate the rule YAML.
	yamlData, err := os.ReadFile(proposalPath)
	if err != nil {
		return PromotionResult{}, fmt.Errorf("read proposal %s: %w", proposalPath, err)
	}
	rules, err := contract.ParseAndValidateBytes(yamlData)
	if err != nil {
		return PromotionResult{}, fmt.Errorf("invalid proposal %s: %w", proposalPath, err)
	}

	// 2. Derive and load the companion fixture.
	ext := filepath.Ext(proposalPath)
	base := strings.TrimSuffix(proposalPath, ext)
	fixturePath := base + ".json"

	fixData, err := os.ReadFile(fixturePath)
	if err != nil {
		return PromotionResult{}, fmt.Errorf("fixture not found at %s (expected alongside %s): %w", fixturePath, proposalPath, err)
	}
	var fc FixtureCase
	if err := json.Unmarshal(fixData, &fc); err != nil {
		return PromotionResult{}, fmt.Errorf("parse fixture %s: %w", fixturePath, err)
	}

	// 3. Convert fixture nodes to graph.Node and run the engine.
	nodes := make([]graph.Node, len(fc.Nodes))
	for i, fn := range fc.Nodes {
		nodes[i] = graph.Node{
			ID:      fn.ID,
			Type:    graph.NodeType(fn.Type),
			Service: fn.Service,
			Label:   fn.Label,
			Meta:    fn.Meta,
		}
	}

	eng := &contract.Engine{}
	result := eng.Link(nodes, rules, nil)

	// 4. Compare produced edges with expected.
	diff := diffEdges(fc.ExpectedEdges, result.Edges)
	if diff != "" {
		return PromotionResult{Diff: diff}, nil // caller prints diff and refuses
	}

	// 5. Move files to workspace.
	base = filepath.Base(proposalPath)
	fixBase := filepath.Base(fixturePath)

	contractsDir := filepath.Join(wsDir, "contracts")
	if err := os.MkdirAll(contractsDir, 0o755); err != nil {
		return PromotionResult{}, fmt.Errorf("create contracts dir: %w", err)
	}

	testdataDir := filepath.Join(wsDir, "testdata", "contracts")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		return PromotionResult{}, fmt.Errorf("create testdata/contracts dir: %w", err)
	}

	ruleDest := filepath.Join(contractsDir, base)
	fixDest := filepath.Join(testdataDir, fixBase)

	if err := copyFile(proposalPath, ruleDest); err != nil {
		return PromotionResult{}, fmt.Errorf("copy rule to %s: %w", ruleDest, err)
	}
	if err := copyFile(fixturePath, fixDest); err != nil {
		// Roll back the rule copy to avoid a partial state.
		_ = os.Remove(ruleDest)
		return PromotionResult{}, fmt.Errorf("copy fixture to %s: %w", fixDest, err)
	}

	// Remove originals from proposals dir on success.
	_ = os.Remove(proposalPath)
	_ = os.Remove(fixturePath)

	return PromotionResult{RuleDest: ruleDest, FixtureDest: fixDest}, nil
}

// diffEdges compares expected edges (from fixture) with produced edges (from engine).
// Returns a human-readable diff string, or "" if they match.
// Matching is by (From, To, Type) — the produced set must contain every expected edge.
func diffEdges(expected []FixtureEdge, produced []graph.Edge) string {
	// Build a set of produced (from, to, type) triples.
	type triple struct{ from, to, typ string }
	producedSet := make(map[triple]bool, len(produced))
	for _, e := range produced {
		producedSet[triple{e.From, e.To, string(e.Type)}] = true
	}

	var missing, extra []string
	for _, e := range expected {
		if !producedSet[triple{e.From, e.To, e.Type}] {
			missing = append(missing, fmt.Sprintf("  - expected  %s → %s  [%s]", e.From, e.To, e.Type))
		}
	}

	// Collect produced triples for checking extras.
	expectedSet := make(map[triple]bool, len(expected))
	for _, e := range expected {
		expectedSet[triple{e.From, e.To, e.Type}] = true
	}

	// Sort produced edges for stable diff output (bug-class rule 2).
	prodSlice := make([]graph.Edge, len(produced))
	copy(prodSlice, produced)
	sort.Slice(prodSlice, func(i, j int) bool {
		if prodSlice[i].From != prodSlice[j].From {
			return prodSlice[i].From < prodSlice[j].From
		}
		if prodSlice[i].To != prodSlice[j].To {
			return prodSlice[i].To < prodSlice[j].To
		}
		return string(prodSlice[i].Type) < string(prodSlice[j].Type)
	})
	for _, e := range prodSlice {
		if !expectedSet[triple{e.From, e.To, string(e.Type)}] {
			extra = append(extra, fmt.Sprintf("  + unexpected %s → %s  [%s]", e.From, e.To, string(e.Type)))
		}
	}

	sort.Strings(missing)
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Fixture mismatch:\n")
	for _, l := range missing {
		sb.WriteString(l + "\n")
	}
	for _, l := range extra {
		sb.WriteString(l + "\n")
	}
	return sb.String()
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
