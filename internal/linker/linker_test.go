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

// TestLinkBrokerHints_CrossLanguage proves the confirmed real chain: a Rails
// service publishing via bunny (exchange held in a variable — unresolvable
// statically) reaching a Go amqp091 consumer, connected by a workspace hint.
func TestLinkBrokerHints_CrossLanguage(t *testing.T) {
	nodes := []graph.Node{
		{ID: "nextgen:pub", Type: graph.NodeTypePublisher, Service: "nextgen",
			Language: "ruby", Meta: map[string]string{"pattern": "bunny_publish"}},
		{ID: "dsw-agent:sub", Type: graph.NodeTypeSubscriber, Service: "dsw-agent",
			Language: "go", Meta: map[string]string{"queue": "build-queue", "pattern": "amqp_consume"}},
		{ID: "other:fn", Type: graph.NodeTypeFunction, Service: "other"},
	}
	links := []workspace.Link{
		{From: "nextgen", To: "dsw-agent", Via: "rabbitmq", Exchange: "dsw.builds"},
	}

	chanNodes, edges := LinkBrokerHints(links, nodes)
	require.Len(t, chanNodes, 1)
	assert.Equal(t, graph.NodeTypeChannel, chanNodes[0].Type)
	assert.Equal(t, "dsw.builds", chanNodes[0].Meta["exchange"])

	require.Len(t, edges, 2)
	assert.Equal(t, graph.EdgeTypePublishes, edges[0].Type)
	assert.Equal(t, "nextgen:pub", edges[0].From)
	assert.Equal(t, chanNodes[0].ID, edges[0].To)
	assert.Equal(t, graph.EdgeTypeSubscribes, edges[1].Type)
	assert.Equal(t, chanNodes[0].ID, edges[1].From)
	assert.Equal(t, "dsw-agent:sub", edges[1].To)
	for _, e := range edges {
		assert.Equal(t, graph.ConfidenceStatic, e.Confidence, "user-declared hints are static")
	}
}

func TestLinkBrokerHints_NoRabbitLinks(t *testing.T) {
	nodes := []graph.Node{{ID: "a", Type: graph.NodeTypePublisher, Service: "svc"}}
	n, e := LinkBrokerHints([]workspace.Link{{From: "a", To: "b", BaseURL: "/api"}}, nodes)
	assert.Empty(t, n)
	assert.Empty(t, e)
}

func TestLinkWebSocketMessages_TypedDispatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "client:send", Type: graph.NodeTypePublisher, Service: "tether-client",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": "'battery'"}},
		{ID: "server:case", Type: graph.NodeTypeSubscriber, Service: "tether-server",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'battery'"}},
		{ID: "server:other", Type: graph.NodeTypeSubscriber, Service: "tether-server",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'location'"}},
	}
	edges := LinkWebSocketMessages(nodes)
	require.Len(t, edges, 1, "only the matching message type links")
	assert.Equal(t, "client:send", edges[0].From)
	assert.Equal(t, "server:case", edges[0].To)
	assert.Equal(t, graph.EdgeTypeWSSend, edges[0].Type)
	assert.Equal(t, "battery", edges[0].Meta["message_type"])
}

func TestLinkHubFanout_BroadcastReachesSubscribers(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:hub.go:method:Broadcast:19", Type: graph.NodeTypeMethod, Service: "svc",
			Meta: map[string]string{"pattern": "hub_method_decl", "receiver": "Hub"}},
		{ID: "svc:handlers.go:publisher:hub_broadcast_call:25", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "hub_broadcast_call"}},
		{ID: "svc:stream.go:subscriber:hub_subscribe_call:30", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
		// Subscribe call in a different service must not link.
		{ID: "other:stream.go:subscriber:hub_subscribe_call:8", Type: graph.NodeTypeSubscriber, Service: "other",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	edges := LinkHubFanout(nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, "svc:handlers.go:publisher:hub_broadcast_call:25", edges[0].From)
	assert.Equal(t, "svc:stream.go:subscriber:hub_subscribe_call:30", edges[0].To)
	assert.Equal(t, graph.EdgeTypeHubBroadcast, edges[0].Type)
	assert.Equal(t, graph.ConfidenceInferred, edges[0].Confidence)
}

func TestLinkHubFanout_MultipleHubTypesPartial(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:a.go:method:Broadcast:1", Type: graph.NodeTypeMethod, Service: "svc",
			Meta: map[string]string{"pattern": "hub_method_decl", "receiver": "GameHub"}},
		{ID: "svc:b.go:method:Broadcast:1", Type: graph.NodeTypeMethod, Service: "svc",
			Meta: map[string]string{"pattern": "hub_method_decl", "receiver": "ChatHub"}},
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "hub_broadcast_call"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	edges := LinkHubFanout(nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.ConfidencePartial, edges[0].Confidence,
		"two hub types in one service: cannot tell which hub the call goes through")
}

// Regression: LinkJS must not delete templ component declarations. It prunes
// JSX component *usage proxies* that lack a matching function declaration,
// but templ components are declarations from the templ parser — removing
// them severed every datastar action/bind chain at the root.
func TestLinkJS_KeepsTemplComponents(t *testing.T) {
	nodes := []graph.Node{
		{ID: "ui:page.templ:component:GamePage:3", Type: graph.NodeTypeComponent,
			Label: "GamePage", Service: "ui", Language: "templ"},
		{ID: "web:App.jsx:component:MissingLib:9", Type: graph.NodeTypeComponent,
			Label: "MissingLib", Service: "web", Language: "javascript"},
	}
	_, removeIDs := NewJSLinker().LinkJS(nodes, nil, map[string][]string{})
	assert.False(t, removeIDs["ui:page.templ:component:GamePage:3"],
		"templ component declarations must survive JS proxy pruning")
	assert.True(t, removeIDs["web:App.jsx:component:MissingLib:9"],
		"JSX usage proxies without declarations are still pruned")
}

func TestLinkJobQueues_EnqueueToPerform(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:jobs.rb:publisher:aj_perform_later:9", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_later", "job_class": "ReportJob"}},
		{ID: "app:report_job.rb:subscriber:aj_perform_method:1", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "ReportJob"}},
		// Different job class must not link.
		{ID: "app:other_job.rb:subscriber:aj_perform_method:1", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "OtherJob"}},
	}
	edges := LinkJobQueues(nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeJobEnqueue, edges[0].Type)
	assert.Equal(t, "app:jobs.rb:publisher:aj_perform_later:9", edges[0].From)
	assert.Equal(t, "app:report_job.rb:subscriber:aj_perform_method:1", edges[0].To)
	assert.Equal(t, "ReportJob", edges[0].Meta["job_class"])
}

func TestLinkPusherChannels_CrossLanguage(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": "'orders'", "event": "'order:updated'"}},
		{ID: "web:sub", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "'orders'"}},
		// Variable-held channel must not link.
		{ID: "rails:pub2", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": "channel_name"}},
		// Different channel must not link.
		{ID: "web:sub2", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "'users'"}},
	}
	edges := LinkPusherChannels(nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypePusherTrigger, edges[0].Type)
	assert.Equal(t, "rails:pub", edges[0].From)
	assert.Equal(t, "web:sub", edges[0].To)
	assert.Equal(t, "orders", edges[0].Meta["channel"])
	assert.Equal(t, "order:updated", edges[0].Meta["event"])
}

func TestLinkBrokerHints_SkipsNonBrokerPublishers(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:ws", Type: graph.NodeTypePublisher, Service: "a",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": "'x'"}},
		{ID: "b:hub", Type: graph.NodeTypeSubscriber, Service: "b",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	links := []workspace.Link{{From: "a", To: "b", Via: "rabbitmq", Exchange: "ex"}}
	n, e := LinkBrokerHints(links, nodes)
	assert.Empty(t, n)
	assert.Empty(t, e)
}
