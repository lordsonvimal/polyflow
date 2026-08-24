package semantic

import (
	"context"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestFederatedSearch_MergesAndTagsServicePerMember(t *testing.T) {
	dbA := openTestDB(t)
	seedNode(t, dbA, &graph.Node{
		ID: "svcA:fn:getUser", Type: graph.NodeTypeFunction,
		Label: "getUser", Service: "svcA", File: "user.go", Line: 5,
	}, nil)
	srA := NewSearcher(NewStore(dbA), nil, nil)

	dbB := openTestDB(t)
	seedNode(t, dbB, &graph.Node{
		// FTS5's default unicode61 tokenizer treats "getUserProfile" as one
		// camelCase token, so the "user" query only matches via the "user.go"
		// filename token — same mechanism svcA's hit relies on above.
		ID: "svcB:fn:getUserProfile", Type: graph.NodeTypeFunction,
		Label: "getUserProfile", Service: "svcB", File: "user.go", Line: 9,
	}, nil)
	srB := NewSearcher(NewStore(dbB), nil, nil)

	ctx := context.Background()
	resp, err := FederatedSearch(ctx, map[string]*Searcher{"svcA": srA, "svcB": srB}, "user", 10)
	if err != nil {
		t.Fatalf("federated search: %v", err)
	}

	byID := make(map[string]Hit, len(resp.Nodes))
	for _, h := range resp.Nodes {
		byID[h.Entity.ID] = h
	}
	hitA, ok := byID["svcA:fn:getUser"]
	if !ok {
		t.Fatalf("expected a hit from svcA, got nodes: %+v", resp.Nodes)
	}
	if hitA.Entity.Service != "svcA" {
		t.Errorf("expected svcA hit tagged Service=svcA, got %q", hitA.Entity.Service)
	}
	hitB, ok := byID["svcB:fn:getUserProfile"]
	if !ok {
		t.Fatalf("expected a hit from svcB, got nodes: %+v", resp.Nodes)
	}
	if hitB.Entity.Service != "svcB" {
		t.Errorf("expected svcB hit tagged Service=svcB, got %q", hitB.Entity.Service)
	}
}

func TestFederatedSearch_SingleMember_StillTagsService(t *testing.T) {
	db := openTestDB(t)
	seedNode(t, db, &graph.Node{
		ID: "fn:getUser", Type: graph.NodeTypeFunction,
		Label: "getUser", Service: "svc", File: "user.go", Line: 5,
	}, nil)
	sr := NewSearcher(NewStore(db), nil, nil)

	resp, err := FederatedSearch(context.Background(), map[string]*Searcher{"svc": sr}, "getUser", 10)
	if err != nil {
		t.Fatalf("federated search: %v", err)
	}
	if len(resp.Nodes) == 0 {
		t.Fatal("expected at least one node hit")
	}
	if resp.Nodes[0].Entity.Service != "svc" {
		t.Errorf("expected Service=svc even with one member, got %q", resp.Nodes[0].Entity.Service)
	}
}

func TestFederatedSearch_NoSearchers_Errors(t *testing.T) {
	_, err := FederatedSearch(context.Background(), map[string]*Searcher{}, "q", 10)
	if err == nil {
		t.Fatal("expected an error with zero searchers")
	}
}

func TestFederatedSearch_MemberErrorPropagates(t *testing.T) {
	db := openTestDB(t)
	// Force an FTS error by dropping the entities_fts table this searcher
	// depends on.
	if _, err := db.Exec(`DROP TABLE entities_fts`); err != nil {
		t.Fatal(err)
	}
	dbOK := openTestDB(t)
	seedNode(t, dbOK, &graph.Node{ID: "n1", Type: graph.NodeTypeFunction, Label: "ok", Service: "svcOK", File: "a.go", Line: 1}, nil)

	_, err := FederatedSearch(context.Background(), map[string]*Searcher{
		"broken": NewSearcher(NewStore(db), nil, nil),
		"ok":     NewSearcher(NewStore(dbOK), nil, nil),
	}, "ok", 10)
	if err == nil {
		t.Fatal("expected the broken member's search error to propagate")
	}
}
