package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// GapCaseID returns the deterministic case id for a runtime-observed gap
// edge from → to: "gap-" + the first 8 hex chars of sha256(from|to). Pinned
// so the same edge always promotes to the same id, making promotion
// idempotent across runs.
func GapCaseID(from, to string) string {
	sum := sha256.Sum256([]byte(from + "|" + to))
	return "gap-" + hex.EncodeToString(sum[:])[:8]
}

// PromoteGaps scans the workspace graph for edges with
// VerificationState == graph.StateObservedOnlyGap — runtime-observed edges
// static analysis missed — and builds one eval Case per gap not already
// present in existing (matched by case id). Direction is pinned: runtime
// observed From → To that static missed, so `impact --target To` must
// include From's file — that is exactly the miss the gap proves. Cases are
// sorted by id for deterministic, idempotent output.
func PromoteGaps(ctx context.Context, store graph.Store, existing *Manifest) ([]Case, error) {
	idx, err := store.BuildIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("build graph index: %w", err)
	}

	have := make(map[string]bool, len(existing.Cases))
	for _, c := range existing.Cases {
		have[c.ID] = true
	}

	seen := make(map[string]bool)
	var cases []Case
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.VerificationState != graph.StateObservedOnlyGap {
				continue
			}
			id := GapCaseID(e.From, e.To)
			if have[id] || seen[id] {
				continue
			}
			fromNode := idx.Nodes[e.From]
			toNode := idx.Nodes[e.To]
			if fromNode == nil || toNode == nil {
				// Can't build an unambiguous case without both endpoints.
				continue
			}
			seen[id] = true
			cases = append(cases, Case{
				ID:               id,
				Kind:             "node",
				Target:           toNode.Label,
				Service:          toNode.Service,
				ExpectedImpacted: []string{},
				MustNotMiss:      []string{fromNode.File},
			})
		}
	}

	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

// renderGapCase formats a promoted Case as a manifest.yaml list entry,
// matching the hand-authored style (2-space list indent, must_not_miss as a
// nested list). expected_impacted is rendered as the literal empty-flow
// list `[]` — a gap case proves one required file, not a full expected set.
func renderGapCase(c Case) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - id: %s\n", c.ID)
	fmt.Fprintf(&b, "    kind: %s\n", c.Kind)
	fmt.Fprintf(&b, "    target: %s\n", c.Target)
	if c.Service != "" {
		fmt.Fprintf(&b, "    service: %s\n", c.Service)
	}
	b.WriteString("    expected_impacted: []\n")
	b.WriteString("    must_not_miss:\n")
	for _, f := range c.MustNotMiss {
		fmt.Fprintf(&b, "      - %s\n", f)
	}
	return b.String()
}

// AppendCasesToManifest appends cases to <dir>/manifest.yaml as text,
// preserving the existing file's content (including hand-written comments)
// rather than round-tripping through yaml.Marshal. Cases are expected
// pre-sorted by id (PromoteGaps' contract) so repeated calls with the same
// input are byte-identical.
func AppendCasesToManifest(dir string, cases []Case) error {
	if len(cases) == 0 {
		return nil
	}
	path := filepath.Join(dir, "manifest.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", path, err)
	}

	var b strings.Builder
	b.Write(data)
	if !strings.HasSuffix(string(data), "\n") {
		b.WriteString("\n")
	}
	for _, c := range cases {
		b.WriteString("\n")
		b.WriteString(renderGapCase(c))
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
