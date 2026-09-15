package trace

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// applyHintsViaPipeline runs the "hints" Tier FX framework (FX.8.31,
// replacing the retired internal/linker.ApplyHints) the same way
// internal/indexer/link_passes.go's apply_hints_and_enrich pass does. See
// internal/e2e/hints_helper_test.go for the identical helper used there —
// duplicated per-package because Go test helpers aren't exported across
// _test.go-only packages.
func applyHintsViaPipeline(t *testing.T, links []workspace.Link, nodes []graph.Node) []graph.Node {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("hints")
	if fw == nil {
		t.Fatal("hints framework not embedded")
	}
	linkHints := make([]graph.LinkHint, len(links))
	for i, l := range links {
		linkHints[i] = graph.LinkHint{From: l.From, To: l.To, BaseURL: l.BaseURL, Hint: l.Hint}
	}
	res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Links: linkHints})
	if err != nil {
		t.Fatalf("hints: %v", err)
	}
	out := make([]graph.Node, len(nodes))
	copy(out, nodes)
	byID := make(map[string]int, len(out))
	for i := range out {
		byID[out[i].ID] = i
	}
	for _, p := range res.Patches {
		idx, ok := byID[p.ID]
		if !ok {
			continue
		}
		n := out[idx]
		m := make(map[string]string, len(n.Meta)+len(p.Meta))
		for k, v := range n.Meta {
			m[k] = v
		}
		for k, v := range p.Meta {
			m[k] = v
		}
		n.Meta = m
		out[idx] = n
	}
	return out
}
