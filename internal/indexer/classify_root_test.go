package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestClassifyRoot_ModuleNodeIsEntrypointDespiteIncomingEdge(t *testing.T) {
	// Reproduces the juniper bug: a synthetic (module) wrapper picks up
	// an ordinary non-Contains edge (LinkTemplScripts' `imports` edge from a
	// templ component's <script src>), which must not stop it from being
	// classified as an entrypoint.
	n := &graph.Node{ID: "js:x.js:function:(module):0", Type: graph.NodeTypeFunction, Label: "(module)"}
	incoming := map[string]bool{n.ID: true}

	ok := classifyRoot(n, incoming, nil)
	assert.True(t, ok)
	assert.Equal(t, "entrypoint", n.Meta["root_kind"])
}

func TestClassifyRoot_MainAndInitAreEntrypoints(t *testing.T) {
	for _, label := range []string{"main", "init"} {
		n := &graph.Node{ID: "go:x.go:function:" + label + ":1", Type: graph.NodeTypeFunction, Label: label}
		ok := classifyRoot(n, nil, nil)
		assert.True(t, ok)
		assert.Equal(t, "entrypoint", n.Meta["root_kind"], "label %s", label)
	}
}

func TestClassifyRoot_ObjectMethodPairIsCallback(t *testing.T) {
	n := &graph.Node{
		ID: "js:x.js:function:onProceed:10", Type: graph.NodeTypeFunction, Label: "onProceed",
		Meta: map[string]string{"pattern": "object_method_pair"},
	}
	ok := classifyRoot(n, map[string]bool{}, nil)
	assert.True(t, ok)
	assert.Equal(t, "callback", n.Meta["root_kind"])
}

func TestClassifyRoot_JSAccessorIsCallback(t *testing.T) {
	n := &graph.Node{
		ID: "js:x.js:function:value:10", Type: graph.NodeTypeFunction, Label: "value",
		Meta: map[string]string{"js_accessor": "true"},
	}
	ok := classifyRoot(n, map[string]bool{}, nil)
	assert.True(t, ok)
	assert.Equal(t, "callback", n.Meta["root_kind"])
}

func TestClassifyRoot_ReferencedAsValueIsCallback(t *testing.T) {
	n := &graph.Node{
		ID: "js:x.js:function:parseDepPrefix:10", Type: graph.NodeTypeFunction, Label: "parseDepPrefix",
		Meta: map[string]string{graph.MetaReferencedAsValue: "true"},
	}
	ok := classifyRoot(n, map[string]bool{}, nil)
	assert.True(t, ok)
	assert.Equal(t, "callback", n.Meta["root_kind"])
}

func TestClassifyRoot_ReferencedIDIsCallback(t *testing.T) {
	n := &graph.Node{ID: "go:x.go:function:runIndex:1", Type: graph.NodeTypeFunction, Label: "runIndex"}
	ok := classifyRoot(n, map[string]bool{}, map[string]bool{n.ID: true})
	assert.True(t, ok)
	assert.Equal(t, "callback", n.Meta["root_kind"])
}

func TestClassifyRoot_UnreferencedIsUnreachable(t *testing.T) {
	n := &graph.Node{ID: "go:x.go:function:deadCode:1", Type: graph.NodeTypeFunction, Label: "deadCode"}
	ok := classifyRoot(n, map[string]bool{}, map[string]bool{})
	assert.True(t, ok)
	assert.Equal(t, "unreachable", n.Meta["root_kind"])
}

func TestClassifyRoot_CalledFunctionNotClassified(t *testing.T) {
	n := &graph.Node{ID: "go:x.go:function:helper:1", Type: graph.NodeTypeFunction, Label: "helper"}
	ok := classifyRoot(n, map[string]bool{n.ID: true}, nil)
	assert.False(t, ok)
	assert.Empty(t, n.Meta)
}

func TestClassifyRoot_NonFunctionNodeSkipped(t *testing.T) {
	n := &graph.Node{ID: "js:x.js:variable:x:1", Type: graph.NodeTypeVariable, Label: "x"}
	ok := classifyRoot(n, map[string]bool{}, nil)
	assert.False(t, ok)
}

// object_method_shorthand (class methods share this grammar node with
// object-literal shorthand methods) must NOT get the same blanket-callback
// treatment as object_method_pair — a genuinely dead class method has to
// stay a real deadcode candidate.
func TestClassifyRoot_ObjectMethodShorthandStaysUnreachable(t *testing.T) {
	n := &graph.Node{
		ID: "js:x.js:function:deadMethod:10", Type: graph.NodeTypeFunction, Label: "deadMethod",
		Meta: map[string]string{"pattern": "object_method_shorthand"},
	}
	ok := classifyRoot(n, map[string]bool{}, nil)
	assert.True(t, ok)
	assert.Equal(t, "unreachable", n.Meta["root_kind"])
}
