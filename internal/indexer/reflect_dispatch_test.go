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
	stampReflectDispatched(nodes, map[string]bool{"TableName": true}, nil)
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_FreeFunctionNotStamped(t *testing.T) {
	// A free function named "String" implements no interface (only a method
	// with a receiver can satisfy one) and must not be excluded from deadcode.
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "String"},
	}
	stampReflectDispatched(nodes, map[string]bool{"String": true}, nil)
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_NameNotInSetUntouched(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "DoSomething"},
	}
	stampReflectDispatched(nodes, map[string]bool{"TableName": true}, nil)
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_EmptySetIsNoOp(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "TableName"},
	}
	stampReflectDispatched(nodes, nil, nil)
	assert.Nil(t, nodes[0].Meta)
}

func TestStampReflectDispatched_PathPrefixRestrictsMatch(t *testing.T) {
	// Tier DC.2: "change"/"up"/"down" are common English words with no
	// gem-specific spelling — a migration-path constraint must reject a
	// same-named method declared outside db/migrate/ while still stamping
	// the one that lives under it.
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "change", File: "db/migrate/20260101_add_x.rb", Language: "ruby"},
		{ID: "2", Type: graph.NodeTypeFunction, Label: "change", File: "app/models/coin.rb", Language: "ruby"},
	}
	stampReflectDispatched(nodes, map[string]bool{"change": true}, map[string]string{"change": "db/migrate/"})
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched], "migration file must be stamped")
	assert.Empty(t, nodes[1].Meta[graph.MetaReflectDispatched], "same-named method outside db/migrate/ must stay a real deadcode candidate")
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
