package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestStampReflectDispatched_MethodMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "TableName", Language: "go"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{"go": {"TableName": true}}, nil)
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_FreeFunctionNotStamped(t *testing.T) {
	// A free function named "String" implements no interface (only a method
	// with a receiver can satisfy one) and must not be excluded from deadcode.
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "String", Language: "go"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{"go": {"String": true}}, nil)
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_NameNotInSetUntouched(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "DoSomething", Language: "go"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{"go": {"TableName": true}}, nil)
	assert.Empty(t, nodes[0].Meta[graph.MetaReflectDispatched])
}

func TestStampReflectDispatched_EmptySetIsNoOp(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeMethod, Label: "TableName", Language: "go"},
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
	stampReflectDispatched(nodes,
		map[string]map[string]bool{"ruby": {"change": true}},
		map[string]map[string]string{"ruby": {"change": "db/migrate/"}})
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched], "migration file must be stamped")
	assert.Empty(t, nodes[1].Meta[graph.MetaReflectDispatched], "same-named method outside db/migrate/ must stay a real deadcode candidate")
}

func TestStampReflectDispatched_JSFunctionMatch(t *testing.T) {
	// DC.4b: JS/TS has no method/function split at all -- extractJSVariables
	// mints every class method (including React lifecycle hooks) as
	// graph.NodeTypeFunction -- so without widening eligibility the same way
	// Ruby's does, package:react's reflect_dispatched_methods gate would
	// never stamp anything.
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "componentDidCatch", Language: "javascript"},
		{ID: "2", Type: graph.NodeTypeFunction, Label: "componentDidCatch", Language: "typescript"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{
		"javascript": {"componentDidCatch": true},
		"typescript": {"componentDidCatch": true},
	}, nil)
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched])
	assert.Equal(t, "true", nodes[1].Meta[graph.MetaReflectDispatched])
}

// TestStampReflectDispatched_TypeScriptFallsBackToJavaScriptBucket pins a
// second DC.4b gap found live against orion-atlas: every JS/TS pattern file,
// react.yaml included, declares one bucket (`language: javascript` — one
// grammar family covers both), but a .tsx node's own Language field is
// stamped "typescript" (tsLanguage, internal/parser/javascript.go). Without
// the fallback, a real ErrorBoundary.tsx in a mixed Rails+React service kept
// flagging dead despite the gate being satisfied and the node type being
// eligible.
func TestStampReflectDispatched_TypeScriptFallsBackToJavaScriptBucket(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "componentDidCatch", Language: "typescript"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{
		"javascript": {"componentDidCatch": true},
	}, nil)
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched])
}

// TestStampReflectDispatched_LanguageKeyedNotServiceKeyed pins the DC.4b bug
// fix directly: a single polyglot service (a Rails app with a React
// frontend, e.g. orion-atlas) must have BOTH its ruby-gated and
// javascript-gated reflect_dispatched_methods apply to the matching nodes —
// keying the whole call by one service-level language string used to mean
// only one of the two ever took effect.
func TestStampReflectDispatched_LanguageKeyedNotServiceKeyed(t *testing.T) {
	nodes := []graph.Node{
		{ID: "1", Type: graph.NodeTypeFunction, Label: "password_required?", Language: "ruby"},
		{ID: "2", Type: graph.NodeTypeFunction, Label: "componentDidCatch", Language: "typescript"},
	}
	stampReflectDispatched(nodes, map[string]map[string]bool{
		"ruby":       {"password_required?": true},
		"typescript": {"componentDidCatch": true},
	}, nil)
	assert.Equal(t, "true", nodes[0].Meta[graph.MetaReflectDispatched], "the ruby-gated hook must still stamp")
	assert.Equal(t, "true", nodes[1].Meta[graph.MetaReflectDispatched], "the javascript-gated hook in the same service must also stamp")
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
