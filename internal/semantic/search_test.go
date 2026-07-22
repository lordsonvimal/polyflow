package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestIsExact_CaseInsensitive(t *testing.T) {
	if !isExact("Create", "create") {
		t.Error("case-insensitive exact match should work")
	}
}

func TestIsExact_SingleTokenOfMultiWord(t *testing.T) {
	// "Create" matches token "create" of query "create user"
	if !isExact("Create", "create user") {
		t.Error("label matching a single token of multi-word query should be exact")
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
