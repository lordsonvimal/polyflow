package semantic

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestLooksLikeIdent(t *testing.T) {
	cases := map[string]bool{
		"do-build":     true,
		"do_build":     true,
		"build.submit": true,
		"DoBuild":      true,
		"build":        false,
		"payment":      false,
		"do build":     false,
		"":             false,
	}
	for in, want := range cases {
		if got := looksLikeIdent(in); got != want {
			t.Errorf("looksLikeIdent(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestQueryTargetsTest(t *testing.T) {
	cases := map[string]bool{
		"cancel build test":       true,
		"route registration spec": true,
		"StartBuild test":         true,
		"do-build":                false,
		"latest build":            false, // "latest" must not false-positive
		"":                        false,
	}
	for in, want := range cases {
		if got := QueryTargetsTest(in); got != want {
			t.Errorf("QueryTargetsTest(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNormalizeIdent(t *testing.T) {
	for _, in := range []string{"do-build", "do_build", "DoBuild", "do.build", "DO/BUILD"} {
		if got := normalizeIdent(in); got != "dobuild" {
			t.Errorf("normalizeIdent(%q) = %q, want %q", in, got, "dobuild")
		}
	}
}

func TestIdentExact(t *testing.T) {
	// Whole-label normalized match.
	if !identExact("DoBuild", "do-build") {
		t.Error("DoBuild should identExact-match do-build")
	}
	// Last path segment of a route label.
	if !identExact("POST /api/projects/do_build", "do-build") {
		t.Error("route last segment do_build should identExact-match do-build")
	}
	// A plain word must never claim an identifier-exact match.
	if identExact("cancel-build", "build") {
		t.Error("plain word 'build' must not identExact-match")
	}
	// Different action, same suffix word — not exact.
	if identExact("POST /api/projects/cancel_build", "do-build") {
		t.Error("cancel_build must not identExact-match do-build")
	}
}

func TestLabelCoversQuery(t *testing.T) {
	// Identifier query: needs the whole thing contiguously, not scattered.
	if !labelCoversQuery("POST /api/x/do_build", "do-build") {
		t.Error("do_build route should cover do-build")
	}
	if labelCoversQuery("POST /docker-builds/:build_id/do-cancel", "do-build") {
		t.Error("do-cancel route must NOT cover do-build (scattered do + builds)")
	}
	// Plain multi-word query: per-word coverage.
	if !labelCoversQuery("cancelBuildOrder", "cancel build order") {
		t.Error("cancelBuildOrder should cover the words cancel/build/order")
	}
}

func TestCoversAllQueryWords(t *testing.T) {
	q := identTokens("do-build") // ["do","build"]
	if !coversAllQueryWords("POST /api/x/do_build", q) {
		t.Error("do_build route should cover both words of do-build")
	}
	if coversAllQueryWords("POST /api/x/cancel_build", q) {
		t.Error("cancel_build route misses 'do' — must not be full coverage")
	}
	if coversAllQueryWords("Build", identTokens("build")) {
		t.Error("single-word query must never be 'full coverage'")
	}
}

// TestSearch_IdentifierQueryPinsEndpoint is the reported bug: searching
// "do-build" surfaced cancel-build endpoints over the do-build endpoint
// because the hyphenated term was shredded to `do* OR build*` and ranked by
// BM25 alone, with no exact/identifier tier to float the real match.
func TestSearch_IdentifierQueryPinsEndpoint(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "h:do_build", Type: graph.NodeTypeHTTPHandler,
		Label: "POST /api/projects/:id/do_build", Service: "juniper", File: "routes.go", Line: 10,
	}, []float32{0.2, 0.9, 0, 0})
	// Two decoys whose cards are shorter/denser on the "build" token — exactly
	// what BM25 rewards.
	for i, lbl := range []string{"POST /api/projects/:id/cancel_build", "GET /api/builds"} {
		seedNode(t, db, &graph.Node{
			ID: fmt.Sprintf("h:decoy%d", i), Type: graph.NodeTypeHTTPHandler,
			Label: lbl, Service: "juniper", File: "routes.go", Line: 20 + i,
		}, []float32{0.9, 0.1, 0, 0})
	}

	sr := NewSearcher(sem, &stubEmbedder{dims: 4, vec: []float32{0.9, 0.1, 0, 0}}, nil)
	resp, err := sr.Search(context.Background(), "do-build", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) == 0 || resp.Nodes[0].Entity.ID != "h:do_build" {
		t.Fatalf("expected h:do_build first, got %+v", resp.Nodes)
	}
	if resp.Nodes[0].Retrieval != "exact" {
		t.Errorf("do_build endpoint should be an exact/identifier match, got %q", resp.Nodes[0].Retrieval)
	}
}

// TestSearch_WeakResultsTrimmedAndNoted: a query that only produces a spray
// of single-arm BM25 near-misses (no exact hit, nothing in both arms,
// nothing covering the whole query) must trim to weakNodeCap and set Note.
func TestSearch_WeakResultsTrimmedAndNoted(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	for i := 0; i < 15; i++ {
		seedNode(t, db, &graph.Node{
			ID: fmt.Sprintf("fn:buildThing%d", i), Type: graph.NodeTypeFunction,
			Label: fmt.Sprintf("buildThing%d", i), Service: "svc", File: "a.go", Line: i + 1,
		}, nil)
	}
	// FTS-only (no embedder): a spray of `build*` BM25 matches with no exact
	// hit and nothing covering the whole query.
	sr := NewSearcher(sem, nil, nil)

	resp, err := sr.Search(context.Background(), "zzznomatch build", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) > weakNodeCap {
		t.Errorf("weak results should cap at %d, got %d", weakNodeCap, len(resp.Nodes))
	}
	if resp.Note == "" {
		t.Error("weak results should carry a Note advisory")
	}
}

// TestSearch_IdentifierQueryWeakWhenOnlyLexicalCousins: "do-build" with no
// real do-build endpoint should NOT present the lexically-adjacent "do-cancel"
// handler as a strong answer just because both retrieval arms land on it — it
// should trim and flag the weak match.
func TestSearch_IdentifierQueryWeakWhenOnlyLexicalCousins(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "h:do_cancel", Type: graph.NodeTypeHTTPHandler,
		Label: "POST /docker-builds/:build_id/do-cancel", Service: "builds-manager", File: "r.go", Line: 1,
	}, []float32{1, 0, 0, 0})
	for i := 0; i < 6; i++ {
		seedNode(t, db, &graph.Node{
			ID: fmt.Sprintf("fn:post%d", i), Type: graph.NodeTypeFunction,
			Label:   fmt.Sprintf("http.MethodPost /docker-builds/build-%d/do-cancel", i),
			Service: "builds", File: "client_test.go", Line: i + 1,
		}, []float32{1, 0, 0, 0})
	}

	sr := NewSearcher(sem, &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}, nil)
	resp, err := sr.Search(context.Background(), "do-build", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) > 0 && resp.Nodes[0].Retrieval == "exact" {
		t.Errorf("do-cancel must not be an exact match for do-build")
	}
	if len(resp.Nodes) > weakNodeCap {
		t.Errorf("weak identifier query should cap at %d, got %d", weakNodeCap, len(resp.Nodes))
	}
	if resp.Note == "" {
		t.Error("weak identifier query should carry a Note")
	}
}

func TestScopedSearch_UnknownServiceErrors(t *testing.T) {
	db := openTestDB(t)
	seedNode(t, db, &graph.Node{
		ID: "local:fn:x", Type: graph.NodeTypeFunction,
		Label: "x", Service: "local", File: "x.go", Line: 1,
	}, nil)
	local := NewSearcher(NewStore(db), nil, nil)
	fleet := map[string]*Searcher{"alpha": local, "beta": local}

	_, err := ScopedSearch(context.Background(), local, fleet, "x", "typo", 10)
	if !errors.Is(err, ErrUnknownSearchScope) {
		t.Fatalf("unknown service scope must return ErrUnknownSearchScope, got %v", err)
	}
}
