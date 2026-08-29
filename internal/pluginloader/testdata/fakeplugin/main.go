// Command fakeplugin is Phase 0's round-trip fixture: a linkplugin.Plugin
// with zero requires: that adds one trivial edge type to a synthetic
// fixture, proving the handshake -> Link -> Result -> graph-merge path
// end-to-end (docs/linker-plugin-architecture-plan.md Phase 0 acceptance).
//
// It is not a template for a real plugin author — see Phase 1 for that.
package main

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
)

// fakeEdgeType is the trivial edge kind this plugin mints. It is not one of
// internal/graph/model.go's known EdgeType constants on purpose — a plugin
// is expected to mint edge kinds core has never seen.
const fakeEdgeType = "fake_edge"

type fakePlugin struct{}

func (fakePlugin) Name() string { return "fakeplugin" }

// Requires declares zero capabilities: this component's Link needs nothing
// beyond ctx.Nodes, so Containment/Symbols/KeyLedger are all nil.
func (fakePlugin) Requires(string) []linkplugin.Capability { return nil }

// Link connects every "source"-labelled node to every "target"-labelled
// node in the same file with a fake_edge — a fixed, trivial rule, deliberate
// per the Phase 0 test's need for a byte-identical comparison against a
// hand-written equivalent.
func (fakePlugin) Link(ctx *linkplugin.LinkContext) (linkplugin.Result, error) {
	var sources, targets []lpgraph.Node
	for _, n := range ctx.Nodes {
		switch n.Label {
		case "source":
			sources = append(sources, n)
		case "target":
			targets = append(targets, n)
		}
	}

	var edges []lpgraph.Edge
	for _, s := range sources {
		for _, t := range targets {
			if s.File != t.File {
				continue
			}
			edges = append(edges, lpgraph.Edge{
				ID:   fmt.Sprintf("fakeplugin:%s->%s", s.ID, t.ID),
				From: s.ID,
				To:   t.ID,
				Type: fakeEdgeType,
			})
		}
	}

	return linkplugin.Result{Edges: edges}, nil
}

func main() {
	linkplugin.Serve(fakePlugin{})
}
