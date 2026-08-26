package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestStampReflectDispatched_MethodMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "TableName"},
	}
	stampReflectDispatched(nodes, map[string]bool{"TableName": true})
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_FreeFunctionNotStamped(t *testing.T) {
	// A free function named "String" implements no interface (only a method
	// with a receiver can satisfy one) and must not be excluded from deadcode.
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "String"},
	}
	stampReflectDispatched(nodes, map[string]bool{"String": true})
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_NameNotInSetUntouched(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "DoSomething"},
	}
	stampReflectDispatched(nodes, map[string]bool{"TableName": true})
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_EmptySetIsNoOp(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "TableName"},
	}
	stampReflectDispatched(nodes, nil)
	assert.Nil(t, nodes[0].Meta)
}

func TestIsMinifiedAsset(t *testing.T) {
	cases := map[string]bool{
		"static/js/datastar.min.js": true,
		"static/css/app.min.css":    true,
		"static/js/app.js":          false,
		"main.go":                   false,
	}
	for path, want := range cases {
		assert.Equal(t, want, isMinifiedAsset(path), "path %s", path)
	}
}
