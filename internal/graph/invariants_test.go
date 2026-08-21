package graph

import (
	"fmt"
	"testing"
)

// realFixtureID mirrors the production ID format (service:file:type:name:line,
// see internal/parser/templ.go:templNodeID) — every ID hash includes service.
func realFixtureID(service, file, name string, line int) string {
	return fmt.Sprintf("%s:%s:function:%s:%d", service, file, name, line)
}

func TestAssertServiceScopedIDs_NoCollision(t *testing.T) {
	// Two services with files that share (file, line, name) after filepath.Base
	// normalization — a common bug shape — but IDs still differ because Service
	// is baked into the ID string itself.
	services := map[string][]*Node{
		"api": {
			{ID: realFixtureID("api", "handler.go", "Handle", 1), Service: "api"},
		},
		"worker": {
			{ID: realFixtureID("worker", "handler.go", "Handle", 1), Service: "worker"},
		},
	}

	nodeID, serviceA, serviceB, ok := AssertServiceScopedIDs(services)
	if !ok {
		t.Fatalf("expected no collision, got nodeID=%q serviceA=%q serviceB=%q", nodeID, serviceA, serviceB)
	}
}

func TestAssertServiceScopedIDs_Collision(t *testing.T) {
	// Deliberately-broken ID function: two different services minting the exact
	// same node ID (e.g. a future parser change that hashes only file+line).
	const collidingID = "broken-id-shared-across-services"
	services := map[string][]*Node{
		"api": {
			{ID: collidingID, Service: "api"},
		},
		"worker": {
			{ID: collidingID, Service: "worker"},
		},
	}

	nodeID, serviceA, serviceB, ok := AssertServiceScopedIDs(services)
	if ok {
		t.Fatalf("expected collision to be detected, got ok=true")
	}
	if nodeID != collidingID {
		t.Errorf("nodeID = %q, want %q", nodeID, collidingID)
	}
	if serviceA == serviceB {
		t.Errorf("serviceA and serviceB must differ, both = %q", serviceA)
	}
	gotServices := map[string]bool{serviceA: true, serviceB: true}
	if !gotServices["api"] || !gotServices["worker"] {
		t.Errorf("expected services {api, worker}, got {%q, %q}", serviceA, serviceB)
	}
}

func TestAssertServiceScopedIDs_SameServiceDuplicateID_NotAViolation(t *testing.T) {
	// A duplicate ID within the SAME service is not a cross-service collision
	// (it's a separate concern, e.g. an upsert); the checker only flags IDs that
	// disagree on owning service.
	services := map[string][]*Node{
		"api": {
			{ID: "dup", Service: "api"},
			{ID: "dup", Service: "api"},
		},
	}

	_, _, _, ok := AssertServiceScopedIDs(services)
	if !ok {
		t.Errorf("expected same-service duplicate to be allowed, got a violation")
	}
}
