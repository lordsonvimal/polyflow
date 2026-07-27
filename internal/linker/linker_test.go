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


func TestLinkRouteComponents(t *testing.T) {
	route := graph.Node{
		ID:      "app:src/App.tsx:route:%2Fsettings:10",
		Type:    graph.NodeTypeRoute,
		Label:   "/settings",
		Service: "app",
		File:    "src/App.tsx",
		Line:    10,
		Meta:    map[string]string{"component": "Settings"},
	}
	settingsFn := graph.Node{
		ID:      "app:src/Settings.tsx:function:Settings:1",
		Type:    graph.NodeTypeFunction,
		Label:   "Settings",
		Service: "app",
		File:    "src/Settings.tsx",
	}
	// Same label in a different service must not match.
	otherServiceFn := graph.Node{
		ID:      "other:src/Settings.tsx:function:Settings:1",
		Type:    graph.NodeTypeFunction,
		Label:   "Settings",
		Service: "other",
		File:    "src/Settings.tsx",
	}

	edges, unresolved := LinkRouteComponents([]graph.Node{route, settingsFn, otherServiceFn})

	require.Len(t, edges, 1, "one renders edge from the route to its component")
	require.Empty(t, unresolved)
	e := edges[0]
	assert.Equal(t, route.ID, e.From)
	assert.Equal(t, settingsFn.ID, e.To)
	assert.Equal(t, graph.EdgeTypeRenders, e.Type)
	assert.Equal(t, graph.ConfidenceInferred, e.Confidence)
}

func TestLinkRouteComponents_MissLedgered(t *testing.T) {
	route := graph.Node{
		ID:      "app:src/App.tsx:route:%2Fmissing:20",
		Type:    graph.NodeTypeRoute,
		Label:   "/missing",
		Service: "app",
		File:    "src/App.tsx",
		Line:    20,
		Meta:    map[string]string{"component": "Ghost"},
	}

	edges, unresolved := LinkRouteComponents([]graph.Node{route})

	assert.Empty(t, edges, "no declaration to resolve to — never guessed")
	require.Len(t, unresolved, 1, "the miss is ledgered, not silently dropped")
	u := unresolved[0]
	assert.Equal(t, "app", u.Service)
	assert.Equal(t, "src/App.tsx", u.File)
	assert.Equal(t, 20, u.Line)
	assert.Equal(t, "Ghost", u.Name)
	assert.Equal(t, "component_ref", u.Kind)
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

// TestLinkTables verifies Y.3c: a datastore call node's SQL is parsed to its
// table, one table node is minted per (service, name), and the query/persist
// terminates at that real entity (callNode → table). Statements with no
// resolvable table (PRAGMA) mint nothing (#12 — never fabricate).
func TestLinkTables(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:sel", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "query",
				"sql": "`SELECT value FROM meta WHERE key = ?`"}},
		{ID: "svc:ins", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist",
				"sql": "`INSERT OR REPLACE INTO meta(key,value) VALUES('x',?)`"}},
		{ID: "svc:del", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist",
				"sql": "`DELETE FROM nodes WHERE id=?`"}},
		{ID: "svc:pragma", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist",
				"sql": "`PRAGMA busy_timeout=5000;`"}},
		{ID: "svc:store", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "store"}}, // not a call — ignored
	}
	tableNodes, edges := LinkTables(nodes)

	// Two distinct tables (meta touched twice → deduped), no table for PRAGMA.
	require.Len(t, tableNodes, 2)
	byName := map[string]graph.Node{}
	for _, n := range tableNodes {
		assert.Equal(t, graph.NodeTypeTable, n.Type)
		assert.Equal(t, "svc", n.Service)
		byName[n.Label] = n
	}
	assert.Contains(t, byName, "meta")
	assert.Contains(t, byName, "nodes")
	assert.Equal(t, "svc:table:meta", byName["meta"].ID)

	// One edge per call with a resolvable table; PRAGMA yields none.
	require.Len(t, edges, 3)
	byFrom := map[string]graph.Edge{}
	for _, e := range edges {
		byFrom[e.From] = e
		assert.Equal(t, graph.ConfidenceStatic, e.Confidence)
	}
	assert.Equal(t, "svc:table:meta", byFrom["svc:sel"].To)
	assert.Equal(t, graph.EdgeTypeQueries, byFrom["svc:sel"].Type)
	assert.Equal(t, "svc:table:meta", byFrom["svc:ins"].To)
	assert.Equal(t, graph.EdgeTypePersists, byFrom["svc:ins"].Type)
	assert.Equal(t, "svc:table:nodes", byFrom["svc:del"].To)
	_, pragmaHasEdge := byFrom["svc:pragma"]
	assert.False(t, pragmaHasEdge, "PRAGMA must not fabricate a table edge")
}

// TestParseSQLTable covers the table-name extraction edge cases directly.
func TestParseSQLTable(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"`SELECT a, b FROM users WHERE id = ?`", "users"},
		{"`INSERT INTO edges (id) VALUES (?)`", "edges"},
		{"`INSERT OR REPLACE INTO meta(key,value) VALUES(?,?)`", "meta"},
		{"`UPDATE nodes SET x=1`", "nodes"},
		{"`DELETE FROM edges WHERE \"from\"=?`", "edges"},
		{"`SELECT id, \"from\", \"to\" FROM edges`", "edges"},
		// Outer FROM opens a subquery → resolve to the inner real table.
		{"`SELECT * FROM ( SELECT id FROM entities_fts ) f`", "entities_fts"},
		{"`PRAGMA busy_timeout=5000;`", ""},
		{"`PRAGMA synchronous=OFF; PRAGMA journal_mode=MEMORY;`", ""},
		{"``", ""},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, parseSQLTable(c.sql), "sql=%s", c.sql)
	}
}

// TestLinkBrokerHints_CrossLanguage proves the confirmed real chain: a Rails
// service publishing via bunny (exchange held in a variable — unresolvable
// statically) reaching a Go amqp091 consumer, connected by a workspace hint.
func TestLinkBrokerHints_CrossLanguage(t *testing.T) {
	nodes := []graph.Node{
		{ID: "main-svc:pub", Type: graph.NodeTypePublisher, Service: "main-svc",
			Language: "ruby", Meta: map[string]string{"pattern": "bunny_publish"}},
		{ID: "svc-c-agent:sub", Type: graph.NodeTypeSubscriber, Service: "svc-c-agent",
			Language: "go", Meta: map[string]string{"queue": "build-queue", "pattern": "amqp_consume"}},
		{ID: "other:fn", Type: graph.NodeTypeFunction, Service: "other"},
	}
	links := []workspace.Link{
		{From: "main-svc", To: "svc-c-agent", Via: "rabbitmq", Exchange: "maple.builds"},
	}

	chanNodes, edges := LinkBrokerHints(links, nodes)
	require.Len(t, chanNodes, 1)
	assert.Equal(t, graph.NodeTypeChannel, chanNodes[0].Type)
	assert.Equal(t, "maple.builds", chanNodes[0].Meta["exchange"])

	require.Len(t, edges, 2)
	assert.Equal(t, graph.EdgeTypePublishes, edges[0].Type)
	assert.Equal(t, "main-svc:pub", edges[0].From)
	assert.Equal(t, chanNodes[0].ID, edges[0].To)
	assert.Equal(t, graph.EdgeTypeSubscribes, edges[1].Type)
	assert.Equal(t, chanNodes[0].ID, edges[1].From)
	assert.Equal(t, "svc-c-agent:sub", edges[1].To)
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
