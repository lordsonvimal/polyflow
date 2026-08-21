package linker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// LinkRouteHandlers emits calls edges from HTTP route nodes to their handler
// function nodes. Route patterns capture the handler as Meta["handler"], but
// since parsing is per-file the reference can't be resolved there. This pass
// runs after all nodes are collected and matches by function label within the
// same service.
//
// A bare label match is not enough: a service routinely defines the same method
// name on several handler structs (`appConfigHandler.SaveConfig` /
// `baseImageHandler.SaveConfig` / `execConfigHandler.SaveConfig`), and a
// label-only index collapses all of them onto whichever node was seen first.
// That mislinks the route *and* leaves the real handler with no inbound caller,
// so it reads as dead code. The receiver is recoverable on both sides — the
// route records the qualifier in Meta["handler"], the method records its struct
// in Meta["receiver"] — so prefer a receiver-qualified match and fall back to
// the label-only lookup only when the qualifier identifies nothing (a package
// name, or a local variable whose type we can't see).
func LinkRouteHandlers(nodes []graph.Node) []graph.Edge {
	// Index function/method nodes: service + "\x00" + label → nodeID, plus a
	// receiver-qualified index: service + "\x00" + lower(receiver) + "\x00" + label.
	// Go names a handler variable after its type (`baseImageHandler` for
	// `BaseImageHandler`), so the qualifier is compared case-insensitively.
	funcIndex := make(map[string]string)
	recvIndex := make(map[string]string)
	// byLabel keeps every same-label candidate in node order so an abbreviated
	// qualifier can still be narrowed; the first entry reproduces funcIndex.
	byLabel := make(map[string][]*graph.Node)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			key := n.Service + "\x00" + n.Label
			if _, exists := funcIndex[key]; !exists {
				funcIndex[key] = n.ID
			}
			byLabel[key] = append(byLabel[key], n)
			if recv := n.Meta["receiver"]; recv != "" {
				rkey := n.Service + "\x00" + strings.ToLower(recv) + "\x00" + n.Label
				if _, exists := recvIndex[rkey]; !exists {
					recvIndex[rkey] = n.ID
				}
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
		// Split the receiver off: "baseImageHandler.SaveConfig" → qualifier
		// "baseImageHandler", label "SaveConfig".
		qualifier := ""
		if dot := strings.LastIndex(handlerName, "."); dot >= 0 {
			qualifier, handlerName = handlerName[:dot], handlerName[dot+1:]
		}
		// Only the innermost segment names the value being called:
		// "s.appConfigHandler.SaveConfig" → "appConfigHandler".
		if dot := strings.LastIndex(qualifier, "."); dot >= 0 {
			qualifier = qualifier[dot+1:]
		}
		// A qualifier that names a receiver type pins the handler exactly.
		calleeID, ok := "", false
		if qualifier != "" {
			calleeID, ok = recvIndex[n.Service+"\x00"+strings.ToLower(qualifier)+"\x00"+handlerName]
		}
		if !ok && qualifier != "" {
			calleeID, ok = uniqueAbbreviatedReceiver(byLabel[n.Service+"\x00"+handlerName], qualifier)
		}
		if !ok {
			calleeID, ok = funcIndex[n.Service+"\x00"+handlerName]
		}
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

// uniqueAbbreviatedReceiver narrows same-label handler candidates using a
// qualifier that abbreviates its type rather than mirroring it — `appController`
// for a `*UserAppController`, the shape `x := NewUserAppController(...)`
// produces. Only a suffix relationship counts (`appcontroller` ends
// `userappcontroller`), and only when exactly one candidate matches: an
// abbreviation is a hint, not a proof, so an ambiguous one must defer to the
// caller's fallback instead of guessing between two plausible structs.
func uniqueAbbreviatedReceiver(candidates []*graph.Node, qualifier string) (string, bool) {
	if len(candidates) < 2 {
		return "", false
	}
	q := strings.ToLower(qualifier)
	found := ""
	for _, c := range candidates {
		recv := strings.ToLower(c.Meta["receiver"])
		if recv == "" || (!strings.HasSuffix(recv, q) && !strings.HasSuffix(q, recv)) {
			continue
		}
		if found != "" {
			return "", false // ambiguous — two structs fit the abbreviation
		}
		found = c.ID
	}
	return found, found != ""
}

// LinkGRPCHandlers emits calls edges from a gRPC server-registration site
// (`Register<Service>Server(s, impl)`) to every method defined on the impl
// struct type. The grpc_server_register pattern captures the impl argument's
// raw source text into Meta["impl"] but nothing ever resolved it to a type,
// so the registration node — the only node representing the RPC entrypoint —
// had zero outgoing edges. A forward flow trace from it (UF flow lane, "flows
// through" a gRPC entrypoint) found nothing to walk despite the impl struct's
// methods containing the real handler logic.
//
// Unlike LinkRouteHandlers, there is no per-method capture to pin a single
// callee, so every method on the impl type is linked — the registration call
// hands the whole struct to grpc.Server, which dispatches to whichever RPC
// method a request names.
func LinkGRPCHandlers(nodes []graph.Node) ([]graph.Edge, []graph.UnresolvedRef) {
	recvIndex := make(map[string][]string) // service + "\x00" + lower(receiver) → method node IDs
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeMethod {
			continue
		}
		recv := n.Meta["receiver"]
		if recv == "" {
			continue
		}
		key := n.Service + "\x00" + strings.ToLower(strings.TrimPrefix(recv, "*"))
		recvIndex[key] = append(recvIndex[key], n.ID)
	}

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeGRPCHandler || n.Meta["pattern"] != "grpc_server_register" {
			continue
		}
		implType, ok := grpcImplTypeName(n.Meta["impl"])
		if !ok {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: n.Meta["impl"], Kind: "grpc_impl_unresolved",
			})
			continue
		}
		methodIDs := recvIndex[n.Service+"\x00"+strings.ToLower(implType)]
		if len(methodIDs) == 0 {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: implType, Kind: "grpc_impl_unresolved",
			})
			continue
		}
		for _, methodID := range methodIDs {
			edges = append(edges, graph.Edge{
				ID:   fmt.Sprintf("calls:%s->%s", n.ID, methodID),
				From: n.ID,
				To:   methodID,
				Type: graph.EdgeTypeCalls,
			})
		}
	}
	return edges, unresolved
}

// grpcImplTypeName recovers a bare type name from the raw source text of a
// gRPC registration's impl argument: `&grpcTraceHandler{session: r.session}`
// → "grpcTraceHandler", `pkg.FooHandler{}` → "FooHandler",
// `NewFooHandler(x)` → "FooHandler" (the New-prefixed constructor convention
// mirrored from uniqueAbbreviatedReceiver's abbreviation matching below). A
// bare identifier (`impl`) names a local variable whose type isn't visible
// from text alone, so it's reported unresolved rather than guessed at.
func grpcImplTypeName(impl string) (string, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(impl), "&")
	i := strings.IndexAny(s, "{(")
	if i <= 0 {
		return "", false
	}
	head := s[:i]
	if s[i] == '(' {
		if !strings.HasPrefix(head, "New") {
			return "", false
		}
		head = head[len("New"):]
	}
	if dot := strings.LastIndex(head, "."); dot >= 0 {
		head = head[dot+1:]
	}
	return head, head != ""
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

// LinkResourceSignals closes hop 6's fetch→signal binding (Y.6). A Solid
// createResource(loaderFn) accessor variable carries meta.resource_fn naming the
// loader function; the loader's fetch is an http_client node reached by a `calls`
// edge from the loader's function node. This pass emits `http_client → signal`
// (flows_to, via:resource) so the response dataflow reaches the reactive signal —
// and, through Y.6's signal→element dom_write edges, the DOM. A loader that isn't
// a resolvable same-file function, or that issues no fetch, yields no edge (#12).
func LinkResourceSignals(nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	// Loader function lookup by service+file+label (createResource(loaderFn) and
	// the loader almost always share a file/module).
	fnByKey := make(map[string]string)
	isHTTP := make(map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			k := n.Service + "\x00" + n.File + "\x00" + n.Label
			if _, ok := fnByKey[k]; !ok {
				fnByKey[k] = n.ID
			}
		case graph.NodeTypeHTTPClient:
			isHTTP[n.ID] = true
		}
	}
	// http_client nodes each function reaches (fn → http_client `calls` edges,
	// emitted by matcher Pass 2 from the enclosing function).
	clientsByFn := make(map[string][]string)
	for i := range edges {
		e := &edges[i]
		if e.Type == graph.EdgeTypeCalls && isHTTP[e.To] {
			clientsByFn[e.From] = append(clientsByFn[e.From], e.To)
		}
	}
	var out []graph.Edge
	seen := make(map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		rf := n.Meta["resource_fn"]
		if rf == "" {
			continue
		}
		fnID := fnByKey[n.Service+"\x00"+n.File+"\x00"+rf]
		if fnID == "" {
			continue // loader not a resolvable same-file function — ledger
		}
		for _, hc := range clientsByFn[fnID] {
			id := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeFlowsTo), hc, n.ID)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, graph.Edge{
				ID:         id,
				From:       hc,
				To:         n.ID,
				Type:       graph.EdgeTypeFlowsTo,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"via": "resource", "loader": rf},
			})
		}
	}
	return out
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
// {via: rabbitmq, exchange: "dsw.builds"}. Broker publishers whose exchange
// cannot be resolved statically (e.g. Ruby bunny publishes through an
// exchange held in a variable) and consumers that only know their queue name
// get connected through a shared channel node for the hinted exchange:
//
//	publisher(from-service) → channel(exchange) → subscriber(to-service)
//
// Tier J.3 gates the attachment on evidence. Before it, this was an
// unconditional cartesian join: every exchange-less publisher in `From` and
// *every* subscriber in `To` was joined to every hinted exchange, so a
// workspace declaring 5 exchanges turned 5 exchange-less subscribers into 25
// `subscribes` edges, 20 of them phantom, all stamped `static`. A node now
// attaches to a hinted exchange only when it either
//
//   - names that exchange (its own `exchange` meta, its `exchange_candidates`,
//     or a queue name that a resolved binding table maps to it), or
//   - names nothing at all *and* the workspace offers it exactly one
//     candidate exchange for its role — a single user declaration is
//     unambiguous, and this is the shape the feature exists for (a bunny
//     publisher holding its exchange in a variable).
//
// Everything else — no evidence, several candidates — is a guess, so it emits
// no edge and lands in the ledger instead (phases.md rule 12: intake is
// accounted for, not silently dropped).
//
// Where a real channel node already exists for the hinted exchange on either
// endpoint, the hint attaches to it rather than minting a parallel
// `broker:channel:<exchange>`; the two IDs never deduped, which is what
// produced nonsensical channel→channel `publishes` edges.
func LinkBrokerHints(links []workspace.Link, nodes []graph.Node) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	rabbit := make([]workspace.Link, 0, len(links))
	for _, l := range links {
		if l.Via == "rabbitmq" && l.Exchange != "" {
			rabbit = append(rabbit, l)
		}
	}
	if len(rabbit) == 0 {
		return nil, nil, nil
	}

	queueEx := queueExchangeIndex(nodes)
	chanIdx := realChannelIndex(nodes)

	var newNodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	minted := make(map[string]bool)  // synthetic channel ID → already appended
	seenEdge := make(map[string]bool) // edge ID → already emitted

	// channelsFor resolves the node(s) a hint for this exchange should meet on.
	// Real channels win over the synthetic one; among real channels the bare
	// exchange rendezvous (empty routing key) wins over per-routing-key nodes,
	// so a hint does not fan out across a topic exchange's keys.
	channelsFor := func(link workspace.Link) []string {
		if real := chanIdx[link.From+"\x00"+link.Exchange]; len(real) > 0 {
			return real
		}
		if real := chanIdx[link.To+"\x00"+link.Exchange]; len(real) > 0 {
			return real
		}
		id := "broker:channel:" + link.Exchange
		if !minted[id] {
			minted[id] = true
			newNodes = append(newNodes, graph.Node{
				ID:      id,
				Type:    graph.NodeTypeChannel,
				Label:   link.Exchange,
				Service: link.From,
				Meta: map[string]string{
					"exchange": link.Exchange,
					"hint":     "true",
					// doctor and the trust stamp must be able to exclude a node
					// that corresponds to no line of source anywhere.
					"synthetic": "true",
				},
			})
		}
		return []string{id}
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypePublisher && n.Type != graph.NodeTypeSubscriber {
			continue
		}
		if !isBrokerPattern(n.Meta["pattern"]) {
			continue // ws/hub/pusher/job publishers are not RabbitMQ traffic
		}
		isPub := n.Type == graph.NodeTypePublisher

		// Links this node could serve, by role and service alone.
		var eligible []workspace.Link
		for _, link := range rabbit {
			if isPub && n.Service == link.From {
				// A publisher that already resolved its own exchange has a real
				// channel from Tier W; the hint has nothing to add.
				if stripMeta(n.Meta["exchange"]) == "" {
					eligible = append(eligible, link)
				}
			} else if !isPub && n.Service == link.To {
				eligible = append(eligible, link)
			}
		}
		if len(eligible) == 0 {
			continue
		}

		// Of those, the ones this node actually names.
		evidence := nodeExchangeEvidence(n, queueEx)
		var matched []workspace.Link
		for _, link := range eligible {
			if evidence[link.Exchange] {
				matched = append(matched, link)
			}
		}

		switch {
		case len(matched) > 0:
			// Evidence decides. More than one exchange named is a genuine
			// ambiguity: keep the edges (recall) but never claim they are
			// verified topology (plan-14 trust soundness).
			conf := graph.ConfidenceStatic
			if len(matched) > 1 {
				conf = graph.ConfidencePartial
			}
			for _, link := range matched {
				emitHintEdges(&edges, seenEdge, n, channelsFor(link), conf, len(matched), "workspace_hint")
			}
		case len(evidence) == 0 && len(eligible) == 1:
			// No evidence, but the workspace offers exactly one answer.
			emitHintEdges(&edges, seenEdge, n, channelsFor(eligible[0]), graph.ConfidenceStatic, 1, "workspace_hint")
		default:
			// Either the node names exchanges none of which this workspace
			// links, or it names nothing while several exchanges compete.
			// Both are unresolved, not attachable.
			name := n.Label
			if name == "" {
				name = n.ID
			}
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service,
				File:    n.File,
				Line:    n.Line,
				Name:    name,
				Kind:    "amqp_exchange_unresolved",
			})
		}
	}
	return newNodes, edges, unresolved
}

// emitHintEdges connects one broker node to the channel(s) a hint resolved to.
// fanout is the number of candidate exchanges the node's evidence admitted; it
// is recorded on the edge so a reader can tell a single declaration from a
// hedge.
func emitHintEdges(edges *[]graph.Edge, seen map[string]bool, n *graph.Node, channelIDs []string, conf string, fanout int, via string) {
	if len(channelIDs) > 1 {
		// Several real channels answer to one exchange (a topic exchange with
		// per-routing-key nodes). Attaching to all of them is a hedge.
		conf = graph.ConfidencePartial
		fanout *= len(channelIDs)
	}
	for _, chanID := range channelIDs {
		if chanID == n.ID {
			continue
		}
		meta := map[string]string{"via": via}
		if fanout > 1 {
			meta["fanout"] = strconv.Itoa(fanout)
		}
		var e graph.Edge
		if n.Type == graph.NodeTypePublisher {
			e = graph.Edge{
				ID:   fmt.Sprintf("publishes:%s->%s", n.ID, chanID),
				From: n.ID, To: chanID, Type: graph.EdgeTypePublishes,
			}
		} else {
			e = graph.Edge{
				ID:   fmt.Sprintf("subscribes:%s->%s", chanID, n.ID),
				From: chanID, To: n.ID, Type: graph.EdgeTypeSubscribes,
			}
		}
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		e.Confidence = conf
		e.Meta = meta
		*edges = append(*edges, e)
	}
}

// nodeExchangeEvidence collects every exchange name a broker node points at:
// its own resolved exchange, the candidate list a partial resolution left
// behind, and the exchange(s) its queue is bound to according to the channels
// a binding table produced (Tier J.1). An empty result means the node names
// nothing — not that it names nothing matching.
func nodeExchangeEvidence(n *graph.Node, queueEx map[string][]string) map[string]bool {
	ev := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(stripMeta(s))
		// "dynamic"/"*" are the matcher's words for "unresolved"; they are not
		// exchange names and must never be treated as evidence.
		if s == "" || s == "dynamic" || s == "*" {
			return
		}
		ev[s] = true
	}
	add(n.Meta["exchange"])
	for _, c := range strings.Split(n.Meta["exchange_candidates"], ",") {
		add(c)
	}
	for _, key := range []string{"queue_name", "queue"} {
		q := strings.TrimSpace(stripMeta(n.Meta[key]))
		if q == "" {
			continue
		}
		for _, ex := range queueEx[q] {
			add(ex)
		}
	}
	return ev
}

// queueExchangeIndex maps a queue name to the exchanges it is bound to, using
// the channel nodes that carry a resolved binding (Tier J.1's static tables).
// Values are sorted: two channels can bind one queue to one exchange under
// different routing keys, and map order must never reach the output
// (bug-class #2).
func queueExchangeIndex(nodes []graph.Node) map[string][]string {
	set := make(map[string]map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeChannel {
			continue
		}
		q := stripMeta(n.Meta["queue_name"])
		ex := stripMeta(n.Meta["exchange"])
		if q == "" || ex == "" {
			continue
		}
		if set[q] == nil {
			set[q] = make(map[string]bool)
		}
		set[q][ex] = true
	}
	out := make(map[string][]string, len(set))
	for q, exs := range set {
		list := make([]string, 0, len(exs))
		for ex := range exs {
			list = append(list, ex)
		}
		sort.Strings(list)
		out[q] = list
	}
	return out
}

// realChannelIndex maps service+exchange to the IDs of the channel nodes that
// actually exist for it. Three preference tiers, most specific first, so a hint
// meets one node rather than every node that mentions the exchange:
//
//  1. the canonical rendezvous `<svc>:channel:<exchange>/` — the ID the
//     matcher and Tier W both converge on, and the one rule 3 exists to reuse;
//  2. canonical per-routing-key channels `<svc>:channel:<exchange>/<key>`;
//  3. anything else carrying the exchange (a declaration call site such as
//     `ExchangeDeclare`, which describes the exchange but is not the point
//     traffic meets at).
//
// Without the tiers a fixture holding both an `ExchangeDeclare` node and the
// canonical channel would fan a single unambiguous hint across both and
// degrade it to `partial`.
func realChannelIndex(nodes []graph.Node) map[string][]string {
	const (
		tierRendezvous = 0
		tierKeyed      = 1
		tierOther      = 2
	)
	byTier := make(map[string]map[int][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeChannel || n.Meta["hint"] == "true" {
			continue
		}
		ex := stripMeta(n.Meta["exchange"])
		if ex == "" || n.Service == "" {
			continue
		}
		canonical := strings.HasPrefix(n.ID, n.Service+":channel:"+ex)
		tier := tierOther
		switch {
		case canonical && stripMeta(n.Meta["routing_key"]) == "":
			tier = tierRendezvous
		case canonical:
			tier = tierKeyed
		}
		k := n.Service + "\x00" + ex
		if byTier[k] == nil {
			byTier[k] = make(map[int][]string)
		}
		byTier[k][tier] = append(byTier[k][tier], n.ID)
	}

	out := make(map[string][]string, len(byTier))
	for k, tiers := range byTier {
		for _, tier := range []int{tierRendezvous, tierKeyed, tierOther} {
			if ids := tiers[tier]; len(ids) > 0 {
				sort.Strings(ids)
				out[k] = ids
				break
			}
		}
	}
	return out
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

// LinkSSEPush adds the server→client push-direction edge for a live SSE
// connection. contracts/http.yaml's API-call variant already joins an
// eventsource_connect node to its server-side handler with an http_call
// edge shaped like a request (client→handler) — correct for "who does this
// connection reach," but backwards for flow tracing: once the connection is
// open, the handler keeps *writing* to it, so the real dataflow direction is
// handler→client. Without this edge, `polyflow flows` starting from the
// server entrypoint could never reach the client's onmessage handler (only
// LinkSSEClients' subscribes edge exists, and it starts at the client node).
// Must run after contract.Engine.Link since it depends on the http_call
// edges the engine produces.
func LinkSSEPush(nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	patternByID := make(map[string]string, len(nodes))
	for i := range nodes {
		patternByID[nodes[i].ID] = nodes[i].Meta["pattern"]
	}
	var out []graph.Edge
	for _, e := range edges {
		if e.Type != graph.EdgeTypeHTTPCall {
			continue
		}
		if patternByID[e.From] != "eventsource_connect" {
			continue
		}
		out = append(out, graph.Edge{
			ID:         fmt.Sprintf("sse_push:%s->%s", e.To, e.From),
			From:       e.To,
			To:         e.From,
			Type:       graph.EdgeTypeSSEEndpoint,
			Confidence: graph.ConfidenceInferred,
			Meta:       map[string]string{"via": "eventsource"},
		})
	}
	return out
}


