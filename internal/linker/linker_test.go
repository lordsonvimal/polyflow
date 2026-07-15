package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func TestLinkTemplComponents(t *testing.T) {
	// A templ component and its generated Go twin in the sibling _templ.go file.
	templComp := graph.Node{
		ID:       "app:views/puzzles.templ:component:PuzzleRows:394",
		Type:     graph.NodeTypeComponent,
		Label:    "PuzzleRows",
		Service:  "app",
		File:     "views/puzzles.templ",
		Language: "templ",
	}
	genFunc := graph.Node{
		ID:       "app:views/puzzles_templ.go:function:PuzzleRows:845",
		Type:     graph.NodeTypeFunction,
		Label:    "PuzzleRows",
		Service:  "app",
		File:     "views/puzzles_templ.go",
		Language: "go",
	}
	// A same-named function in a different package must NOT match: keying on the
	// derived generated-file path, not the bare label, prevents the collision.
	otherPkgFunc := graph.Node{
		ID:       "app:other/helpers.go:function:PuzzleRows:12",
		Type:     graph.NodeTypeFunction,
		Label:    "PuzzleRows",
		Service:  "app",
		File:     "other/helpers.go",
		Language: "go",
	}
	// A hand-written .go component call site with no generated twin: no edge.
	orphanComp := graph.Node{
		ID:       "app:views/orphan.templ:component:Orphan:3",
		Type:     graph.NodeTypeComponent,
		Label:    "Orphan",
		Service:  "app",
		File:     "views/orphan.templ",
		Language: "templ",
	}

	edges := LinkTemplComponents([]graph.Node{templComp, genFunc, otherPkgFunc, orphanComp})

	require.Len(t, edges, 1, "exactly one twin bridge expected")
	e := edges[0]
	assert.Equal(t, genFunc.ID, e.From, "bridge runs from generated Go func")
	assert.Equal(t, templComp.ID, e.To, "into the templ component")
	assert.Equal(t, graph.EdgeTypeComponentImpl, e.Type)
	assert.Equal(t, graph.ConfidenceStatic, e.Confidence)
	assert.Equal(t, "templ_generated", e.Meta["via"])
}

func TestLinkTemplComponents_NoTwin(t *testing.T) {
	// A generated function whose label differs from any component: no bridge.
	nodes := []graph.Node{
		{ID: "app:views/x.templ:component:Foo:1", Type: graph.NodeTypeComponent,
			Label: "Foo", Service: "app", File: "views/x.templ", Language: "templ"},
		{ID: "app:views/x_templ.go:function:Bar:2", Type: graph.NodeTypeFunction,
			Label: "Bar", Service: "app", File: "views/x_templ.go", Language: "go"},
	}
	edges := LinkTemplComponents(nodes)
	assert.Empty(t, edges, "label mismatch must not bridge")
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
	_, removeIDs, _, _ := NewJSLinker().LinkJS(nodes, nil, map[string][]string{})
	assert.False(t, removeIDs["ui:page.templ:component:GamePage:3"],
		"templ component declarations must survive JS proxy pruning")
	assert.True(t, removeIDs["web:App.jsx:component:MissingLib:9"],
		"JSX usage proxies without declarations are still pruned")
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


func TestLinkSSEClients(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:notif.tsx:http_client:eventsource_connect:23", Type: graph.NodeTypeHTTPClient,
			Service: "web", File: "notif.tsx", Meta: map[string]string{"pattern": "eventsource_connect"}},
		{ID: "web:notif.tsx:subscriber:ws_onmessage_assign:24", Type: graph.NodeTypeSubscriber,
			Service: "web", File: "notif.tsx", Meta: map[string]string{"pattern": "ws_onmessage_assign"}},
		{ID: "web:other.tsx:subscriber:ws_onmessage_assign:5", Type: graph.NodeTypeSubscriber,
			Service: "web", File: "other.tsx", Meta: map[string]string{"pattern": "ws_onmessage_assign"}},
	}
	edges := LinkSSEClients(nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, "web:notif.tsx:http_client:eventsource_connect:23", edges[0].From)
	assert.Equal(t, "web:notif.tsx:subscriber:ws_onmessage_assign:24", edges[0].To)
}
