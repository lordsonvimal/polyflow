package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func retractNode(id, svc, file, label string) Node {
	return Node{ID: id, Service: svc, File: file, Label: label, Type: NodeTypeClass}
}

// TestRetractResolvedRefs_DropsFalseAlarm is the measured case: on the
// juniper fleet 1324 of 1668 inherits_unresolved entries (79%) had a real
// inherits edge by the end of the run. lros_controller.rb:7 ApiBaseController
// was listed as a blind spot while the edge sat in the edge table, because the
// ledger is written at parse time and nothing retracts it when a linker
// succeeds.
func TestRetractResolvedRefs_DropsFalseAlarm(t *testing.T) {
	const ctrl = "/repo/app/controllers/client_api/v1/lros_controller.rb"
	const base = "/repo/app/controllers/client_api/v1/api_base_controller.rb"
	nodes := []Node{
		retractNode("n1", "orion", ctrl, "LrosController"),
		retractNode("n2", "orion", base, "ApiBaseController"),
	}
	edges := []Edge{{ID: "e1", From: "n1", To: "n2", Type: EdgeTypeInherits}}
	refs := []UnresolvedRef{
		{Service: "orion", File: ctrl, Line: 7, Name: "ApiBaseController", Kind: "inherits_unresolved"},
	}

	got := RetractResolvedRefs(refs, nodes, edges)

	assert.Empty(t, got, "an entry whose edge exists is not a blind spot")
}

// TestRetractResolvedRefs_KeepsGenuineGap — the whole point of the ledger is
// that a silently missing edge is the worst failure mode. Retraction must only
// remove entries that are demonstrably resolved.
func TestRetractResolvedRefs_KeepsGenuineGap(t *testing.T) {
	const ctrl = "/repo/app/controllers/lros_controller.rb"
	nodes := []Node{retractNode("n1", "orion", ctrl, "LrosController")}
	refs := []UnresolvedRef{
		{Service: "orion", File: ctrl, Line: 7, Name: "MissingConcern", Kind: "inherits_unresolved"},
	}

	got := RetractResolvedRefs(refs, nodes, nil)

	require.Len(t, got, 1)
	assert.Equal(t, "MissingConcern", got[0].Name)
}

// TestRetractResolvedRefs_WrongEdgeTypeDoesNotWitness — an `imports` edge to a
// name does not prove an `inherits` reference to it resolved. Each kind has
// exactly one witnessing edge type.
func TestRetractResolvedRefs_WrongEdgeTypeDoesNotWitness(t *testing.T) {
	nodes := []Node{
		retractNode("n1", "svc", "/repo/a.rb", "A"),
		retractNode("n2", "svc", "/repo/b.rb", "B"),
	}
	edges := []Edge{{ID: "e1", From: "n1", To: "n2", Type: EdgeTypeImports}}
	refs := []UnresolvedRef{
		{Service: "svc", File: "/repo/a.rb", Line: 1, Name: "B", Kind: "inherits_unresolved"},
	}

	assert.Len(t, RetractResolvedRefs(refs, nodes, edges), 1)
}

// TestRetractResolvedRefs_ScopedByServiceAndFile — two services can hold
// same-named files and classes (the fleet vendors lib/dx.rb into several
// repos). A resolution in one must not retract a gap in another.
func TestRetractResolvedRefs_ScopedByServiceAndFile(t *testing.T) {
	nodes := []Node{
		retractNode("n1", "svcA", "/a/dx.rb", "Child"),
		retractNode("n2", "svcA", "/a/base.rb", "Base"),
		retractNode("n3", "svcB", "/b/dx.rb", "Child"),
	}
	edges := []Edge{{ID: "e1", From: "n1", To: "n2", Type: EdgeTypeInherits}}
	refs := []UnresolvedRef{
		{Service: "svcA", File: "/a/dx.rb", Line: 1, Name: "Base", Kind: "inherits_unresolved"},
		{Service: "svcB", File: "/b/dx.rb", Line: 1, Name: "Base", Kind: "inherits_unresolved"},
	}

	got := RetractResolvedRefs(refs, nodes, edges)

	require.Len(t, got, 1)
	assert.Equal(t, "svcB", got[0].Service, "svcB's gap is real and untouched")
}

// TestRetractResolvedRefs_UnretractableKindsSurvive — kinds with no single
// witnessing edge type (config_not_found, selector_dynamic, the new
// rails_route_action_unresolved) are left alone rather than guessed at.
func TestRetractResolvedRefs_UnretractableKindsSurvive(t *testing.T) {
	nodes := []Node{
		retractNode("n1", "svc", "/repo/a.rb", "A"),
		retractNode("n2", "svc", "/repo/b.rb", "B"),
	}
	edges := []Edge{{ID: "e1", From: "n1", To: "n2", Type: EdgeTypeCalls}}
	refs := []UnresolvedRef{
		{Service: "svc", File: "/repo/a.rb", Line: 1, Name: "B", Kind: "config_not_found"},
		{Service: "svc", File: "/repo/a.rb", Line: 2, Name: "B", Kind: "rails_route_action_unresolved"},
	}

	assert.Len(t, RetractResolvedRefs(refs, nodes, edges), 2)
}

// TestRetractResolvedRefs_PreservesOrder — the ledger is reported in a stable
// order and callers rely on it.
func TestRetractResolvedRefs_PreservesOrder(t *testing.T) {
	refs := []UnresolvedRef{
		{Service: "s", File: "/a.rb", Line: 1, Name: "X", Kind: "call_ref"},
		{Service: "s", File: "/a.rb", Line: 2, Name: "Y", Kind: "call_ref"},
		{Service: "s", File: "/a.rb", Line: 3, Name: "Z", Kind: "call_ref"},
	}
	got := RetractResolvedRefs(refs, nil, nil)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"X", "Y", "Z"}, []string{got[0].Name, got[1].Name, got[2].Name})
}
