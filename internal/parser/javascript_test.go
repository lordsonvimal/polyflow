package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// A top-level function is independently created by both the YAML-pattern
// matcher (function_decl) and extractJSVariables' own structural walk — an
// ID collision used to mean overlay's bare re-creation silently won outright
// (last in the appended slice), losing base's wide EndLine (the real
// declaration span) to overlay's degenerate EndLine==Line self-attribution,
// AND dropping any Meta base had stamped (MetaReferencedAsValue from
// matcher.go's Pass 3c). mergeJSNodes must keep base's node — including its
// EndLine — as authoritative, only unioning in whatever Meta key overlay
// uniquely contributes.
func TestMergeJSNodes_BaseWinsStructureOverlayFillsMetaGaps(t *testing.T) {
	base := []graph.Node{
		{ID: "a", Label: "parseDepPrefix", Type: graph.NodeTypeFunction, EndLine: 40, Meta: map[string]string{"referenced_as_value": "true"}},
		{ID: "b", Label: "onlyInBase", Type: graph.NodeTypeFunction},
	}
	overlay := []graph.Node{
		{ID: "a", Label: "parseDepPrefix", Type: graph.NodeTypeFunction, EndLine: 38}, // re-created, degenerate EndLine==Line, no meta
		{ID: "c", Label: "onlyInOverlay", Type: graph.NodeTypeFunction, Meta: map[string]string{"js_accessor": "true"}},
	}

	merged := mergeJSNodes(base, overlay)

	byID := map[string]graph.Node{}
	for _, n := range merged {
		byID[n.ID] = n
	}
	assert.Len(t, merged, 3)
	assert.Equal(t, "true", byID["a"].Meta["referenced_as_value"], "base's stamp must survive overlay's bare re-creation")
	assert.Equal(t, 40, byID["a"].EndLine, "base's wider EndLine must win, not overlay's degenerate one")
	assert.Equal(t, "onlyInBase", byID["b"].Label)
	assert.Equal(t, "true", byID["c"].Meta["js_accessor"], "overlay-only nodes are appended untouched")
}

// A Meta key overlay uniquely contributes must not overwrite the same key if
// base already set it (defensive — not expected in practice today, since
// base and overlay never stamp the same key name, but the fill-gaps
// semantics must hold if that ever changes).
func TestMergeJSNodes_BaseMetaKeyNotOverwritten(t *testing.T) {
	base := []graph.Node{{ID: "a", Meta: map[string]string{"k": "base"}}}
	overlay := []graph.Node{{ID: "a", Meta: map[string]string{"k": "overlay"}}}

	merged := mergeJSNodes(base, overlay)
	assert.Equal(t, "base", merged[0].Meta["k"])
}
