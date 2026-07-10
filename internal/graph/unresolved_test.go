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

func TestUnresolvedNote(t *testing.T) {
	assert.Empty(t, UnresolvedNote(0))
	assert.Contains(t, UnresolvedNote(3), "verify these 3 unresolved references manually")
}
