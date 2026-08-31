package impact_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// TestGroupUnresolvedByFile_GroupsAndOmitsServiceWhenSingle is the shape
// found live on orion-atlas: a 128-entry unresolved list touching only 16
// distinct files, one of them (App.tsx) alone accounting for 47 entries. The
// grouped form keeps every line/name/kind but stops repeating the file path
// once per entry, and single-service responses (the common case) drop the
// per-entry service entirely.
func TestGroupUnresolvedByFile_GroupsAndOmitsServiceWhenSingle(t *testing.T) {
	refs := []graph.UnresolvedRef{
		{Service: "svc", File: "App.tsx", Line: 95, Name: "onResolvedEmail", Kind: "call_ref"},
		{Service: "svc", File: "App.tsx", Line: 111, Name: "setAuthChecked", Kind: "call_ref"},
		{Service: "svc", File: "db.go", Line: 41, Name: "dynDispatch", Kind: "call_ref"},
	}

	groups := impact.GroupUnresolvedByFile(refs, true)

	require.Len(t, groups, 2, "two distinct files, sorted")
	assert.Equal(t, "App.tsx", groups[0].File)
	require.Len(t, groups[0].Entries, 2)
	assert.Equal(t, "onResolvedEmail", groups[0].Entries[0].Name)
	assert.Equal(t, "setAuthChecked", groups[0].Entries[1].Name)
	assert.Empty(t, groups[0].Entries[0].Service, "single-service: per-entry service is redundant with the response's own service field")
	assert.Equal(t, "db.go", groups[1].File)
	require.Len(t, groups[1].Entries, 1)
}

// TestGroupUnresolvedByFile_KeepsServiceWhenMultiple covers the cross-service
// case: dropping service there would make an unresolved ref ambiguous about
// which service's file it names.
func TestGroupUnresolvedByFile_KeepsServiceWhenMultiple(t *testing.T) {
	refs := []graph.UnresolvedRef{
		{Service: "frontend", File: "shared.ts", Line: 1, Name: "x", Kind: "call_ref"},
		{Service: "backend", File: "shared.ts", Line: 2, Name: "y", Kind: "call_ref"},
	}

	groups := impact.GroupUnresolvedByFile(refs, false)

	require.Len(t, groups, 1, "same file name, grouped together regardless of service")
	require.Len(t, groups[0].Entries, 2)
	assert.Equal(t, "frontend", groups[0].Entries[0].Service)
	assert.Equal(t, "backend", groups[0].Entries[1].Service)
}

func TestGroupUnresolvedByFile_Empty(t *testing.T) {
	assert.Nil(t, impact.GroupUnresolvedByFile(nil, true))
}

// TestResult_MarshalJSON_GroupsUnresolved is an end-to-end check of the wire
// format actually sent to callers (CLI --format json, MCP tool responses):
// json.Marshal on *Result must produce the grouped shape, not the flat list
// graph.UnresolvedRef would naively give.
func TestResult_MarshalJSON_GroupsUnresolved(t *testing.T) {
	r := &impact.Result{
		ServicesAffected: []string{"backend"},
		Unresolved: []graph.UnresolvedRef{
			{Service: "backend", File: "db.go", Line: 41, Name: "dynDispatch", Kind: "call_ref"},
			{Service: "backend", File: "db.go", Line: 50, Name: "other", Kind: "call_ref"},
		},
	}

	data, err := json.Marshal(r)
	require.NoError(t, err)

	var decoded struct {
		Unresolved []impact.UnresolvedFileGroup `json:"unresolved"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Unresolved, 1)
	assert.Equal(t, "db.go", decoded.Unresolved[0].File)
	require.Len(t, decoded.Unresolved[0].Entries, 2)

	// Every original field must round-trip: this is a reshape, not a
	// reduction of the actual unresolved information.
	assert.Equal(t, 41, decoded.Unresolved[0].Entries[0].Line)
	assert.Equal(t, "dynDispatch", decoded.Unresolved[0].Entries[0].Name)
	assert.Equal(t, "call_ref", decoded.Unresolved[0].Entries[0].Kind)
	assert.Empty(t, decoded.Unresolved[0].Entries[0].Service, "single-affected-service: redundant field dropped")

	// No other field is disturbed by the custom marshaler.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "services_affected")
	assert.Contains(t, raw, "total_callers")
}

// TestResult_MarshalJSON_EmptyUnresolvedStaysEmptyArray guards the
// zero-refs case: an empty list must still serialize as `[]`, not `null` —
// callers checking `len(unresolved) == 0` for "no blind spots" must see an
// empty array either way, and Result.Unresolved's own doc comment already
// promises "always present ([] when clean)".
func TestResult_MarshalJSON_EmptyUnresolvedStaysEmptyArray(t *testing.T) {
	r := &impact.Result{Unresolved: []graph.UnresolvedRef{}}
	data, err := json.Marshal(r)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.JSONEq(t, "[]", string(raw["unresolved"]))
}
