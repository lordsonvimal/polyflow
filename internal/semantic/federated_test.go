package semantic

import (
	"context"
	"strings"
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

// TestFederatedSearch_ExcludesEmbeddinglessMembers: a member whose vector
// arm is unavailable (no embeddings indexed) must not contribute lexical
// hits to a ranking that a healthy member answered semantically. Its hits
// are dropped from the merge and it's named in the Semantic note.
func TestFederatedSearch_ExcludesEmbeddinglessMembers(t *testing.T) {
	dbHealthy := openTestDB(t)
	seedNode(t, dbHealthy, &graph.Node{
		ID: "healthy:fn:handleUser", Type: graph.NodeTypeFunction,
		Label: "handleUser", Service: "healthy", File: "user.go", Line: 1,
	}, []float32{1, 0, 0, 0})
	srHealthy := NewSearcher(NewStore(dbHealthy), &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}, nil)

	dbStale := openTestDB(t)
	seedNode(t, dbStale, &graph.Node{
		// Matches the "user" query lexically via the user.go filename token,
		// exactly like the healthy member's hit — but this DB has no
		// embeddings, so the match is lexical-only.
		ID: "stale:fn:randomThing", Type: graph.NodeTypeFunction,
		Label: "randomThing", Service: "stale", File: "user.go", Line: 1,
	}, nil)
	// Simulate a member that was indexed without embeddings.
	if _, err := dbStale.Exec(`DELETE FROM embeddings`); err != nil {
		t.Fatal(err)
	}
	srStale := NewSearcher(NewStore(dbStale), &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}, nil)

	resp, err := FederatedSearch(context.Background(), map[string]*Searcher{
		"healthy": srHealthy,
		"stale":   srStale,
	}, "user", 10)
	if err != nil {
		t.Fatalf("federated search: %v", err)
	}
	for _, h := range resp.Nodes {
		if h.Entity.Service == "stale" {
			t.Errorf("embedding-less member 'stale' must be excluded from the merge, got hit %q", h.Entity.ID)
		}
	}
	if !strings.Contains(resp.Semantic, "stale") {
		t.Errorf("Semantic note must name the excluded member, got %q", resp.Semantic)
	}
}

// TestScopedSearch_DefaultIsWorkspaceLocal: service == "" must hit only the
// local Searcher even when fleet members are wired (GR.7 — fleet search is
// opt-in via service "*").
func TestScopedSearch_DefaultIsWorkspaceLocal(t *testing.T) {
	dbLocal := openTestDB(t)
	seedNode(t, dbLocal, &graph.Node{
		ID: "local:fn:doThing", Type: graph.NodeTypeFunction,
		Label: "doThing", Service: "local", File: "thing.go", Line: 1,
	}, nil)
	local := NewSearcher(NewStore(dbLocal), nil, nil)

	dbOther := openTestDB(t)
	seedNode(t, dbOther, &graph.Node{
		ID: "other:fn:doThing", Type: graph.NodeTypeFunction,
		Label: "doThing", Service: "other", File: "thing.go", Line: 1,
	}, nil)
	fleet := map[string]*Searcher{
		"local": local,
		"other": NewSearcher(NewStore(dbOther), nil, nil),
	}

	ctx := context.Background()

	local1, err := ScopedSearch(ctx, local, fleet, "doThing", "", 10)
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	for _, h := range local1.Nodes {
		if h.Entity.ID == "other:fn:doThing" {
			t.Errorf("default scope leaked a fleet-member hit: %q", h.Entity.ID)
		}
	}

	all, err := ScopedSearch(ctx, local, fleet, "doThing", "*", 10)
	if err != nil {
		t.Fatalf("scoped search '*': %v", err)
	}
	var sawOther bool
	for _, h := range all.Nodes {
		if h.Entity.ID == "other:fn:doThing" {
			sawOther = true
		}
	}
	if !sawOther {
		t.Errorf("service='*' must federate: expected an 'other' hit, got %+v", all.Nodes)
	}
}

// TestMergeAcrossMembers_OrdersByRawScoreNotRoundedTie is a regression test
// for the fleet-wide head collapsing to alphabetical entity-ID order. RRF
// scores for the first few ranks round to the same 3dp value
// (1/(60+1)=0.016393, 1/(60+2)=0.016129 → both "0.016"), so sorting on the
// rounded display score made the tie-break (entity ID) decide the head. The
// merge must sort on the raw accumulated score: a better rank in any member
// must outrank a worse rank even when its entity ID sorts later.
func TestMergeAcrossMembers_OrdersByRawScoreNotRoundedTie(t *testing.T) {
	pick := func(r Response, section string) []Hit {
		if section == "nodes" {
			return r.Nodes
		}
		return nil
	}

	perMember := map[string]Response{
		// "mA" contributes the best hit at rank 0 (1/61 = 0.016393).
		"mA": {Nodes: []Hit{
			{Entity: Entity{ID: "zzz:fn:best"}, Retrieval: "semantic"},
		}},
		// "mB" contributes "aaa:fn:worse" at rank 1 (1/62 = 0.016129) — a
		// distinct raw score but the *same* 3dp value (0.016) as the best hit,
		// and an entity ID that sorts first. Under the old rounded-score sort
		// the whole 0.016 bucket tied and "aaa:fn:worse" won the head on ID.
		// The rank-0 filler ties the best hit on raw score, so it carries an
		// ID that sorts after it to keep the raw tie-break unambiguous.
		"mB": {Nodes: []Hit{
			{Entity: Entity{ID: "zzz:fn:zfiller"}, Retrieval: "semantic"},
			{Entity: Entity{ID: "aaa:fn:worse"}, Retrieval: "semantic"},
		}},
	}

	out := mergeAcrossMembers([]string{"mA", "mB"}, perMember, pick, "nodes", 10)

	if len(out) != 3 {
		t.Fatalf("expected 3 merged hits, got %d: %+v", len(out), out)
	}
	if out[0].Entity.ID != "zzz:fn:best" {
		t.Errorf("head must be the better-ranked hit regardless of entity ID; got %q (full order: %s)",
			out[0].Entity.ID, hitIDs(out))
	}
	if !(out[0].Score >= out[1].Score && out[1].Score >= out[2].Score) {
		t.Errorf("merged hits must be in non-increasing score order, got %s", hitIDs(out))
	}
}

func hitIDs(hits []Hit) string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.Entity.ID
	}
	return strings.Join(ids, ", ")
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
