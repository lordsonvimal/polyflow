package main

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ref(file, name string, line int) graph.UnresolvedRef {
	return graph.UnresolvedRef{Service: "svc", File: file, Line: line, Name: name, Kind: "call_ref"}
}

// TestCapPerFile_IndexLikeFileCannotFloodTheFooter is the measured case:
// config/routes.rb declares every route in the application, so once phase A
// began ledgering unresolved route actions, any impact query that reached
// routes.rb inherited 178 footer lines about routes unrelated to the question.
// The footer tells an agent which files to open, so its length is a direct
// token cost.
func TestCapPerFile_IndexLikeFileCannotFloodTheFooter(t *testing.T) {
	var refs []graph.UnresolvedRef
	for i := 1; i <= 178; i++ {
		refs = append(refs, ref("/repo/config/routes.rb", "route", i))
	}
	refs = append(refs, ref("/repo/app/controllers/lros_controller.rb", "logger_context", 15))

	shown, omitted := capPerFile(refs, 5)

	assert.Len(t, shown, 6, "5 from routes.rb + the 1 from the controller")
	assert.Equal(t, 173, omitted["/repo/config/routes.rb"])
	assert.NotContains(t, omitted, "/repo/app/controllers/lros_controller.rb",
		"a file under the cap must not be reported as truncated")
}

// TestCapPerFile_PreservesOrder — the footer is grouped by file and line, and
// capping must not reshuffle what survives.
func TestCapPerFile_PreservesOrder(t *testing.T) {
	refs := []graph.UnresolvedRef{
		ref("/a.rb", "one", 1), ref("/b.rb", "two", 2), ref("/a.rb", "three", 3),
	}
	shown, omitted := capPerFile(refs, 5)

	require.Len(t, shown, 3)
	assert.Empty(t, omitted)
	assert.Equal(t, []string{"one", "two", "three"},
		[]string{shown[0].Name, shown[1].Name, shown[2].Name})
}

func TestCapPerFile_Empty(t *testing.T) {
	shown, omitted := capPerFile(nil, 5)
	assert.Empty(t, shown)
	assert.Empty(t, omitted)
}
