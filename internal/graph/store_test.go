package graph_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *graph.SQLiteStore {
	t.Helper()
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func nodeFixture(id string) *graph.Node {
	return &graph.Node{
		ID:       id,
		Type:     graph.NodeTypeFunction,
		Label:    "func_" + id,
		Service:  "svc",
		File:     "main.go",
		Line:     10,
		Language: "go",
	}
}

func edgeFixture(id, from, to string) *graph.Edge {
	return &graph.Edge{
		ID:   id,
		From: from,
		To:   to,
		Type: graph.EdgeTypeCalls,
	}
}

func TestUpsertAndGetNode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n := nodeFixture("n1")
	n.Meta = map[string]string{"key": "value"}

	require.NoError(t, s.UpsertNode(ctx, n))

	got, err := s.GetNode(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, n.ID, got.ID)
	assert.Equal(t, n.Type, got.Type)
	assert.Equal(t, n.Label, got.Label)
	assert.Equal(t, n.Meta, got.Meta)
}

func TestUpsertNodeIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n := nodeFixture("n1")
	require.NoError(t, s.UpsertNode(ctx, n))

	n.Label = "updated_label"
	require.NoError(t, s.UpsertNode(ctx, n))

	got, err := s.GetNode(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, "updated_label", got.Label)
}

func TestGetNodeNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetNode(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestUpsertAndGetEdge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n1")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n2")))

	e := edgeFixture("e1", "n1", "n2")
	e.Meta = map[string]string{"confidence": "static"}
	require.NoError(t, s.UpsertEdge(ctx, e))

	got, err := s.GetEdge(ctx, "e1")
	require.NoError(t, err)
	assert.Equal(t, e.ID, got.ID)
	assert.Equal(t, e.From, got.From)
	assert.Equal(t, e.To, got.To)
	assert.Equal(t, e.Meta, got.Meta)
}

func TestListEdgesFromTo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"n1", "n2", "n3"} {
		require.NoError(t, s.UpsertNode(ctx, nodeFixture(id)))
	}
	require.NoError(t, s.UpsertEdge(ctx, edgeFixture("e1", "n1", "n2")))
	require.NoError(t, s.UpsertEdge(ctx, edgeFixture("e2", "n1", "n3")))
	require.NoError(t, s.UpsertEdge(ctx, edgeFixture("e3", "n2", "n3")))

	fromN1, err := s.ListEdgesFrom(ctx, "n1")
	require.NoError(t, err)
	assert.Len(t, fromN1, 2)

	toN3, err := s.ListEdgesTo(ctx, "n3")
	require.NoError(t, err)
	assert.Len(t, toN3, 2)

	fromN3, err := s.ListEdgesFrom(ctx, "n3")
	require.NoError(t, err)
	assert.Empty(t, fromN3)
}

func TestSearchNodes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	nodes := []*graph.Node{
		{ID: "a", Type: graph.NodeTypeFunction, Label: "CreateUser", Service: "api", File: "user.go", Language: "go"},
		{ID: "b", Type: graph.NodeTypeFunction, Label: "CreateProject", Service: "api", File: "project.go", Language: "go"},
		{ID: "c", Type: graph.NodeTypeFunction, Label: "DeleteUser", Service: "api", File: "user.go", Language: "go"},
	}
	for _, n := range nodes {
		require.NoError(t, s.UpsertNode(ctx, n))
	}

	results, err := s.SearchNodes(ctx, "Create", 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)

	results, err = s.SearchNodes(ctx, "User", 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)

	results, err = s.SearchNodes(ctx, "Delete", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestSearchNodesLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		n := &graph.Node{
			ID:       fmt.Sprintf("n%d", i),
			Type:     graph.NodeTypeFunction,
			Label:    fmt.Sprintf("Handle%d", i),
			Service:  "svc",
			File:     "main.go",
			Language: "go",
		}
		require.NoError(t, s.UpsertNode(ctx, n))
	}

	results, err := s.SearchNodes(ctx, "Handle", 3)
	require.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	nodes, edges, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, nodes)
	assert.Equal(t, 0, edges)

	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n1")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n2")))
	require.NoError(t, s.UpsertEdge(ctx, edgeFixture("e1", "n1", "n2")))

	nodes, edges, err = s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, nodes)
	assert.Equal(t, 1, edges)
}

func TestUpsertAndListParseErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	pe := &graph.ParseError{
		FilePath:       "internal/handlers/broken.go",
		Service:        "svc",
		ErrorCount:     2,
		FirstErrorLine: 17,
		IndexedAt:      1700000000,
	}
	require.NoError(t, s.UpsertParseError(ctx, pe))

	list, err := s.ListParseErrors(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, pe.FilePath, list[0].FilePath)
	assert.Equal(t, pe.Service, list[0].Service)
	assert.Equal(t, pe.ErrorCount, list[0].ErrorCount)
	assert.Equal(t, pe.FirstErrorLine, list[0].FirstErrorLine)
	assert.Equal(t, pe.IndexedAt, list[0].IndexedAt)
}

func TestUpsertParseErrorIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	pe := &graph.ParseError{FilePath: "a.go", Service: "svc", ErrorCount: 1, IndexedAt: 100}
	require.NoError(t, s.UpsertParseError(ctx, pe))

	pe.ErrorCount = 3
	pe.IndexedAt = 200
	require.NoError(t, s.UpsertParseError(ctx, pe))

	list, err := s.ListParseErrors(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "upsert should not duplicate rows")
	assert.Equal(t, 3, list[0].ErrorCount)
}

func TestListParseErrors_Empty(t *testing.T) {
	s := newTestStore(t)
	list, err := s.ListParseErrors(context.Background())
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSetAndGetMeta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetMeta(ctx, "last_indexed", "1700000000"))

	val, err := s.GetMeta(ctx, "last_indexed")
	require.NoError(t, err)
	assert.Equal(t, "1700000000", val)
}

func TestGetMeta_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetMeta(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestSetMeta_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.SetMeta(ctx, "key", "v1"))
	require.NoError(t, s.SetMeta(ctx, "key", "v2"))

	val, err := s.GetMeta(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "v2", val)
}

// BenchmarkSearchNodes measures FTS5 search on a 10k node store.
func BenchmarkSearchNodes(b *testing.B) {
	s, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	bw := graph.NewBatchWriter(s)
	for i := 0; i < 10000; i++ {
		n := &graph.Node{
			ID:       fmt.Sprintf("n%d", i),
			Type:     graph.NodeTypeFunction,
			Label:    fmt.Sprintf("HandleRequest%d", i),
			Service:  "svc",
			File:     fmt.Sprintf("handler_%d.go", i),
			Language: "go",
		}
		_ = bw.AddNode(ctx, n)
	}
	_ = bw.Flush(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.SearchNodes(ctx, "HandleRequest", 20)
	}
}

func TestBuildIndex(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n1")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n2")))
	require.NoError(t, s.UpsertEdge(ctx, edgeFixture("e1", "n1", "n2")))

	idx, err := s.BuildIndex(ctx)
	require.NoError(t, err)

	assert.Len(t, idx.Nodes, 2)
	assert.Len(t, idx.OutEdges["n1"], 1)
	assert.Len(t, idx.InEdges["n2"], 1)
	assert.Empty(t, idx.OutEdges["n2"])
}
