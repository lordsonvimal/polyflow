package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"/users/:id", "/users/*"},
		{"/users/:id/posts/:postId", "/users/*/posts/*"},
		{"/users/{id}", "/users/*"},
		{"/users/[0-9]+", "/users/*"},
		{"/api/v1/users/", "/api/v1/users"},
		{"/users/123", "/users/123"},
		{"/", "/"},
		{"", "/"},
	}
	for _, c := range cases {
		got := normalizePath(c.in)
		if got != c.out {
			t.Errorf("normalizePath(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func newNode(id, svc string, typ graph.NodeType, method, path string) graph.Node {
	return graph.Node{
		ID:      id,
		Service: svc,
		Type:    typ,
		Meta:    map[string]string{"method": method, "path": path},
	}
}

func TestLink(t *testing.T) {
	cfg := &workspace.WorkspaceConfig{}
	l := New(cfg)

	cases := []struct {
		name       string
		nodes      []graph.Node
		wantEdges  int
		wantConf   string // confidence on first edge if wantEdges > 0
		wantTarget string // To field on first edge if wantEdges > 0
	}{
		{
			name: "exact match",
			nodes: []graph.Node{
				newNode("client1", "svc-a", graph.NodeTypeHTTPClient, "GET", "/users"),
				newNode("handler1", "svc-b", graph.NodeTypeHTTPHandler, "GET", "/users"),
			},
			wantEdges: 1,
			wantConf:  "static",
			wantTarget: "handler1",
		},
		{
			name: "param normalization",
			nodes: []graph.Node{
				newNode("client2", "svc-a", graph.NodeTypeHTTPClient, "GET", "/users/123"),
				newNode("handler2", "svc-b", graph.NodeTypeHTTPHandler, "GET", "/users/:id"),
			},
			wantEdges: 1,
			wantConf:  "inferred",
			wantTarget: "handler2",
		},
		{
			name: "method mismatch",
			nodes: []graph.Node{
				newNode("client3", "svc-a", graph.NodeTypeHTTPClient, "POST", "/users"),
				newNode("handler3", "svc-b", graph.NodeTypeHTTPHandler, "GET", "/users"),
			},
			wantEdges: 1,
			wantConf:  "unknown", // no match → unknown edge
		},
		{
			name: "no match — unresolvable",
			nodes: []graph.Node{
				newNode("client4", "svc-a", graph.NodeTypeHTTPClient, "GET", "/dynamic/xyz"),
			},
			wantEdges: 1,
			wantConf:  "unknown",
		},
		{
			name: "same-service — no cross edge",
			nodes: []graph.Node{
				newNode("client5", "svc-a", graph.NodeTypeHTTPClient, "GET", "/users"),
				newNode("handler5", "svc-a", graph.NodeTypeHTTPHandler, "GET", "/users"),
			},
			wantEdges: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edges, err := l.Link(c.nodes, nil)
			if err != nil {
				t.Fatalf("Link() error: %v", err)
			}
			if len(edges) != c.wantEdges {
				t.Fatalf("got %d edges, want %d", len(edges), c.wantEdges)
			}
			if c.wantEdges > 0 {
				e := edges[0]
				if got := e.Meta["confidence"]; got != c.wantConf {
					t.Errorf("confidence = %q, want %q", got, c.wantConf)
				}
				if c.wantTarget != "" && e.To != c.wantTarget {
					t.Errorf("edge.To = %q, want %q", e.To, c.wantTarget)
				}
			}
		})
	}
}

func TestLinkBaseURLHint(t *testing.T) {
	cfg := &workspace.WorkspaceConfig{
		Links: []workspace.Link{
			{From: "frontend", To: "backend", BaseURL: "/api"},
		},
	}
	l := New(cfg)

	nodes := []graph.Node{
		{
			ID:      "client",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "/users", "target_service": "backend"},
		},
		newNode("handler", "backend", graph.NodeTypeHTTPHandler, "GET", "/users"),
	}

	// ApplyHints would have already stripped /api. Here path is already stripped.
	edges, err := l.Link(nodes, nil)
	if err != nil {
		t.Fatalf("Link() error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if got := edges[0].Meta["confidence"]; got != "static" {
		t.Errorf("confidence = %q, want static", got)
	}
}

func TestLinkNilConfig(t *testing.T) {
	l := New(nil)
	_, err := l.Link(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestLinkBrokerChannels_CrossService(t *testing.T) {
	// Two channel nodes with the same key from different services:
	// one has a publisher pointing at it, the other is the subscribe-side.
	pubChannel := graph.Node{
		ID:      "svc-a:channel:user.events/user.created",
		Type:    graph.NodeTypeChannel,
		Service: "svc-a",
		Meta:    map[string]string{"exchange": "user.events", "routing_key": "user.created"},
	}
	subChannel := graph.Node{
		ID:      "svc-b:channel:user.events/user.created",
		Type:    graph.NodeTypeChannel,
		Service: "svc-b",
		Meta:    map[string]string{"exchange": "user.events", "routing_key": "user.created"},
	}
	publisher := graph.Node{
		ID:      "svc-a:svc.go:publisher:publishUserCreated:5",
		Type:    graph.NodeTypePublisher,
		Service: "svc-a",
		Meta:    map[string]string{"exchange": "user.events", "routing_key": "user.created"},
	}
	// "publishes" edge links publisher -> pubChannel (in-memory only for this test)
	// LinkBrokerChannels builds its own index from node meta.

	nodes := []graph.Node{pubChannel, subChannel, publisher}
	edges := LinkBrokerChannels(nodes)

	if len(edges) != 1 {
		t.Fatalf("expected 1 cross-service broker edge, got %d", len(edges))
	}
	e := edges[0]
	if e.From != pubChannel.ID {
		t.Errorf("edge.From = %q, want %q", e.From, pubChannel.ID)
	}
	if e.To != subChannel.ID {
		t.Errorf("edge.To = %q, want %q", e.To, subChannel.ID)
	}
	if e.Type != graph.EdgeTypePublishes {
		t.Errorf("edge.Type = %q, want publishes", e.Type)
	}
	if e.Meta["via"] != "amqp_channel" {
		t.Errorf("edge.Meta[via] = %q, want amqp_channel", e.Meta["via"])
	}
}

func TestLinkBrokerChannels_SameService(t *testing.T) {
	// Same-service channel nodes should not produce cross edges.
	ch1 := graph.Node{
		ID:      "svc-a:channel:orders/placed",
		Type:    graph.NodeTypeChannel,
		Service: "svc-a",
		Meta:    map[string]string{"exchange": "orders", "routing_key": "placed"},
	}
	pub := graph.Node{
		ID:      "svc-a:order.go:publisher:placeOrder:10",
		Type:    graph.NodeTypePublisher,
		Service: "svc-a",
		Meta:    map[string]string{"exchange": "orders", "routing_key": "placed"},
	}
	edges := LinkBrokerChannels([]graph.Node{ch1, pub})
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for same-service channels, got %d", len(edges))
	}
}

func TestLinkBrokerChannels_NoChannels(t *testing.T) {
	edges := LinkBrokerChannels([]graph.Node{})
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for empty node list, got %d", len(edges))
	}
}

func TestLinkDatastores(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:datastore:sqlite", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "store", "engine": "sqlite"}},
		{ID: "svc:q1", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "query"}},
		{ID: "svc:p1", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist"}},
		{ID: "other:q", Type: graph.NodeTypeDatastore, Service: "other",
			Meta: map[string]string{"kind": "call", "op": "query"}}, // no store in service
	}
	edges := LinkDatastores(nodes)
	require.Len(t, edges, 2)

	byFrom := map[string]graph.Edge{}
	for _, e := range edges {
		byFrom[e.From] = e
	}
	assert.Equal(t, graph.EdgeTypeQueries, byFrom["svc:q1"].Type)
	assert.Equal(t, graph.EdgeTypePersists, byFrom["svc:p1"].Type)
	assert.Equal(t, "svc:datastore:sqlite", byFrom["svc:q1"].To)
	assert.Equal(t, graph.ConfidenceInferred, byFrom["svc:q1"].Confidence)
}

func TestLinkDatastores_MultiEnginePartialConfidence(t *testing.T) {
	nodes := []graph.Node{
		{ID: "m:datastore:postgres", Type: graph.NodeTypeDatastore, Service: "m",
			Meta: map[string]string{"kind": "store"}},
		{ID: "m:datastore:sqlite", Type: graph.NodeTypeDatastore, Service: "m",
			Meta: map[string]string{"kind": "store"}},
		{ID: "m:q", Type: graph.NodeTypeDatastore, Service: "m",
			Meta: map[string]string{"kind": "call", "op": "query"}},
	}
	edges := LinkDatastores(nodes)
	require.Len(t, edges, 2, "ambiguous engine: edge to each store")
	for _, e := range edges {
		assert.Equal(t, graph.ConfidencePartial, e.Confidence)
	}
}
