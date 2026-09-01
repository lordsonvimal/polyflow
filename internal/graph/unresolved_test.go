package graph

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnresolvedInFiles_ScopesToFileSet(t *testing.T) {
	refs := []UnresolvedRef{
		{Service: "web", File: "src/a.ts", Line: 3, Name: "onSave", Kind: "call_ref"},
		{Service: "web", File: "src/b.ts", Line: 7, Name: "helper", Kind: "import_ref"},
		{Service: "api", File: "handler.go", Line: 12, Name: "runX", Kind: "call_ref"},
	}
	files := map[string]bool{"src/a.ts": true, "handler.go": true}

	got := UnresolvedInFiles(refs, files)
	require.Len(t, got, 2)
	assert.Equal(t, "onSave", got[0].Name) // input order preserved
	assert.Equal(t, "runX", got[1].Name)
}

func TestUnresolvedInFiles_NoMatchIsEmptyNotNil(t *testing.T) {
	refs := []UnresolvedRef{{File: "src/b.ts", Line: 1, Name: "x", Kind: "call_ref"}}

	got := UnresolvedInFiles(refs, map[string]bool{"src/a.ts": true})
	require.NotNil(t, got)
	assert.Empty(t, got)

	// The JSON contract: an empty section encodes as [], never null.
	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestDropExternalFrameworkRefs(t *testing.T) {
	idx := NewAdjacencyIndex()
	idx.AddNode(&Node{ID: "1", Label: "FileListing", Type: NodeTypeClass})
	idx.AddNode(&Node{ID: "2", Label: "Component", Type: NodeTypeClass}) // a real in-repo class

	refs := []UnresolvedRef{
		{File: "a.jsx", Name: "React.Component", Kind: "inherits_unresolved"},   // drop
		{File: "a.jsx", Name: "Store.PureComponent", Kind: "inherits_unresolved"}, // drop (namespace)
		{File: "a.jsx", Name: "Component", Kind: "inherits_unresolved"},          // keep — declared in repo
		{File: "a.jsx", Name: "Select", Kind: "jsx_component_unresolved"},        // drop — react-select
		{File: "a.jsx", Name: "FileListing", Kind: "jsx_component_unresolved"},   // keep — declared
		{File: "a.jsx", Name: "For", Kind: "jsx_framework_component"},            // drop
		{File: "a.jsx", Name: "buildThing", Kind: "call_ref"},                   // keep — untouched kind
	}

	got := DropExternalFrameworkRefs(refs, idx)
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"Component", "FileListing", "buildThing"}, names)
}

func TestUnresolvedNote(t *testing.T) {
	assert.Empty(t, UnresolvedNote(0))
	assert.Contains(t, UnresolvedNote(3), "verify these 3 unresolved references manually")
}
