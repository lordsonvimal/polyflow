package graph_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// stubSearcher implements graph.NodeSearcher for unit tests.
type stubSearcher struct {
	nodes []*graph.Node
	err   error
}

func (s *stubSearcher) SearchNodes(_ context.Context, query string, limit int) ([]*graph.Node, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := s.nodes
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func node(id, label, service, file, typ string) *graph.Node {
	return &graph.Node{ID: id, Label: label, Service: service, File: file, Type: graph.NodeType(typ)}
}

// stubSearcherWithGetNode adds the optional GetNode capability on top of
// stubSearcher, so tests can exercise ResolveTarget's ID short-circuit the
// same way *SQLiteStore does in production.
type stubSearcherWithGetNode struct {
	stubSearcher
	byID map[string]*graph.Node
}

func (s *stubSearcherWithGetNode) GetNode(_ context.Context, id string) (*graph.Node, error) {
	if n, ok := s.byID[id]; ok {
		return n, nil
	}
	return nil, fmt.Errorf("not found: %s", id)
}

// ── filter unit tests ─────────────────────────────────────────────────────────

func TestResolveTarget_NoFilters(t *testing.T) {
	// Without filters: returns nodes[0] (backward-compat) and empty candidates.
	n := node("id1", "Login", "server", "api/session.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "Login", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "id1" {
		t.Fatalf("want root id1, got %s", root.ID)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (unambiguous), got %d", len(cands))
	}
}

func TestResolveTarget_ServiceFilter(t *testing.T) {
	// Two exact-label matches in different services; service filter picks the right one.
	n1 := node("ui-login", "Login", "ui", "ui/src/Login.tsx", "component")
	n2 := node("srv-login", "Login", "server", "api/session.go", "function")
	// SearchNodes ranks ui first (simulating the ambiguous case).
	s := &stubSearcher{nodes: []*graph.Node{n1, n2}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "Login", "server", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "srv-login" {
		t.Fatalf("want srv-login, got %s", root.ID)
	}
	// Two exact matches → candidates populated, sorted by (service, file).
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}
	if cands[0].Service != "server" || cands[1].Service != "ui" {
		t.Fatalf("candidates not sorted by service: %v", cands)
	}
}

func TestResolveTarget_TypeFilter(t *testing.T) {
	// Filter by node type picks the right node.
	n1 := node("comp", "Login", "ui", "ui/src/Login.tsx", "component")
	n2 := node("fn", "Login", "server", "api/session.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "Login", "", "function")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "fn" {
		t.Fatalf("want fn, got %s", root.ID)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}
}

func TestResolveTarget_BothFilters(t *testing.T) {
	// Both filters applied together.
	n1 := node("comp", "Login", "ui", "ui/src/Login.tsx", "component")
	n2 := node("fn", "server", "server", "api/session.go", "function")
	n3 := node("fn2", "Login", "server", "api/other.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2, n3}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "Login", "server", "function")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// n3 is the only exact-label match for service=server + type=function.
	// n2's Label is "server" not "Login" so not an exact match.
	if root.ID != "fn2" {
		t.Fatalf("want fn2, got %s", root.ID)
	}
	// Only n1 and n3 are exact-label matches; 2 matches → candidates populated.
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates (n1+n3), got %d: %v", len(cands), cands)
	}
}

func TestResolveTarget_Unambiguous(t *testing.T) {
	// Single exact-label match → candidates empty.
	n := node("only", "UniqFunc", "svc", "pkg/foo.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "UniqFunc", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "only" {
		t.Fatalf("want only, got %s", root.ID)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates (unambiguous), got %d", len(cands))
	}
}

func TestResolveTarget_Ambiguity(t *testing.T) {
	// >1 exact-label match → candidates sorted by (service, file).
	n1 := node("b-pkg", "Foo", "bravo", "b/foo.go", "function")
	n2 := node("a-pkg2", "Foo", "alpha", "b/foo.go", "function")
	n3 := node("a-pkg1", "Foo", "alpha", "a/foo.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2, n3}}
	_, cands, _, err := graph.ResolveTarget(context.Background(), s, "Foo", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(cands))
	}
	// Sorted: alpha/a/foo.go < alpha/b/foo.go < bravo/b/foo.go
	if cands[0].ID != "a-pkg1" || cands[1].ID != "a-pkg2" || cands[2].ID != "b-pkg" {
		t.Fatalf("wrong sort order: %v", cands)
	}
}

func TestResolveTarget_PrefersNonTestFileOnTie(t *testing.T) {
	// Two exact-label matches, same service+type, no explicit disambiguating
	// filter: a same-named mock/helper in a test file must not win over the
	// real production declaration just because it ranks first in search.
	mock := node("mock", "renderFileIcon", "ui", "src/Icon.test.jsx", "function")
	prod := node("prod", "renderFileIcon", "ui", "src/Icon.jsx", "function")
	s := &stubSearcher{nodes: []*graph.Node{mock, prod}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "renderFileIcon", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "prod" {
		t.Fatalf("want prod (non-test file) to win the tie, got %s", root.ID)
	}
	// Both matches must still be visible — the tie-break changes the default
	// pick, never hides the ambiguity.
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates (ambiguity still surfaced), got %d", len(cands))
	}
}

func TestResolveTarget_AllTestFileMatchesFallsBackToFirst(t *testing.T) {
	// Every exact match is in a test file (e.g. two spec helpers of the same
	// name) — must still resolve to something rather than erroring.
	n1 := node("t1", "helper", "svc", "a_test.go", "function")
	n2 := node("t2", "helper", "svc", "b_test.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2}}
	root, _, _, err := graph.ResolveTarget(context.Background(), s, "helper", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "t1" {
		t.Fatalf("want first match t1 when all matches are test files, got %s", root.ID)
	}
}

func TestResolveTarget_PrefixMatchFallback(t *testing.T) {
	// No exact-label match (prefix-only) → root = nodes[0], candidates empty,
	// and exactMatch = false: this is the fuzzy-guess path a caller must be
	// able to tell apart from a confirmed hit (see TestResolveTarget_ExactMatchFlag).
	n := node("id1", "LoginPage", "ui", "pages/login.go", "component")
	s := &stubSearcher{nodes: []*graph.Node{n}}
	root, cands, exactMatch, err := graph.ResolveTarget(context.Background(), s, "Login", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != "id1" {
		t.Fatalf("want id1, got %s", root.ID)
	}
	if len(cands) != 0 {
		t.Fatalf("want 0 candidates, got %d", len(cands))
	}
	if exactMatch {
		t.Fatal("want exactMatch=false for a prefix-only match")
	}
}

// TestResolveTarget_ExactMatchFlag pins the exactMatch return across the
// three cases that must be told apart: a real exact-label hit, a fuzzy
// fallback with nothing named the query at all, and the ID short-circuit.
// Added after a live bench trial guessed a nonexistent function name
// ("PublishDockerBuild") and silently got back the containing file — with no
// signal distinguishing that from a genuine match, the agent trusted the
// wrong result for several tool calls before falling back to manual grep.
func TestResolveTarget_ExactMatchFlag(t *testing.T) {
	t.Run("exact label match", func(t *testing.T) {
		n := node("id1", "PublishBuildJob", "svc", "publishers.go", "function")
		s := &stubSearcher{nodes: []*graph.Node{n}}
		_, _, exactMatch, err := graph.ResolveTarget(context.Background(), s, "PublishBuildJob", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exactMatch {
			t.Fatal("want exactMatch=true for a real exact-label hit")
		}
		if note := graph.ResolutionNote("PublishBuildJob", exactMatch); note != "" {
			t.Fatalf("want no resolution note on an exact match, got %q", note)
		}
	})

	t.Run("fuzzy fallback, nothing named the query", func(t *testing.T) {
		n := node("id1", "publishBuildJobToAMQP", "svc", "docker_build_service.go", "function")
		s := &stubSearcher{nodes: []*graph.Node{n}} // ranks first as free text, but not an exact label match
		root, _, exactMatch, err := graph.ResolveTarget(context.Background(), s, "PublishDockerBuild", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if root.ID != "id1" {
			t.Fatalf("want the fuzzy fallback to still return the top FTS hit, got %s", root.ID)
		}
		if exactMatch {
			t.Fatal("want exactMatch=false: nothing in the index is labeled PublishDockerBuild")
		}
		note := graph.ResolutionNote("PublishDockerBuild", exactMatch)
		if note == "" {
			t.Fatal("want a non-empty resolution note when exactMatch=false")
		}
	})

	t.Run("literal ID short-circuit counts as exact", func(t *testing.T) {
		wanted := node("svc:file.go:function:Foo:1", "Foo", "svc", "file.go", "function")
		s := &stubSearcherWithGetNode{
			stubSearcher: stubSearcher{nodes: []*graph.Node{}},
			byID:         map[string]*graph.Node{wanted.ID: wanted},
		}
		_, _, exactMatch, err := graph.ResolveTarget(context.Background(), s, wanted.ID, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exactMatch {
			t.Fatal("want exactMatch=true for a literal node-ID hit")
		}
	})
}

func TestResolveTarget_NotFound(t *testing.T) {
	s := &stubSearcher{nodes: nil}
	_, _, _, err := graph.ResolveTarget(context.Background(), s, "Missing", "", "")
	if err == nil {
		t.Fatal("want error for missing node")
	}
}

func TestResolveTarget_StoreError(t *testing.T) {
	s := &stubSearcher{err: fmt.Errorf("db fail")}
	_, _, _, err := graph.ResolveTarget(context.Background(), s, "Any", "", "")
	if err == nil {
		t.Fatal("want error on store failure")
	}
}

// TestResolveTarget_ExactIDShortCircuit reproduces the juniper AMQP bench
// finding: querying trace with the literal node ID of the manager-side
// "runner_heartbeat/heartbeat" consumer channel returned the unrelated
// maple-agent producer channel instead, because ResolveTarget only ever matched
// on Label and the ID never equals the bare label. With GetNode wired, an
// ID-shaped query must resolve to exactly that node, not to whatever
// SearchNodes(query) ranks first as free text.
func TestResolveTarget_ExactIDShortCircuit(t *testing.T) {
	wanted := node("maple-manager:channel:runner_heartbeat/heartbeat", "runner_heartbeat/heartbeat", "maple-manager", "exchanges.go", "channel")
	wrong := node("maple-agent:channel:runner_heartbeat/", "runner_heartbeat/", "maple-agent", "", "channel")
	s := &stubSearcherWithGetNode{
		stubSearcher: stubSearcher{nodes: []*graph.Node{wrong}}, // what label search would (wrongly) rank first
		byID:         map[string]*graph.Node{wanted.ID: wanted},
	}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, wanted.ID, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != wanted.ID {
		t.Fatalf("want root %s, got %s", wanted.ID, root.ID)
	}
	if len(cands) != 0 {
		t.Fatalf("want no candidates for an unambiguous ID match, got %v", cands)
	}
}

// TestResolveTarget_IDMissFallsBackToLabelSearch confirms a query shaped like
// an ID but not present in the graph still falls through to the normal
// label-based path instead of erroring out on the GetNode miss.
func TestResolveTarget_IDMissFallsBackToLabelSearch(t *testing.T) {
	n := node("svc:file.go:function:Login:10", "Login", "server", "file.go", "function")
	s := &stubSearcherWithGetNode{
		stubSearcher: stubSearcher{nodes: []*graph.Node{n}},
		byID:         map[string]*graph.Node{}, // GetNode always misses
	}
	root, _, _, err := graph.ResolveTarget(context.Background(), s, "svc:file.go:function:Login:10", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root.ID != n.ID {
		t.Fatalf("want fallback to label search result %s, got %s", n.ID, root.ID)
	}
}

// ── determinism test (rule 2) ────────────────────────────────────────────────

func TestResolveTarget_Determinism(t *testing.T) {
	// Two exact matches → candidates must be byte-identical across two calls.
	n1 := node("ui-login", "Login", "ui", "ui/src/Login.tsx", "component")
	n2 := node("srv-login", "Login", "server", "api/session.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2}}
	ctx := context.Background()

	_, c1, _, err := graph.ResolveTarget(ctx, s, "Login", "", "")
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	_, c2, _, err := graph.ResolveTarget(ctx, s, "Login", "", "")
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	b1, _ := json.Marshal(c1)
	b2, _ := json.Marshal(c2)
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic output:\nrun1: %s\nrun2: %s", b1, b2)
	}
}

// ── back-compat: no filters behaves like SearchNodes[0] ──────────────────────

func TestResolveTarget_BackCompat(t *testing.T) {
	// With no filters and a single exact match, root is nodes[0].
	// This asserts the pre-B.3 behavior is preserved.
	n1 := node("first", "Foo", "svc", "a.go", "function")
	n2 := node("second", "FooBar", "svc", "b.go", "function")
	s := &stubSearcher{nodes: []*graph.Node{n1, n2}}
	root, cands, _, err := graph.ResolveTarget(context.Background(), s, "Foo", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// n1 is the only exact-label match; n2 is prefix-only → root=n1, cands=[].
	if root.ID != "first" {
		t.Fatalf("want first (exact), got %s", root.ID)
	}
	if len(cands) != 0 {
		t.Fatalf("want empty candidates (unambiguous), got %d", len(cands))
	}
}

// ── fan-out: two exact matches both appear in candidates (rule 1) ─────────────

func TestResolveTarget_FanOut(t *testing.T) {
	// Rule 1: when a key (label) maps to multiple nodes, ALL are in candidates.
	nodes := make([]*graph.Node, 0, 5)
	for i := 0; i < 5; i++ {
		nodes = append(nodes, node(
			fmt.Sprintf("id%d", i),
			"Save",
			fmt.Sprintf("svc%d", i),
			fmt.Sprintf("file%d.go", i),
			"function",
		))
	}
	s := &stubSearcher{nodes: nodes}
	_, cands, _, err := graph.ResolveTarget(context.Background(), s, "Save", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cands) != 5 {
		t.Fatalf("want 5 candidates (all exact matches), got %d", len(cands))
	}
}
