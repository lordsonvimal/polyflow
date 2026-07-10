package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Import-call linking must attribute a call site to the function that
// actually contains it, not the nearest preceding declaration (regression:
// checkbox → collapseAllBoundaries), and must resolve member calls to
// module-scope variable targets (Solid signal setters).
func TestLinkJS_ContainmentAndVariableTargets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Filters.tsx")
	src := `import { uiStore } from "./stores/ui";

const Filters = () => {
  const checkbox = (label: string) => (
    <label>{label}</label>
  );
  return (
    <div>
      <button onClick={() => uiStore.collapseAllBoundaries()}>Collapse</button>
      <button onClick={() => uiStore.setNotification("hi")}>Notify</button>
    </div>
  );
};
`
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	nodes := []graph.Node{
		{
			ID: "web:" + file + ":function:Filters:3", Type: graph.NodeTypeFunction,
			Label: "Filters", Service: "web", File: file, Line: 3,
			Meta: map[string]string{"end_line": "13"},
		},
		{
			ID: "web:" + file + ":function:checkbox:4", Type: graph.NodeTypeFunction,
			Label: "checkbox", Service: "web", File: file, Line: 4,
			Meta: map[string]string{"end_line": "6"},
		},
		{
			ID: "web:ui.ts:function:collapseAllBoundaries:137", Type: graph.NodeTypeFunction,
			Label: "collapseAllBoundaries", Service: "web", File: "ui.ts", Line: 137,
		},
		{
			ID: "web:ui.ts:variable:setNotification:42", Type: graph.NodeTypeVariable,
			Label: "setNotification", Service: "web", File: "ui.ts", Line: 42,
			Meta: map[string]string{"scope": "module", "destructured": "true"},
		},
	}

	edges, _ := NewJSLinker().LinkJS(nodes, nil, map[string][]string{"web": {file}})

	fromByTarget := map[string]string{}
	for _, e := range edges {
		fromByTarget[e.To] = e.From
	}

	assert.Equal(t, fmt.Sprintf("web:%s:function:Filters:3", file),
		fromByTarget["web:ui.ts:function:collapseAllBoundaries:137"],
		"call at line 9 is outside checkbox (ends line 6); must attribute to Filters")
	assert.Equal(t, fmt.Sprintf("web:%s:function:Filters:3", file),
		fromByTarget["web:ui.ts:variable:setNotification:42"],
		"member call must fall back to module-scope variable targets")
}

// Module-level store derivations (regression: derived.ts had zero outgoing
// edges): member calls inside a `const x = createMemo(() => …)` initializer
// must attribute to the declared variable, target variables get read/write
// semantics (accessor → reads, signal setter → writes), and bare value uses
// of imported constants produce reads edges.
func TestLinkJS_ModuleLevelDerivationsAndVariableSemantics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "derived.ts")
	src := `import { uiStore } from "./ui";
import { DEFAULT_CONFIDENCE } from "./confidence";

export const filtered = createMemo(() => {
  const hidden = uiStore.hiddenTypes();
  return hidden;
});

createEffect(() => {
  uiStore.setNotification("changed");
});

export const levels = [...DEFAULT_CONFIDENCE];
`
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	filteredID := "web:" + file + ":variable:filtered:4"
	moduleID := "web:" + file + ":function:(module):0"
	nodes := []graph.Node{
		{
			ID: filteredID, Type: graph.NodeTypeVariable,
			Label: "filtered", Service: "web", File: file, Line: 4,
			Meta: map[string]string{"scope": "module"},
		},
		{
			ID: "web:" + file + ":variable:levels:13", Type: graph.NodeTypeVariable,
			Label: "levels", Service: "web", File: file, Line: 13,
			Meta: map[string]string{"scope": "module"},
		},
		{
			ID: moduleID, Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "web", File: file, Line: 0,
			Meta: map[string]string{"scope": "module"},
		},
		{
			ID: "web:ui.ts:variable:hiddenTypes:52", Type: graph.NodeTypeVariable,
			Label: "hiddenTypes", Service: "web", File: "ui.ts", Line: 52,
			Meta: map[string]string{"scope": "module", "destructured": "true"},
		},
		{
			ID: "web:ui.ts:variable:setNotification:42", Type: graph.NodeTypeVariable,
			Label: "setNotification", Service: "web", File: "ui.ts", Line: 42,
			Meta: map[string]string{"scope": "module", "destructured": "true", "setter": "true"},
		},
		{
			ID: "web:confidence.ts:variable:DEFAULT_CONFIDENCE:10", Type: graph.NodeTypeVariable,
			Label: "DEFAULT_CONFIDENCE", Service: "web", File: "confidence.ts", Line: 10,
			Meta: map[string]string{"scope": "module", "kind": "const"},
		},
	}

	edges, _ := NewJSLinker().LinkJS(nodes, nil, map[string][]string{"web": {file}})

	type key struct{ from, to string }
	byPair := map[key]graph.Edge{}
	for _, e := range edges {
		byPair[key{e.From, e.To}] = e
	}

	// filtered (memo) reads hiddenTypes through the store member call.
	if e, ok := byPair[key{filteredID, "web:ui.ts:variable:hiddenTypes:52"}]; assert.True(t, ok,
		"memo-body member call must attribute to the filtered variable; edges: %+v", edges) {
		assert.Equal(t, graph.EdgeTypeReads, e.Type, "accessor call on a variable is a read")
		assert.Equal(t, graph.ConfidenceInferred, e.Confidence)
	}
	// Bare createEffect setter call: (module) writes the signal.
	if e, ok := byPair[key{moduleID, "web:ui.ts:variable:setNotification:42"}]; assert.True(t, ok,
		"module-level effect must attribute to the (module) node; edges: %+v", edges) {
		assert.Equal(t, graph.EdgeTypeWrites, e.Type, "setter call must retype to writes")
	}
	// Imported constant spread into a module initializer: levels reads it.
	if e, ok := byPair[key{"web:" + file + ":variable:levels:13", "web:confidence.ts:variable:DEFAULT_CONFIDENCE:10"}]; assert.True(t, ok,
		"imported constant value use must produce a reads edge; edges: %+v", edges) {
		assert.Equal(t, graph.EdgeTypeReads, e.Type)
	}
}
