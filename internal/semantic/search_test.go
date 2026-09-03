package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// openTestDB opens an in-memory SQLite with the graph schema (for nodes table)
// plus the semantic tables (embeddings + entities_fts).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store.DB()
}

// seedEntity inserts one entity into both embeddings and entities_fts tables.
// vec may be nil — in that case an all-zero 4-dim vector is stored.
func seedEntity(t *testing.T, db *sql.DB, ent Entity, vec []float32) {
	t.Helper()
	if vec == nil {
		vec = make([]float32, 4)
	}
	blob := vecToBlob(vec)
	metaJSON, _ := json.Marshal(entityAnchors{
		NodeID:  ent.NodeID,
		Members: ent.Members,
		File:    ent.File,
		Line:    ent.Line,
	})
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO embeddings (entity_id, entity_type, content_hash, embedder_id, dims, vector, meta)
		VALUES (?, ?, ?, 'test-v1', ?, ?, ?)
		ON CONFLICT(entity_id) DO UPDATE SET
			entity_type=excluded.entity_type,
			content_hash=excluded.content_hash,
			embedder_id=excluded.embedder_id,
			dims=excluded.dims,
			vector=excluded.vector,
			meta=excluded.meta`,
		ent.ID, ent.Type, ent.ContentHash, len(vec), blob, string(metaJSON))
	if err != nil {
		t.Fatalf("seed entity %s: %v", ent.ID, err)
	}
	_, err = db.ExecContext(context.Background(),
		`DELETE FROM entities_fts WHERE entity_id = ?`, ent.ID)
	if err != nil {
		t.Fatalf("fts delete %s: %v", ent.ID, err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO entities_fts (entity_id, entity_type, text) VALUES (?, ?, ?)`,
		ent.ID, ent.Type, ent.Text)
	if err != nil {
		t.Fatalf("fts insert %s: %v", ent.ID, err)
	}
}

// seedNode inserts a node in both the nodes table and the entity tables.
func seedNode(t *testing.T, db *sql.DB, n *graph.Node, vec []float32) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO nodes (id, type, label, service, file, line, language, meta)
		VALUES (?, ?, ?, ?, ?, ?, '', '{}')`,
		n.ID, string(n.Type), n.Label, n.Service, n.File, n.Line)
	if err != nil {
		t.Fatalf("seed node %s: %v", n.ID, err)
	}
	cardText := n.Label + " " + string(n.Type) + " " + n.Service + " " + n.File
	seedEntity(t, db, Entity{
		ID:      n.ID,
		Type:    "node",
		Text:    cardText,
		NodeID:  n.ID,
		File:    n.File,
		Line:    n.Line,
	}, vec)
}

// ── buildFTS5Query ────────────────────────────────────────────────────────────

func TestBuildFTS5Query_SimpleWord(t *testing.T) {
	got := buildFTS5Query("checkout")
	want := "checkout*"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildFTS5Query_MultiWord(t *testing.T) {
	got := buildFTS5Query("checkout flow")
	if got != "checkout* OR flow*" {
		t.Errorf("got %q", got)
	}
}

func TestBuildFTS5Query_StripSpecialChars(t *testing.T) {
	// "user's checkout-flow" must not produce a FTS5 syntax error
	got := buildFTS5Query("user's checkout-flow")
	if got == "" {
		t.Fatal("expected non-empty FTS5 query")
	}
	// Should contain prefix stars and OR-joining
	if !contains(got, "*") {
		t.Errorf("expected prefix stars, got %q", got)
	}
	if !contains(got, "OR") {
		t.Errorf("expected OR-join, got %q", got)
	}
	// Must not contain raw special chars that would cause FTS5 parse errors
	for _, ch := range []string{"'", `"`, "-", ":"} {
		if contains(got, ch) {
			t.Errorf("FTS5 query still contains %q: %s", ch, got)
		}
	}
}

func TestBuildFTS5Query_Empty(t *testing.T) {
	if got := buildFTS5Query(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBuildFTS5Query_DottedIdentifier(t *testing.T) {
	// Regression: "build.submit" (an AMQP routing key) previously produced
	// "build.submit*", which FTS5 rejects with `syntax error near "."`.
	got := buildFTS5Query("build.submit")
	if got != "build* OR submit*" {
		t.Errorf("got %q, want %q", got, "build* OR submit*")
	}
}

func TestBuildFTS5Query_AllowlistPunctuation(t *testing.T) {
	// The allowlist keeps letters/digits/underscore and drops everything else,
	// so no unhandled punctuation can reach the FTS5 parser. Underscores stay
	// inside a token (they are valid, not FTS5 syntax).
	got := buildFTS5Query("pkg.Method{build_jobs}[0] <T> a#b&c|d\\e`f=g%h")
	for _, ch := range []string{".", "{", "}", "[", "]", "<", ">", "#", "&", "|", "\\", "`", "=", "%"} {
		if contains(got, ch) {
			t.Errorf("FTS5 query still contains %q: %s", ch, got)
		}
	}
	if !contains(got, "build_jobs*") {
		t.Errorf("expected underscore token preserved, got %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// ── isExact ───────────────────────────────────────────────────────────────────

func TestIsExact_WholeQuery(t *testing.T) {
	if !isExact("Create", "Create") {
		t.Error("exact same string should match")
	}
}

func TestIsExact_CaseSensitive(t *testing.T) {
	// isExact is case-sensitive on purpose: a lowercase descriptive word in a
	// natural-language query ("...gin handler") must not exact-match an
	// unrelated capitalized declaration ("Handler") and steal the exact-match
	// floor from the query's real identifier. See isExact's doc comment.
	if isExact("Create", "create") {
		t.Error("case-mismatched label/token should not be exact")
	}
	if !isExact("Create", "Create") {
		t.Error("case-matched label/token should be exact")
	}
}

func TestIsExact_SingleTokenOfMultiWord(t *testing.T) {
	// "Create" matches token "Create" of query "Create user"
	if !isExact("Create", "Create user") {
		t.Error("label matching a single token of multi-word query should be exact")
	}
	if isExact("Create", "create user") {
		t.Error("case-mismatched token match should not be exact")
	}
}

func TestIsExact_PrefixOnly(t *testing.T) {
	// "CreateClient" is a prefix of "create" query → NOT exact
	if isExact("CreateClient", "create") {
		t.Error("prefix-only match should not be exact")
	}
}

func TestIsExact_EmptyLabel(t *testing.T) {
	if isExact("", "create") {
		t.Error("empty label should not match")
	}
}

// ── RRF math and fusion ───────────────────────────────────────────────────────

func TestRRFFuse_Math(t *testing.T) {
	// One FTS hit at rank 1, one vector hit at rank 1 for the same entity.
	fts := []ftsHit{{EntityID: "a", EntityType: "node", Rank: 1, Label: "Alpha"}}
	vec := []rawVecHit{{entityID: "a", entityType: "node", rank: 1}}
	fused := rrfFuse(fts, vec, "Alpha")
	if len(fused) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(fused))
	}
	want := 2.0 / (float64(rrfK) + 1.0) // 1/(60+1) + 1/(60+1)
	if abs(fused[0].score-want) > 1e-9 {
		t.Errorf("score = %v, want %v", fused[0].score, want)
	}
}

func TestRRFFuse_DedupeSameEntity(t *testing.T) {
	fts := []ftsHit{{EntityID: "x", EntityType: "node", Rank: 1}}
	vec := []rawVecHit{{entityID: "x", entityType: "node", rank: 2}}
	fused := rrfFuse(fts, vec, "query")
	if len(fused) != 1 {
		t.Errorf("expected 1 deduplicated entry, got %d", len(fused))
	}
	if fused[0].retrieval != "fused" {
		t.Errorf("expected retrieval=fused, got %q", fused[0].retrieval)
	}
}

func TestRRFFuse_FTSOnly(t *testing.T) {
	fts := []ftsHit{{EntityID: "b", EntityType: "doc", Rank: 1}}
	fused := rrfFuse(fts, nil, "query")
	if len(fused) != 1 || fused[0].retrieval != "lexical" {
		t.Errorf("FTS-only hit should be lexical, got %v", fused)
	}
}

func TestRRFFuse_VectorOnly(t *testing.T) {
	vec := []rawVecHit{{entityID: "c", entityType: "flow", rank: 1}}
	fused := rrfFuse(nil, vec, "query")
	if len(fused) != 1 || fused[0].retrieval != "semantic" {
		t.Errorf("vector-only hit should be semantic, got %v", fused)
	}
}

func TestRRFFuse_ExactMatchLabel(t *testing.T) {
	fts := []ftsHit{{EntityID: "Create", EntityType: "node", Rank: 1, Label: "Create"}}
	fused := rrfFuse(fts, nil, "Create")
	if len(fused) != 1 || fused[0].retrieval != "exact" {
		t.Errorf("exact label match should be exact, got %q", fused[0].retrieval)
	}
}

func TestRRFFuse_ProductionOutranksTestFileRoute(t *testing.T) {
	// Both label-match "do-build" exactly (route last segment), but one is
	// registered only in a handler test. Production must sort first.
	fts := []ftsHit{
		{EntityID: "t", EntityType: "node", Rank: 1, Label: "POST /x/v/1/do-build",
			NodeType: string(graph.NodeTypeHTTPHandler), File: "views/start_build_handler_test.go"},
		{EntityID: "p", EntityType: "node", Rank: 2, Label: "POST /dsw/app-configs/:id/v/:v/do-build",
			NodeType: string(graph.NodeTypeHTTPHandler), File: "routes/views.go"},
	}
	fused := rrfFuse(fts, nil, "do-build")
	if fused[0].entityID != "p" {
		t.Fatalf("production route should rank first, got order %s,%s", fused[0].entityID, fused[1].entityID)
	}
	// hasStrongNodeAnchor must key on the production hit, not the test one.
	if !hasStrongNodeAnchor(fused, "do-build") {
		t.Error("production exact route should be a strong anchor")
	}
}

func TestRRFFuse_TestOnlyMatch_NotStrongAnchor(t *testing.T) {
	fts := []ftsHit{
		{EntityID: "t", EntityType: "node", Rank: 1, Label: "POST /x/do-build",
			NodeType: string(graph.NodeTypeHTTPHandler), File: "handler_test.go"},
	}
	fused := rrfFuse(fts, nil, "do-build")
	if hasStrongNodeAnchor(fused, "do-build") {
		t.Error("a test-only match must not suppress the weak-match advisory")
	}
}

func TestHasStrongNodeAnchor_TestHitAllowedWhenQueryTargetsTests(t *testing.T) {
	// Same exact test-file hit, but the query itself names "test": the
	// demotion is lifted and the hit counts as an anchor.
	fused := []fusedEntry{
		{entityID: "t", entityType: "node", retrieval: "exact", isTest: true},
	}
	if hasStrongNodeAnchor(fused, "do-build") {
		t.Error("test hit should be demoted for a plain query")
	}
	if !hasStrongNodeAnchor(fused, "do-build test") {
		t.Error("test hit should anchor when the query targets tests")
	}
}

func TestRRFFuse_FanOut_MultipleEntitiesSameKey(t *testing.T) {
	// Bug-class rule 1: fan-out — two entities sharing a match must both appear.
	fts := []ftsHit{
		{EntityID: "a", EntityType: "node", Rank: 1},
		{EntityID: "b", EntityType: "node", Rank: 2},
	}
	fused := rrfFuse(fts, nil, "q")
	if len(fused) != 2 {
		t.Errorf("expected 2 fused entries (fan-out), got %d", len(fused))
	}
}

// ── Determinism ───────────────────────────────────────────────────────────────

func TestRRFFuse_DeterministicTies(t *testing.T) {
	// Two vector-only hits with identical scores (same rank) must sort by entity ID.
	vec := []rawVecHit{
		{entityID: "z-entity", entityType: "node", rank: 1},
		{entityID: "a-entity", entityType: "node", rank: 1},
	}
	run1 := rrfFuse(nil, vec, "q")
	run2 := rrfFuse(nil, vec, "q")
	if len(run1) != 2 || len(run2) != 2 {
		t.Fatalf("expected 2 entries")
	}
	if run1[0].entityID != run2[0].entityID || run1[1].entityID != run2[1].entityID {
		t.Error("non-deterministic output across two runs")
	}
	// a-entity should come before z-entity (entity ID alphabetical order).
	if run1[0].entityID != "a-entity" {
		t.Errorf("expected a-entity first, got %q", run1[0].entityID)
	}
}

// ── Exact-match floor (bug-class rule 9) ──────────────────────────────────────

func TestSearch_ExactMatchFloor(t *testing.T) {
	// Regression: query "Create" with corpus [Create, CreateClient].
	// "Create" must rank above "CreateClient" regardless of BM25/vector scores.
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:Create", Type: graph.NodeTypeFunction,
		Label: "Create", Service: "svc", File: "a.go", Line: 1,
	}, []float32{0.9, 0, 0, 0})
	seedNode(t, db, &graph.Node{
		ID: "fn:CreateClient", Type: graph.NodeTypeFunction,
		Label: "CreateClient", Service: "svc", File: "b.go", Line: 1,
	}, []float32{0.9, 0, 0, 0}) // same vector score as Create

	emb := &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}
	sr := NewSearcher(sem, emb, nil)

	ctx := context.Background()
	resp, err := sr.Search(ctx, "Create", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) < 2 {
		t.Fatalf("expected ≥2 node results, got %d", len(resp.Nodes))
	}
	if resp.Nodes[0].Entity.ID != "fn:Create" {
		t.Errorf("exact match Create must rank first, got %q", resp.Nodes[0].Entity.ID)
	}
	if resp.Nodes[0].Retrieval != "exact" {
		t.Errorf("exact match must have retrieval=exact, got %q", resp.Nodes[0].Retrieval)
	}
}

// TestSearch_ExactMatchTightensResponse reproduces the juniper bench
// finding: querying the exact symbol name "RemoveConfig" returned an
// 18KB, 20-deep ranked node list plus flow/doc sections for a query that
// only needed the 1-2 nodes actually named RemoveConfig. When the top hit
// is an exact-label match, the response should collapse to a handful of
// nodes and trim flows/docs to a small cap (not zero — a later bench trial
// on "heartbeat" showed the exact match can itself be an unrelated,
// coincidentally-same-named symbol, and zeroing flows/docs then silently
// discards the one flow that would have actually answered the query).
func TestSearch_ExactMatchTightensResponse(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	// The exact target, plus enough loosely-related lexical noise to fill a
	// limit=20 window if the tightening didn't kick in.
	seedNode(t, db, &graph.Node{
		ID: "fn:RemoveConfig", Type: graph.NodeTypeFunction,
		Label: "RemoveConfig", Service: "maple-agent", File: "config_handlers.go", Line: 1,
	}, []float32{0.9, 0, 0, 0})
	for i := 0; i < 15; i++ {
		seedNode(t, db, &graph.Node{
			ID: fmt.Sprintf("fn:RemoveConfigVariant%d", i), Type: graph.NodeTypeFunction,
			Label: fmt.Sprintf("RemoveConfigVariant%d", i), Service: "maple-agent",
			File: "other.go", Line: i + 2,
		}, []float32{0.1, 0, 0, 0})
	}
	seedEntity(t, db, Entity{
		ID: "chain:RemoveConfig:flow", Type: "flow",
		Text: "RemoveConfig flow chain", NodeID: "fn:RemoveConfig",
	}, nil)

	sr := NewSearcher(sem, &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}, nil)
	ctx := context.Background()

	resp, err := sr.Search(ctx, "RemoveConfig", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Nodes[0].Entity.ID != "fn:RemoveConfig" || resp.Nodes[0].Retrieval != "exact" {
		t.Fatalf("expected exact match first, got %+v", resp.Nodes[0])
	}
	if len(resp.Nodes) > exactMatchNodeCap {
		t.Errorf("exact match should cap nodes at %d, got %d", exactMatchNodeCap, len(resp.Nodes))
	}
	if len(resp.Flows) > exactMatchFlowCap {
		t.Errorf("exact match should cap flows at %d, got %d", exactMatchFlowCap, len(resp.Flows))
	}
}

// TestSearch_NoExactMatchKeepsFullLimit confirms the tightening in
// TestSearch_ExactMatchTightensResponse is conditional on an exact hit, not
// a general cap: an ambiguous/semantic query still gets the full requested
// window so recall on genuinely fuzzy queries doesn't regress.
func TestSearch_NoExactMatchKeepsFullLimit(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	for i := 0; i < 8; i++ {
		seedNode(t, db, &graph.Node{
			ID: fmt.Sprintf("fn:HandleThing%d", i), Type: graph.NodeTypeFunction,
			Label: fmt.Sprintf("HandleThing%d", i), Service: "svc", File: "a.go", Line: i + 1,
		}, []float32{0.5, 0, 0, 0})
	}

	sr := NewSearcher(sem, &stubEmbedder{dims: 4, vec: []float32{0.5, 0, 0, 0}}, nil)
	ctx := context.Background()

	resp, err := sr.Search(ctx, "handle thing broadly", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) > 0 && resp.Nodes[0].Retrieval == "exact" {
		t.Fatalf("test setup invalid: got an exact match, want a fuzzy query")
	}
	if len(resp.Nodes) != 8 {
		t.Errorf("non-exact query should return the full corpus within limit, got %d", len(resp.Nodes))
	}
}

// ── Degradation (--no-embed) ──────────────────────────────────────────────────

func TestSearch_NilEmbedder_FTSOnly(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:getUser", Type: graph.NodeTypeFunction,
		Label: "getUser", Service: "svc", File: "user.go", Line: 5,
	}, nil)

	sr := NewSearcher(sem, nil, nil) // nil embedder → FTS-only
	ctx := context.Background()

	resp, err := sr.Search(ctx, "getUser", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) == 0 {
		t.Error("FTS-only search should still return node results")
	}
	if resp.Nodes[0].Entity.ID != "fn:getUser" {
		t.Errorf("expected fn:getUser, got %q", resp.Nodes[0].Entity.ID)
	}
	if resp.Semantic == "" {
		t.Error("nil embedder must set Semantic degradation note")
	}
	if !startsWith(resp.Semantic, "unavailable:") {
		t.Errorf("degradation note should start with 'unavailable:', got %q", resp.Semantic)
	}
}

// ── Glossary expansion ────────────────────────────────────────────────────────

func TestSearch_GlossaryExpansion(t *testing.T) {
	// "Falcon" is a jargon term; workspace synonyms map it to "purchase".
	// Searching "Falcon" with synonyms should find the "handlePurchase" node.
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:handlePurchase", Type: graph.NodeTypeFunction,
		Label: "handlePurchase", Service: "api", File: "purchase.go", Line: 10,
	}, nil)

	sr := NewSearcher(sem, nil, map[string][]string{
		"falcon": {"purchase"},
	})
	ctx := context.Background()

	resp, err := sr.Search(ctx, "falcon", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, h := range resp.Nodes {
		if h.Entity.ID == "fn:handlePurchase" {
			found = true
		}
	}
	if !found {
		t.Error("glossary expansion 'falcon'→'purchase' should find handlePurchase")
	}
}

// ── Two-run determinism (bug-class rule 2) ────────────────────────────────────

func TestSearch_TwoRunDeterminism(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	for i, n := range []string{"Alpha", "Beta", "Gamma", "Delta"} {
		id := "fn:" + n
		seedNode(t, db, &graph.Node{
			ID: id, Type: graph.NodeTypeFunction,
			Label: n, Service: "svc", File: "f.go", Line: i + 1,
		}, []float32{float32(i + 1), 0, 0, 0})
	}

	emb := &stubEmbedder{dims: 4, vec: []float32{1, 0, 0, 0}}
	sr := NewSearcher(sem, emb, nil)
	ctx := context.Background()

	resp1, err := sr.Search(ctx, "alpha beta", 10)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	sr.Invalidate() // force matrix reload for second run
	resp2, err := sr.Search(ctx, "alpha beta", 10)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}

	j1, _ := json.Marshal(resp1)
	j2, _ := json.Marshal(resp2)
	if string(j1) != string(j2) {
		t.Errorf("non-deterministic output:\nrun1: %s\nrun2: %s", j1, j2)
	}
}

// ── Typed sections and file/line enrichment ───────────────────────────────────

func TestSearch_NodeFileLineEnriched(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:doThing", Type: graph.NodeTypeFunction,
		Label: "doThing", Service: "svc", File: "thing.go", Line: 42,
	}, nil)

	sr := NewSearcher(sem, nil, nil)
	ctx := context.Background()

	resp, err := sr.Search(ctx, "doThing", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Nodes) == 0 {
		t.Fatal("expected 1 node result")
	}
	hit := resp.Nodes[0]
	if hit.Entity.File != "thing.go" {
		t.Errorf("expected file thing.go, got %q", hit.Entity.File)
	}
	if hit.Entity.Line != 42 {
		t.Errorf("expected line 42, got %d", hit.Entity.Line)
	}
}

func TestSearch_FlowEntityReturned(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	flowEnt := Entity{
		ID:      "chain:route:POST /orders:8f3a",
		Type:    "flow",
		Text:    "handlePurchase http_handler checkout orders purchase",
		NodeID:  "route:POST /orders",
		Members: []string{"route:POST /orders", "fn:handlePurchase"},
		File:    "api.go",
		Line:    10,
	}
	seedEntity(t, db, flowEnt, nil)

	sr := NewSearcher(sem, nil, nil)
	ctx := context.Background()

	resp, err := sr.Search(ctx, "handlePurchase", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, h := range resp.Flows {
		if h.Entity.ID == flowEnt.ID {
			found = true
			if h.Entity.NodeID != "route:POST /orders" {
				t.Errorf("NodeID mismatch: got %q", h.Entity.NodeID)
			}
		}
	}
	if !found {
		t.Errorf("flow entity not returned; flows=%v", resp.Flows)
	}
}

// ── FTSSearch ─────────────────────────────────────────────────────────────────

func TestFTSSearch_ReturnsResults(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:loginUser", Type: graph.NodeTypeFunction,
		Label: "loginUser", Service: "auth", File: "login.go", Line: 1,
	}, nil)

	ctx := context.Background()
	hits, err := sem.FTSSearch(ctx, buildFTS5Query("login"), 10)
	if err != nil {
		t.Fatalf("fts search: %v", err)
	}
	if len(hits) == 0 {
		t.Error("expected at least one FTS hit for 'login'")
	}
	if hits[0].Label != "loginUser" {
		t.Errorf("expected label loginUser, got %q", hits[0].Label)
	}
}

// The corpus shape that broke node retrieval on fleet-juniper: a central
// handler sits on hundreds of flow chains but has only a handful of
// definitions, so a pooled `ORDER BY rank LIMIT n` spends the window on flows.
// Each type must get its own quota.
func TestFTSSearchPerType_FlowsDoNotStarveNodes(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)
	ctx := context.Background()

	for i := 0; i < 300; i++ {
		seedEntity(t, db, Entity{
			ID:   fmt.Sprintf("chain:route:%d", i),
			Type: "flow",
			Text: fmt.Sprintf("POST /save SaveConfig step%d", i),
		}, nil)
	}
	for _, f := range []string{"app_config_handler.go", "base_image_handler.go", "exec_config_handler.go"} {
		seedNode(t, db, &graph.Node{
			ID: "maple:" + f + ":method:SaveConfig", Type: graph.NodeTypeMethod,
			Label: "SaveConfig", Service: "maple", File: f, Line: 1,
		}, nil)
	}

	hits, err := sem.FTSSearchPerType(ctx, buildFTS5Query("SaveConfig"), 50)
	if err != nil {
		t.Fatalf("fts search per type: %v", err)
	}
	nodes := map[string]bool{}
	for _, h := range hits {
		if h.EntityType == "node" {
			nodes[h.EntityID] = true
		}
	}
	if len(nodes) != 3 {
		t.Errorf("all 3 SaveConfig methods must survive 300 competing flows, got %d", len(nodes))
	}
}

// Ranks restart at 1 for each type so RRF compares like with like; a pooled
// rank would score the first flow above every node.
func TestFTSSearchPerType_RanksAreScopedToType(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		seedEntity(t, db, Entity{
			ID: fmt.Sprintf("chain:%d", i), Type: "flow",
			Text: fmt.Sprintf("checkout flow %d", i),
		}, nil)
	}
	seedNode(t, db, &graph.Node{
		ID: "svc:checkout.go:function:checkout", Type: graph.NodeTypeFunction,
		Label: "checkout", Service: "svc", File: "checkout.go", Line: 1,
	}, nil)

	hits, err := sem.FTSSearchPerType(ctx, buildFTS5Query("checkout"), 50)
	if err != nil {
		t.Fatalf("fts search per type: %v", err)
	}
	best := map[string]int{}
	for _, h := range hits {
		if r, seen := best[h.EntityType]; !seen || h.Rank < r {
			best[h.EntityType] = h.Rank
		}
	}
	for et, r := range best {
		if r != 1 {
			t.Errorf("type %q best rank = %d, want 1", et, r)
		}
	}
}

func TestFTSSearch_NLQuerySafe(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)
	ctx := context.Background()
	// These would each be an FTS5 syntax error if passed raw; buildFTS5Query
	// must sanitise them. "build.submit" is the exact input that crashed
	// `polyflow search` with `fts5: syntax error near "."`.
	for _, q := range []string{"user's checkout-flow", "build.submit", "pkg.Method(arg)", "a<b>c{d}"} {
		if _, err := sem.FTSSearch(ctx, buildFTS5Query(q), 10); err != nil {
			t.Errorf("sanitised query %q should not cause FTS error: %v", q, err)
		}
	}
}

// ── LoadEntitiesByIDs ─────────────────────────────────────────────────────────

func TestLoadEntitiesByIDs_NodeEnrichment(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)

	seedNode(t, db, &graph.Node{
		ID: "fn:foo", Type: graph.NodeTypeFunction,
		Label: "foo", Service: "svc", File: "foo.go", Line: 7,
	}, nil)

	ctx := context.Background()
	m, err := sem.LoadEntitiesByIDs(ctx, []string{"fn:foo"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ent, ok := m["fn:foo"]
	if !ok {
		t.Fatal("fn:foo not found")
	}
	if ent.File != "foo.go" {
		t.Errorf("file: got %q", ent.File)
	}
	if ent.Line != 7 {
		t.Errorf("line: got %d", ent.Line)
	}
}

// ── GetEmbedStatus ────────────────────────────────────────────────────────────

func TestGetEmbedStatus_Missing(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)
	// No embed_status row yet → should return ""
	got := sem.GetEmbedStatus(context.Background())
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestGetEmbedStatus_Present(t *testing.T) {
	db := openTestDB(t)
	sem := NewStore(db)
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES ('embed_status', 'ok')`)
	got := sem.GetEmbedStatus(ctx)
	if got != "ok" {
		t.Errorf("expected ok, got %q", got)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// stubEmbedder always returns the same fixed vector for any text.
type stubEmbedder struct {
	dims int
	vec  []float32
}

func (e *stubEmbedder) ID() string   { return "stub-v1" }
func (e *stubEmbedder) Dims() int    { return e.dims }
func (e *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		v := make([]float32, e.dims)
		copy(v, e.vec)
		out[i] = v
	}
	return out, nil
}
