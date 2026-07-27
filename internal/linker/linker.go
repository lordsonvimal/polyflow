package linker

import (
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// LinkRouteHandlers emits calls edges from HTTP route nodes to their handler
// function nodes. Route patterns capture the handler as Meta["handler"], but
// since parsing is per-file the reference can't be resolved there. This pass
// runs after all nodes are collected and matches by function label within the
// same service.
func LinkRouteHandlers(nodes []graph.Node) []graph.Edge {
	// Index function/method nodes: service + "\x00" + label → nodeID
	funcIndex := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			key := n.Service + "\x00" + n.Label
			if _, exists := funcIndex[key]; !exists {
				funcIndex[key] = n.ID
			}
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		handlerName, ok := n.Meta["handler"]
		if !ok || handlerName == "" {
			continue
		}
		// Strip method receiver: "s.handleSearch" → "handleSearch"
		if dot := strings.LastIndex(handlerName, "."); dot >= 0 {
			handlerName = handlerName[dot+1:]
		}
		calleeID, ok := funcIndex[n.Service+"\x00"+handlerName]
		if !ok {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("calls:%s->%s", n.ID, calleeID),
			From: n.ID,
			To:   calleeID,
			Type: graph.EdgeTypeCalls,
		})
	}
	return edges
}

// LinkRouteComponents emits renders edges from Solid Router client-route
// declarations (NodeTypeRoute, Meta["component"] set by the solid_route
// pattern) to the component function/method they reference. Mirrors
// LinkRouteHandlers's same-service label-lookup shape, but on a miss it
// ledgers the reference instead of silently skipping it (H.2:
// recall-over-precision — a route's component_ref is a real user-facing
// link and must be visible even when unresolved).
func LinkRouteComponents(nodes []graph.Node) ([]graph.Edge, []graph.UnresolvedRef) {
	funcIndex := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod || n.Type == graph.NodeTypeComponent {
			key := n.Service + "\x00" + n.Label
			if _, exists := funcIndex[key]; !exists {
				funcIndex[key] = n.ID
			}
		}
	}

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeRoute {
			continue
		}
		component := n.Meta["component"]
		if component == "" {
			continue
		}
		targetID, ok := funcIndex[n.Service+"\x00"+component]
		if !ok {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: component, Kind: "component_ref",
			})
			continue
		}
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("renders:%s->%s", n.ID, targetID),
			From:       n.ID,
			To:         targetID,
			Type:       graph.EdgeTypeRenders,
			Confidence: graph.ConfidenceInferred,
		})
	}
	return edges, unresolved
}

// templGeneratedPath maps a `.templ` source path to the path of the Go file
// `templ generate` produces beside it: `views/puzzles.templ` →
// `views/puzzles_templ.go`. Returns "" for non-templ paths.
func templGeneratedPath(templFile string) string {
	if !strings.HasSuffix(templFile, ".templ") {
		return ""
	}
	return templFile[:len(templFile)-len(".templ")] + "_templ.go"
}

// LinkTemplComponents bridges each templ component to its generated Go twin.
// A `.templ` component and the identically-named function in the sibling
// `_templ.go` file describe the same component but live in disjoint subgraphs:
// the generated function is the half the go/packages call graph reaches (a
// handler's `views.PuzzleRows(vm).Render(...)` call lands there), while the
// templ component is the half datastar/DOM edges attach to. This pass emits a
// bridge edge from the generated function to the templ component so a
// route→handler traversal crosses the seam into the component.
//
// Matching keys on the derived generated-file path plus label, not the bare
// label, so identically-named components in different packages don't collide.
func LinkTemplComponents(nodes []graph.Node) []graph.Edge {
	// Index generated Go functions living in a `_templ.go` file: file + "\x00" + label → nodeID.
	genFuncs := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction || n.Language != "go" {
			continue
		}
		if !strings.HasSuffix(n.File, "_templ.go") {
			continue
		}
		key := n.File + "\x00" + n.Label
		if _, exists := genFuncs[key]; !exists {
			genFuncs[key] = n.ID
		}
	}
	if len(genFuncs) == 0 {
		return nil
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeComponent || n.Language != "templ" {
			continue
		}
		genPath := templGeneratedPath(n.File)
		if genPath == "" {
			continue
		}
		funcID, ok := genFuncs[genPath+"\x00"+n.Label]
		if !ok {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeComponentImpl), funcID, n.ID),
			From:       funcID,
			To:         n.ID,
			Type:       graph.EdgeTypeComponentImpl,
			Confidence: graph.ConfidenceStatic,
			Meta:       map[string]string{"via": "templ_generated"},
		})
	}
	return edges
}



// stripMeta strips surrounding quotes from a meta value captured by tree-sitter.
func stripMeta(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// LinkDatastores emits queries/persists edges from datastore call-site nodes
// (GORM chains, database/sql calls; meta kind=call) to their service's
// logical datastore node (meta kind=store, derived from resolved driver
// dependencies). When a service has multiple engines the edge targets each —
// static analysis cannot tell which engine a *gorm.DB instance points at, so
// those extra edges carry confidence "partial" instead of "inferred".
func LinkDatastores(nodes []graph.Node) []graph.Edge {
	storesByService := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeDatastore && n.Meta["kind"] == "store" {
			storesByService[n.Service] = append(storesByService[n.Service], n.ID)
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDatastore || n.Meta["kind"] != "call" {
			continue
		}
		stores := storesByService[n.Service]
		edgeType := graph.EdgeTypeQueries
		if n.Meta["op"] == "persist" {
			edgeType = graph.EdgeTypePersists
		}
		confidence := graph.ConfidenceInferred
		if len(stores) > 1 {
			confidence = graph.ConfidencePartial
		}
		for _, storeID := range stores {
			edges = append(edges, graph.Edge{
				ID:         fmt.Sprintf("%s:%s->%s", string(edgeType), n.ID, storeID),
				From:       n.ID,
				To:         storeID,
				Type:       edgeType,
				Confidence: confidence,
			})
		}
	}
	return edges
}

// LinkTables closes the db end of the request path (Y.3c). A datastore call
// node (kind=call) already carries the literal SQL in meta.sql and reaches its
// enclosing function via the SSA `calls` edge, so the path
// `handler-fn → calls → repo-fn → calls → callNode → queries → store` is
// connected — but it terminates at an opaque driver/store node. This pass
// parses the table name out of the SQL (first FROM/INTO/UPDATE target) and
// emits `callNode → table` (queries/persists), minting one table node per
// (service, table). The call node is itself type=datastore, so the emitted
// edge is literally the plan's `datastore → table`, and the query now ends at
// a real entity. Statements with no resolvable table (PRAGMA, multi-statement)
// are left alone — no table node is fabricated (#12).
func LinkTables(nodes []graph.Node) ([]graph.Node, []graph.Edge) {
	tableID := func(service, name string) string {
		return service + ":table:" + name
	}
	seen := make(map[string]bool)
	var newNodes []graph.Node
	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDatastore || n.Meta["kind"] != "call" {
			continue
		}
		table := parseSQLTable(n.Meta["sql"])
		if table == "" {
			continue
		}
		tid := tableID(n.Service, table)
		if !seen[tid] {
			seen[tid] = true
			newNodes = append(newNodes, graph.Node{
				ID:       tid,
				Type:     graph.NodeTypeTable,
				Label:    table,
				Service:  n.Service,
				Language: n.Language,
				Meta:     map[string]string{"name": table},
			})
		}
		edgeType := graph.EdgeTypeQueries
		if n.Meta["op"] == "persist" {
			edgeType = graph.EdgeTypePersists
		}
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("%s:%s->%s", string(edgeType), n.ID, tid),
			From:       n.ID,
			To:         tid,
			Type:       edgeType,
			Confidence: graph.ConfidenceStatic,
			Meta:       map[string]string{"via": "sql_table", "table": table},
		})
	}
	return newNodes, edges
}

// parseSQLTable extracts the primary table name from a SQL literal: the token
// following the first FROM, INTO, or UPDATE keyword. Returns "" when the SQL
// has no such target (PRAGMA, DDL we don't model) or when the next token opens
// a subquery — the search then continues to the next keyword so an outer
// `FROM ( SELECT … FROM real_table )` resolves to the inner table. The keyword
// scan is case-insensitive; the returned name preserves the source casing with
// surrounding quotes/backticks/brackets and any trailing `(col,…)` stripped.
func parseSQLTable(rawSQL string) string {
	sql := stripMeta(strings.TrimSpace(rawSQL))
	if sql == "" {
		return ""
	}
	// Only consider the first statement; multi-statement PRAGMA blocks and the
	// like carry no single owning table.
	if semi := strings.IndexByte(sql, ';'); semi >= 0 {
		sql = sql[:semi]
	}
	fields := strings.Fields(sql)
	for i := 0; i < len(fields)-1; i++ {
		switch strings.ToUpper(fields[i]) {
		case "FROM", "INTO", "UPDATE":
			name := cleanTableToken(fields[i+1])
			if name == "" {
				continue // subquery "(" or empty — keep scanning
			}
			return name
		}
	}
	return ""
}

// cleanTableToken normalises a raw table token: strips a trailing column list
// (`meta(key,value)` → `meta`), surrounding quotes/backticks/brackets, and
// trailing punctuation. Returns "" for a subquery-opening "(" or an empty
// result.
func cleanTableToken(tok string) string {
	if paren := strings.IndexByte(tok, '('); paren >= 0 {
		tok = tok[:paren]
	}
	tok = strings.Trim(tok, "`\"'[]")
	tok = strings.TrimRight(tok, ",;")
	return tok
}

// LinkBrokerHints applies workspace `links:` hints of the form
// {via: rabbitmq, exchange: "maple.builds"}. Broker publishers whose exchange
// cannot be resolved statically (e.g. Ruby bunny publishes through an
// exchange held in a variable) and consumers that only know their queue name
// get connected through a shared channel node for the hinted exchange:
//
//	publisher(from-service) → channel(exchange) → subscriber(to-service)
//
// Hint edges are user-declared, so they carry confidence "static".
func LinkBrokerHints(links []workspace.Link, nodes []graph.Node) ([]graph.Node, []graph.Edge) {
	var newNodes []graph.Node
	var edges []graph.Edge

	for _, link := range links {
		if link.Via != "rabbitmq" || link.Exchange == "" {
			continue
		}
		channelID := "broker:channel:" + link.Exchange
		channelCreated := false

		ensureChannel := func() {
			if channelCreated {
				return
			}
			channelCreated = true
			newNodes = append(newNodes, graph.Node{
				ID:      channelID,
				Type:    graph.NodeTypeChannel,
				Label:   link.Exchange,
				Service: link.From,
				Meta:    map[string]string{"exchange": link.Exchange, "hint": "true"},
			})
		}

		for i := range nodes {
			n := &nodes[i]
			if !isBrokerPattern(n.Meta["pattern"]) {
				continue // ws/hub/pusher/job publishers are not RabbitMQ traffic
			}
			switch {
			case n.Type == graph.NodeTypePublisher && n.Service == link.From && stripMeta(n.Meta["exchange"]) == "":
				ensureChannel()
				edges = append(edges, graph.Edge{
					ID:         fmt.Sprintf("publishes:%s->%s", n.ID, channelID),
					From:       n.ID,
					To:         channelID,
					Type:       graph.EdgeTypePublishes,
					Confidence: graph.ConfidenceStatic,
					Meta:       map[string]string{"via": "workspace_hint"},
				})
			case n.Type == graph.NodeTypeSubscriber && n.Service == link.To:
				ensureChannel()
				edges = append(edges, graph.Edge{
					ID:         fmt.Sprintf("subscribes:%s->%s", channelID, n.ID),
					From:       channelID,
					To:         n.ID,
					Type:       graph.EdgeTypeSubscribes,
					Confidence: graph.ConfidenceStatic,
					Meta:       map[string]string{"via": "workspace_hint"},
				})
			}
		}
	}
	return newNodes, edges
}

// isBrokerPattern reports whether a pattern name represents message-broker
// traffic (as opposed to WebSocket/hub/Pusher publishers, which also use
// publisher/subscriber node types but must not be attached to broker hints).
func isBrokerPattern(pattern string) bool {
	if strings.HasPrefix(pattern, "ws_") || strings.HasPrefix(pattern, "hub_") ||
		strings.HasPrefix(pattern, "pusher_") {
		return false
	}
	return strings.Contains(pattern, "publish") || strings.Contains(pattern, "consume") ||
		strings.Contains(pattern, "subscribe")
}



// LinkSSEClients connects an EventSource connection to the message handlers
// registered on it in the same file (es.onmessage = …, es.on('message', …)).
// Without this edge the subscriber floats disconnected from the stream that
// feeds it.
func LinkSSEClients(nodes []graph.Node) []graph.Edge {
	clientsByFile := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Meta["pattern"] == "eventsource_connect" {
			clientsByFile[n.File] = append(clientsByFile[n.File], n.ID)
		}
	}
	if len(clientsByFile) == 0 {
		return nil
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeSubscriber {
			continue
		}
		p := n.Meta["pattern"]
		if p != "ws_onmessage_assign" && p != "ws_on_message" {
			continue
		}
		for _, clientID := range clientsByFile[n.File] {
			edges = append(edges, graph.Edge{
				ID:         fmt.Sprintf("sse:%s->%s", clientID, n.ID),
				From:       clientID,
				To:         n.ID,
				Type:       graph.EdgeTypeSubscribes,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"via": "eventsource"},
			})
		}
	}
	return edges
}


