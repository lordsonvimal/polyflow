package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func TestLinkTemplComponents(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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


// routeHandlerNode builds an http_handler route node referencing handler as
// written at the call site ("baseImageHandler.SaveConfig").
func routeHandlerNode(id, label, handler string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, Label: label, Service: "app",
		File: "internal/routes/views.go",
		Meta: map[string]string{"handler": handler},
	}
}

// handlerMethod builds a method node carrying its receiver struct.
func handlerMethod(file, recv, label string) graph.Node {
	return graph.Node{
		ID: "app:" + file + ":method:" + label, Type: graph.NodeTypeMethod, Label: label,
		Service: "app", File: file,
		Meta: map[string]string{"receiver": recv},
	}
}

// Three handler structs defining the same method name is the ordinary Rails-ish
// Go shape. Matching on the bare label collapses every route onto whichever
// method was indexed first, which both mislinks the route and strands the real
// handler with no caller (it then reads as dead code). The receiver qualifier
// recorded on each side must pin them apart.
func TestLinkRouteHandlers_DisambiguatesByReceiver(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "POST /app-configs/save", "appConfigHandler.SaveConfig"),
		routeHandlerNode("app:routes:http_handler:2", "POST /base-images/save", "baseImageHandler.SaveConfig"),
		routeHandlerNode("app:routes:http_handler:3", "POST /exec-configs/save", "execConfigHandler.SaveConfig"),
		handlerMethod("internal/views/app_config_handler.go", "AppConfigHandler", "SaveConfig"),
		handlerMethod("internal/views/base_image_handler.go", "BaseImageHandler", "SaveConfig"),
		handlerMethod("internal/views/exec_config_handler.go", "ExecConfigHandler", "SaveConfig"),
	}

	got := map[string]string{}
	for _, e := range LinkRouteHandlers(nodes) {
		got[e.From] = e.To
	}

	assert.Equal(t, "app:internal/views/app_config_handler.go:method:SaveConfig", got["app:routes:http_handler:1"])
	assert.Equal(t, "app:internal/views/base_image_handler.go:method:SaveConfig", got["app:routes:http_handler:2"])
	assert.Equal(t, "app:internal/views/exec_config_handler.go:method:SaveConfig", got["app:routes:http_handler:3"])
}

// Only the innermost segment names the value being called; a field path or a
// package qualifier must not defeat the receiver match.
func TestLinkRouteHandlers_ChainedQualifier(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "GET /x", "s.execConfigHandler.Show"),
		handlerMethod("internal/views/app_config_handler.go", "AppConfigHandler", "Show"),
		handlerMethod("internal/views/exec_config_handler.go", "ExecConfigHandler", "Show"),
	}

	edges := LinkRouteHandlers(nodes)

	require.Len(t, edges, 1)
	assert.Equal(t, "app:internal/views/exec_config_handler.go:method:Show", edges[0].To)
}

// When the qualifier names nothing we can resolve — a local variable or a
// package — the label-only lookup still has to produce an edge, since a missing
// route→handler hop breaks every downstream flow.
func TestLinkRouteHandlers_UnresolvableQualifierFallsBack(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "GET /y", "h.List"),
		handlerMethod("internal/views/base_image_handler.go", "BaseImageHandler", "List"),
	}

	edges := LinkRouteHandlers(nodes)

	require.Len(t, edges, 1, "falls back to the label match rather than dropping the hop")
	assert.Equal(t, "app:internal/views/base_image_handler.go:method:List", edges[0].To)
}

// `appController := controllers.NewUserAppController(...)` names the variable
// for a shortened form of its type, so the exact receiver match misses. A
// unique suffix relationship still identifies the struct — without it the
// label-only fallback picked an unrelated `SessionStore.Create`.
func TestLinkRouteHandlers_AbbreviatedQualifier(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "POST /apps", "appController.Create"),
		handlerMethod("internal/auth/saml/session.go", "SessionStore", "Create"),
		handlerMethod("internal/controllers/user_app_controller.go", "UserAppController", "Create"),
	}

	edges := LinkRouteHandlers(nodes)

	require.Len(t, edges, 1)
	assert.Equal(t, "app:internal/controllers/user_app_controller.go:method:Create", edges[0].To)
}

// Two structs fitting the same abbreviation is not evidence, so the guess is
// declined and the existing fallback decides.
func TestLinkRouteHandlers_AmbiguousAbbreviationDeclined(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "POST /x", "appController.Create"),
		handlerMethod("a.go", "UserAppController", "Create"),
		handlerMethod("b.go", "AdminAppController", "Create"),
	}

	edges := LinkRouteHandlers(nodes)

	require.Len(t, edges, 1)
	assert.Equal(t, "app:a.go:method:Create", edges[0].To, "falls back to the first label match, not an arbitrary abbreviation pick")
}

// A receiver match must not reach across service boundaries.
func TestLinkRouteHandlers_ReceiverMatchIsServiceScoped(t *testing.T) {
	t.Parallel()
	other := handlerMethod("internal/views/exec_config_handler.go", "ExecConfigHandler", "Save")
	other.ID, other.Service = "other:exec:method:Save", "other"
	nodes := []graph.Node{
		routeHandlerNode("app:routes:http_handler:1", "GET /z", "execConfigHandler.Save"),
		other,
	}

	assert.Empty(t, LinkRouteHandlers(nodes), "no handler in this service, so no edge")
}

// PW.1: the gin_route registration ("path": "/notifications") calls serveWS,
// whose body contains the bare ws_upgrade node (no path of its own). The
// pass must copy path+method onto it so contracts/websocket.yaml's
// connect-time rule can key on it.
func TestLinkWSUpgradeRoute_StampsPathFromRegisteringRoute(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "app:routes:http_handler:1", Type: graph.NodeTypeHTTPHandler, Label: "GET /notifications",
			Service: "app", File: "internal/routes/views.go",
			Meta: map[string]string{"handler": "serveWS", "path": "/notifications", "method": "GET", "pattern": "gin_route"}},
		{ID: "app:internal/handlers/ws.go:function:serveWS", Type: graph.NodeTypeFunction, Label: "serveWS",
			Service: "app", File: "internal/handlers/ws.go", Line: 10, EndLine: 20},
		{ID: "app:internal/handlers/ws.go:http_handler:ws_upgrade:12", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "internal/handlers/ws.go", Line: 12,
			Meta: map[string]string{"pattern": "ws_upgrade"}},
	}

	updated := LinkWSUpgradeRoute(nodes)

	require.Len(t, updated, 1)
	assert.Equal(t, "app:internal/handlers/ws.go:http_handler:ws_upgrade:12", updated[0].ID)
	assert.Equal(t, "/notifications", updated[0].Meta["path"])
	assert.Equal(t, "GET", updated[0].Meta["method"])
}

// A ws_upgrade call outside the registered handler's line range (a
// different function entirely) must not receive the route's path — the
// containment check is load-bearing, not decorative.
func TestLinkWSUpgradeRoute_OutsideSpanNotStamped(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "app:routes:http_handler:1", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "internal/routes/views.go",
			Meta: map[string]string{"handler": "serveWS", "path": "/notifications", "method": "GET"}},
		{ID: "app:internal/handlers/ws.go:function:serveWS", Type: graph.NodeTypeFunction, Label: "serveWS",
			Service: "app", File: "internal/handlers/ws.go", Line: 10, EndLine: 20},
		// Unrelated function elsewhere in the same file, with its own ws_upgrade call.
		{ID: "app:internal/handlers/ws.go:function:otherUpgrade", Type: graph.NodeTypeFunction, Label: "otherUpgrade",
			Service: "app", File: "internal/handlers/ws.go", Line: 30, EndLine: 40},
		{ID: "app:internal/handlers/ws.go:http_handler:ws_upgrade:32", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "internal/handlers/ws.go", Line: 32,
			Meta: map[string]string{"pattern": "ws_upgrade"}},
	}

	updated := LinkWSUpgradeRoute(nodes)

	assert.Empty(t, updated, "the ws_upgrade node at line 32 is not registered by any route")
}

// A node that already carries a path (Python's ws_upgrade_fastapi shape)
// must be left untouched, not overwritten.
func TestLinkWSUpgradeRoute_AlreadyHasPathSkipped(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "app:routes:http_handler:1", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "internal/routes/views.go",
			Meta: map[string]string{"handler": "serveWS", "path": "/notifications", "method": "GET"}},
		{ID: "app:internal/handlers/ws.go:function:serveWS", Type: graph.NodeTypeFunction, Label: "serveWS",
			Service: "app", File: "internal/handlers/ws.go", Line: 10, EndLine: 20},
		{ID: "app:internal/handlers/ws.go:http_handler:ws_upgrade_fastapi:12", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "internal/handlers/ws.go", Line: 12,
			Meta: map[string]string{"pattern": "ws_upgrade_fastapi", "path": "/already-set"}},
	}

	assert.Empty(t, LinkWSUpgradeRoute(nodes))
}

func TestLinkRouteComponents(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestLinkDatastores_GormSkipsNonGormEngine: a GORM call site is not fanned
// out onto an engine that only has a raw database/sql driver (a stray
// go-sql-driver/mysql), but a plain sql_* call still reaches every engine.
func TestLinkDatastores_GormSkipsNonGormEngine(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "s:pg", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "store", "engine": "postgres", "orm": "gorm"}},
		{ID: "s:my", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "store", "engine": "mysql", "driver": "go-sql-driver"}},
		{ID: "g:create", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist", "package": "gorm.io/gorm", "pattern": "gorm_persist"}},
		{ID: "r:exec", Type: graph.NodeTypeDatastore, Service: "svc",
			Meta: map[string]string{"kind": "call", "op": "persist", "pattern": "sql_exec"}},
	}
	edges := LinkDatastores(nodes)
	byFrom := map[string][]string{}
	for _, e := range edges {
		byFrom[e.From] = append(byFrom[e.From], e.To)
	}
	assert.Equal(t, []string{"s:pg"}, byFrom["g:create"], "GORM call: gorm engine only")
	assert.ElementsMatch(t, []string{"s:pg", "s:my"}, byFrom["r:exec"], "raw sql: every engine")
}

// TestLinkTables verifies Y.3c: a datastore call node's SQL is parsed to its
// table, one table node is minted per (service, name), and the query/persist
// terminates at that real entity (callNode → table). Statements with no
// resolvable table (PRAGMA) mint nothing (#12 — never fabricate).
func TestLinkTables(t *testing.T) {
	t.Parallel()
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
	tableNodes, edges, unresolved := LinkTables(nodes)
	assert.Empty(t, unresolved)

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

// TestLinkTables_SQ2ReconcilesSchemaDeclaredTable verifies SQ2: when a
// schema-declared graph.NodeTypeTable node (minted by internal/parser/sql.go
// from a real CREATE TABLE) is already present, a datastore call querying the
// same table name rewires its edge onto that node instead of minting a
// duplicate synthetic one — even when the call lives in a different service
// than the schema file (the plan's "shared db/migrations service" case).
func TestLinkTables_SQ2ReconcilesSchemaDeclaredTable(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "schema:db/schema.sql:table:users:3", Type: graph.NodeTypeTable,
			Label: "users", Service: "schema", File: "db/schema.sql", Line: 3,
			Meta: map[string]string{"columns": `[{"name":"id"}]`}},
		{ID: "app:sel", Type: graph.NodeTypeDatastore, Service: "app",
			Meta: map[string]string{"kind": "call", "op": "query",
				"sql": "`SELECT * FROM users WHERE id = ?`"}},
	}
	tableNodes, edges, unresolved := LinkTables(nodes)

	assert.Empty(t, tableNodes, "no duplicate table node minted when a schema declaration already exists")
	assert.Empty(t, unresolved)
	require.Len(t, edges, 1)
	assert.Equal(t, "schema:db/schema.sql:table:users:3", edges[0].To,
		"query edge lands on the real schema-declared node, not a synthetic app:table:users")
	assert.Equal(t, graph.EdgeTypeQueries, edges[0].Type)
}

// TestLinkTables_SQ2SchemaAbsentRegression is SQ2's own regression fixture
// (rule 5): with no schema-declared table node anywhere in the workspace, the
// pre-SQ2 synthetic-mint path must be byte-for-byte unchanged.
func TestLinkTables_SQ2SchemaAbsentRegression(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "app:sel", Type: graph.NodeTypeDatastore, Service: "app",
			Meta: map[string]string{"kind": "call", "op": "query",
				"sql": "`SELECT * FROM users WHERE id = ?`"}},
	}
	tableNodes, edges, unresolved := LinkTables(nodes)

	require.Len(t, tableNodes, 1)
	assert.Equal(t, "app:table:users", tableNodes[0].ID)
	require.Len(t, edges, 1)
	assert.Equal(t, "app:table:users", edges[0].To)
	assert.Empty(t, unresolved)
}

// TestLinkTables_SQ2NameCollisionFansOut verifies the extended rule-1 fan-out:
// two schema-declared tables sharing a name across different services both
// receive the query edge, plus a sql_table_query_collision ledger entry —
// never first-match.
func TestLinkTables_SQ2NameCollisionFansOut(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "svcA:a.sql:table:users:1", Type: graph.NodeTypeTable,
			Label: "users", Service: "svcA", File: "a.sql", Line: 1},
		{ID: "svcB:b.sql:table:users:1", Type: graph.NodeTypeTable,
			Label: "users", Service: "svcB", File: "b.sql", Line: 1},
		{ID: "app:sel", Type: graph.NodeTypeDatastore, Service: "app", File: "app.go", Line: 5,
			Meta: map[string]string{"kind": "call", "op": "query",
				"sql": "`SELECT * FROM users WHERE id = ?`"}},
	}
	tableNodes, edges, unresolved := LinkTables(nodes)

	assert.Empty(t, tableNodes)
	require.Len(t, edges, 2)
	gotTo := map[string]bool{}
	for _, e := range edges {
		gotTo[e.To] = true
	}
	assert.True(t, gotTo["svcA:a.sql:table:users:1"])
	assert.True(t, gotTo["svcB:b.sql:table:users:1"])
	require.Len(t, unresolved, 1)
	assert.Equal(t, "sql_table_query_collision", unresolved[0].Kind)
}

// TestLinkTables_SQ2Determinism runs LinkTables twice against the same input
// (rule 2 — this pass iterates a name->[]nodeID map, the exact non-determinism
// shape rule 2 exists for) and asserts byte-identical edge sets both runs.
func TestLinkTables_SQ2Determinism(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "svcA:a.sql:table:users:1", Type: graph.NodeTypeTable,
			Label: "users", Service: "svcA", File: "a.sql", Line: 1},
		{ID: "svcB:b.sql:table:users:1", Type: graph.NodeTypeTable,
			Label: "users", Service: "svcB", File: "b.sql", Line: 1},
		{ID: "app:sel", Type: graph.NodeTypeDatastore, Service: "app", File: "app.go", Line: 5,
			Meta: map[string]string{"kind": "call", "op": "query",
				"sql": "`SELECT * FROM users WHERE id = ?`"}},
	}
	_, edges1, unresolved1 := LinkTables(nodes)
	_, edges2, unresolved2 := LinkTables(nodes)
	assert.Equal(t, edges1, edges2)
	assert.Equal(t, unresolved1, unresolved2)
}

// TestParseSQLTable covers the table-name extraction edge cases directly.
func TestParseSQLTable(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

	chanNodes, edges, unresolved := LinkBrokerHints(links, nodes)
	assert.Empty(t, unresolved, "a single hinted exchange is unambiguous evidence-free wiring")
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
	t.Parallel()
	nodes := []graph.Node{{ID: "a", Type: graph.NodeTypePublisher, Service: "svc"}}
	n, e, u := LinkBrokerHints([]workspace.Link{{From: "a", To: "b", BaseURL: "/api"}}, nodes)
	assert.Empty(t, n)
	assert.Empty(t, e)
	assert.Empty(t, u, "no rabbitmq link means nothing was even attempted")
}

// J.3 regression for the 25-edge cartesian: an exchange-less `dynamic`
// subscriber facing 5 hinted exchanges was joined to all 5 and stamped
// `static`. It must now produce nothing, and say so in the ledger.
func TestLinkBrokerHints_RequiresExchangeEvidence(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "agent:sub:dynamic", Label: "dynamic", Type: graph.NodeTypeSubscriber,
			Service: "maple-agent", File: "amqp.go", Line: 238,
			Meta: map[string]string{"pattern": "amqp_consume", "key_dynamic": "true"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_jobs"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_logs"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "file_sync"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "container_events"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "shinyproxy_config"},
	}

	chanNodes, edges, unresolved := LinkBrokerHints(links, nodes)
	assert.Empty(t, edges, "no evidence + 5 candidate exchanges is a guess, not a link")
	assert.Empty(t, chanNodes, "no edge means no channel to mint")
	require.Len(t, unresolved, 1, "one ledger entry per node, not per link")
	assert.Equal(t, "amqp_exchange_unresolved", unresolved[0].Kind)
	assert.Equal(t, "dynamic", unresolved[0].Name)
	assert.Equal(t, "maple-agent", unresolved[0].Service)
	assert.Equal(t, 238, unresolved[0].Line)
}

func TestLinkBrokerHints_MatchingExchangeStillLinks(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "agent:sub:jobs", Type: graph.NodeTypeSubscriber, Service: "maple-agent",
			Meta: map[string]string{"pattern": "amqp_consume", "exchange": `"build_jobs"`}},
		{ID: "agent:sub:other", Type: graph.NodeTypeSubscriber, Service: "maple-agent",
			Meta: map[string]string{"pattern": "amqp_consume", "exchange": "unlinked_exchange"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_jobs"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_logs"},
	}

	_, edges, unresolved := LinkBrokerHints(links, nodes)
	require.Len(t, edges, 1)
	assert.Equal(t, "agent:sub:jobs", edges[0].To)
	assert.Equal(t, graph.ConfidenceStatic, edges[0].Confidence)
	assert.Empty(t, edges[0].Meta["fanout"])
	require.Len(t, unresolved, 1, "the node naming an unlinked exchange is a miss, not a link")
	assert.Equal(t, "agent:sub:other", unresolved[0].Name)
}

func TestLinkBrokerHints_MultiCandidateStampsPartial(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "agent:sub:multi", Type: graph.NodeTypeSubscriber, Service: "maple-agent",
			Meta: map[string]string{"pattern": "amqp_consume",
				"exchange_candidates": "build_jobs,build_logs"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_jobs"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_logs"},
	}

	_, edges, unresolved := LinkBrokerHints(links, nodes)
	require.Len(t, edges, 2)
	assert.Empty(t, unresolved)
	for _, e := range edges {
		assert.Equal(t, graph.ConfidencePartial, e.Confidence,
			"two candidate exchanges cannot both be the deployed topology")
		assert.Equal(t, "2", e.Meta["fanout"])
	}
}

// The hint must meet the real channel a resolved binding already produced;
// minting `broker:channel:<exchange>` beside `<svc>:channel:<exchange>/<key>`
// is what created channel→channel publishes edges.
func TestLinkBrokerHints_ReusesExistingChannelID(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "maple-agent:channel:build_logs/", Label: "build_logs/", Type: graph.NodeTypeChannel,
			Service: "maple-agent", Meta: map[string]string{"exchange": "build_logs"}},
		{ID: "maple-agent:sub:logs", Type: graph.NodeTypeSubscriber, Service: "maple-agent",
			Meta: map[string]string{"pattern": "amqp_consume", "exchange": "build_logs"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_logs"},
	}

	chanNodes, edges, _ := LinkBrokerHints(links, nodes)
	assert.Empty(t, chanNodes, "a real channel exists; no synthetic node may be minted")
	require.Len(t, edges, 1)
	assert.Equal(t, "maple-agent:channel:build_logs/", edges[0].From)
}

// A queue name is exchange evidence when a resolved binding table (J.1) says
// which exchange that queue is bound to.
func TestLinkBrokerHints_QueueNameResolvesExchange(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "maple-manager:channel:build_logs/logs.build.*", Type: graph.NodeTypeChannel,
			Service: "maple-manager", Meta: map[string]string{
				"exchange": "build_logs", "routing_key": "logs.build.*",
				"queue_name": "build_logs_queue", "resolved_via": "static_table"}},
		{ID: "agent:sub:q", Type: graph.NodeTypeSubscriber, Service: "maple-agent",
			Meta: map[string]string{"pattern": "amqp_consume", "queue": "build_logs_queue"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_logs"},
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "file_sync"},
	}

	_, edges, unresolved := LinkBrokerHints(links, nodes)
	require.Len(t, edges, 1, "the queue binds one exchange, so only one hint applies")
	assert.Equal(t, "maple-manager:channel:build_logs/logs.build.*", edges[0].From)
	assert.Equal(t, graph.ConfidenceStatic, edges[0].Confidence)
	assert.Empty(t, unresolved)
}

// Publishers keep their pre-J.3 gate: one that resolved its own exchange has a
// real channel already and must not collect a second, hint-borne one.
func TestLinkBrokerHints_ResolvedPublisherSkipped(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		{ID: "mgr:pub:resolved", Type: graph.NodeTypePublisher, Service: "maple-manager",
			Meta: map[string]string{"pattern": "amqp_publish", "exchange": "build_jobs"}},
	}
	links := []workspace.Link{
		{From: "maple-manager", To: "maple-agent", Via: "rabbitmq", Exchange: "build_jobs"},
	}

	_, edges, unresolved := LinkBrokerHints(links, nodes)
	assert.Empty(t, edges)
	assert.Empty(t, unresolved, "a statically resolved publisher is not a miss")
}


// Regression: LinkJS must not delete templ component declarations. It prunes
// JSX component *usage proxies* that lack a matching function declaration,
// but templ components are declarations from the templ parser — removing
// them severed every datastar action/bind chain at the root.
func TestLinkJS_KeepsTemplComponents(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	nodes := []graph.Node{
		{ID: "a:ws", Type: graph.NodeTypePublisher, Service: "a",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": "'x'"}},
		{ID: "b:hub", Type: graph.NodeTypeSubscriber, Service: "b",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	links := []workspace.Link{{From: "a", To: "b", Via: "rabbitmq", Exchange: "ex"}}
	n, e, u := LinkBrokerHints(links, nodes)
	assert.Empty(t, n)
	assert.Empty(t, e)
	assert.Empty(t, u, "non-broker traffic is out of scope, not unresolved")
}


func TestLinkSSEClients(t *testing.T) {
	t.Parallel()
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

// grpcRegisterNode builds a grpc_handler node as the grpc_server_register
// pattern produces it: labeled with the Register<Service>Server function
// name, carrying the impl argument's raw source text.
func grpcRegisterNode(id, impl string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeGRPCHandler, Label: "RegisterTraceServiceServer", Service: "app",
		File: "receiver.go",
		Meta: map[string]string{"pattern": "grpc_server_register", "impl": impl},
	}
}

// RegisterTraceServiceServer(s, &grpcTraceHandler{session: r.session}) — the
// registration node must gain a calls edge to every method on the impl
// struct, since that struct (not the registration call) is where a request's
// static flow actually continues.
func TestLinkGRPCHandlers_StructLiteral(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		grpcRegisterNode("app:receiver.go:grpc_handler:grpc_server_register:80", "&grpcTraceHandler{session: r.session}"),
		handlerMethod("receiver.go", "grpcTraceHandler", "Export"),
		handlerMethod("receiver.go", "grpcTraceHandler", "Close"),
		handlerMethod("receiver.go", "otherHandler", "Export"),
	}

	edges, unresolved := LinkGRPCHandlers(nodes)

	require.Len(t, edges, 2, "links every method on the impl struct, not just one")
	assert.Empty(t, unresolved)
	got := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, "app:receiver.go:grpc_handler:grpc_server_register:80", e.From)
		assert.Equal(t, graph.EdgeTypeCalls, e.Type)
		got[e.To] = true
	}
	assert.True(t, got["app:receiver.go:method:Export"])
	assert.True(t, got["app:receiver.go:method:Close"])
}

// A `New<Type>(...)` constructor call is the other common registration
// shape — the type name is recoverable from the function name alone.
func TestLinkGRPCHandlers_ConstructorCall(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		grpcRegisterNode("app:receiver.go:grpc_handler:grpc_server_register:12", "NewFooHandler(session)"),
		handlerMethod("receiver.go", "FooHandler", "Export"),
	}

	edges, unresolved := LinkGRPCHandlers(nodes)

	require.Len(t, edges, 1)
	assert.Empty(t, unresolved)
	assert.Equal(t, "app:receiver.go:method:Export", edges[0].To)
}

// A bare identifier (`impl`) names a local variable whose type isn't visible
// from text alone — the pass must ledger it rather than guess.
func TestLinkGRPCHandlers_BareIdentifierUnresolved(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		grpcRegisterNode("app:receiver.go:grpc_handler:grpc_server_register:9", "impl"),
	}

	edges, unresolved := LinkGRPCHandlers(nodes)

	assert.Empty(t, edges)
	require.Len(t, unresolved, 1)
	assert.Equal(t, "grpc_impl_unresolved", unresolved[0].Kind)
}
