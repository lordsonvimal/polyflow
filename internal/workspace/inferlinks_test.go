package workspace_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func newMemStore(t *testing.T) *graph.SQLiteStore {
	t.Helper()
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInferLinks_HTTPEnvHint(t *testing.T) {
	ctx := context.Background()
	s := newMemStore(t)

	require.NoError(t, s.UpsertUnresolvedRefs(ctx, []graph.UnresolvedRef{
		{Service: "svc-a", File: "a.go", Line: 10, Name: `os.Getenv("ORDER_SERVICE_URL")`, Kind: "dynamic_url"},
	}))

	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{
			{Name: "svc-a"},
			{Name: "order-service"},
			{Name: "unrelated"},
		},
	}

	links, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "svc-a", links[0].From)
	assert.Equal(t, "order-service", links[0].To)
	assert.Equal(t, "ORDER_SERVICE_URL", links[0].Hint)
}

// TestInferLinks_HTTPEnvHint_FanOut: bug-class #1 — a hint matching two
// services proposes both, never first-match.
func TestInferLinks_HTTPEnvHint_FanOut(t *testing.T) {
	ctx := context.Background()
	s := newMemStore(t)

	require.NoError(t, s.UpsertUnresolvedRefs(ctx, []graph.UnresolvedRef{
		{Service: "svc-a", File: "a.go", Line: 10, Name: "ORDER_SERVICE_URL", Kind: "dynamic_url"},
	}))

	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{
			{Name: "svc-a"},
			{Name: "order-service"},
			{Name: "order-legacy"},
		},
	}

	links, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	require.Len(t, links, 2)
	assert.Equal(t, "order-legacy", links[0].To)
	assert.Equal(t, "order-service", links[1].To)
}

func TestInferLinks_BrokerExchangeOverlap(t *testing.T) {
	ctx := context.Background()
	s := newMemStore(t)

	nodes := []graph.Node{
		{ID: "chan:a1", Type: graph.NodeTypeChannel, Service: "svc-a", Meta: map[string]string{"exchange": "orders.events"}},
		{ID: "chan:b1", Type: graph.NodeTypeChannel, Service: "svc-b", Meta: map[string]string{"exchange": "orders.events"}},
		{ID: "chan:c1", Type: graph.NodeTypeChannel, Service: "svc-c", Meta: map[string]string{"exchange": "unrelated.topic"}},
	}
	for i := range nodes {
		require.NoError(t, s.UpsertNode(ctx, &nodes[i]))
	}

	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "svc-a"}, {Name: "svc-b"}, {Name: "svc-c"}},
	}

	links, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	require.Len(t, links, 2)
	assert.Equal(t, workspace.Link{From: "svc-a", To: "svc-b", Via: "rabbitmq", Exchange: "orders.events"}, links[0])
	assert.Equal(t, workspace.Link{From: "svc-b", To: "svc-a", Via: "rabbitmq", Exchange: "orders.events"}, links[1])
}

// TestInferLinks_Negative_NoSharedExchange: two same-named-shape channels in
// unrelated services must not link when their exchange values differ.
func TestInferLinks_Negative_NoSharedExchange(t *testing.T) {
	ctx := context.Background()
	s := newMemStore(t)

	nodes := []graph.Node{
		{ID: "chan:a1", Type: graph.NodeTypeChannel, Service: "svc-a", Meta: map[string]string{"exchange": "svc-a.internal"}},
		{ID: "chan:b1", Type: graph.NodeTypeChannel, Service: "svc-b", Meta: map[string]string{"exchange": "svc-b.internal"}},
	}
	for i := range nodes {
		require.NoError(t, s.UpsertNode(ctx, &nodes[i]))
	}

	cfg := &workspace.WorkspaceConfig{Services: []workspace.Service{{Name: "svc-a"}, {Name: "svc-b"}}}

	links, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	assert.Empty(t, links)
}

// TestInferLinks_Determinism: two runs over the same store produce
// byte-identical proposal order (bug-class #2).
func TestInferLinks_Determinism(t *testing.T) {
	ctx := context.Background()
	s := newMemStore(t)

	require.NoError(t, s.UpsertUnresolvedRefs(ctx, []graph.UnresolvedRef{
		{Service: "svc-a", File: "a.go", Line: 10, Name: "ORDER_SERVICE_URL", Kind: "dynamic_url"},
		{Service: "svc-c", File: "c.go", Line: 20, Name: "ORDER_SERVICE_URL", Kind: "dynamic_url"},
	}))
	nodes := []graph.Node{
		{ID: "chan:a1", Type: graph.NodeTypeChannel, Service: "svc-a", Meta: map[string]string{"exchange": "orders.events"}},
		{ID: "chan:b1", Type: graph.NodeTypeChannel, Service: "svc-b", Meta: map[string]string{"exchange": "orders.events"}},
		{ID: "chan:z1", Type: graph.NodeTypeChannel, Service: "svc-z", Meta: map[string]string{"exchange": "orders.events"}},
	}
	for i := range nodes {
		require.NoError(t, s.UpsertNode(ctx, &nodes[i]))
	}

	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "svc-a"}, {Name: "svc-b"}, {Name: "svc-c"}, {Name: "svc-z"}, {Name: "order-service"}},
	}

	first, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	second, err := workspace.InferLinks(ctx, s, cfg)
	require.NoError(t, err)
	assert.Equal(t, first, second)
}
