package linker

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Linker resolves cross-service edges by matching HTTP client calls
// to HTTP handler nodes across services.
type Linker struct {
	config *workspace.WorkspaceConfig
}

// New creates a Linker for the given workspace configuration.
func New(cfg *workspace.WorkspaceConfig) *Linker {
	return &Linker{config: cfg}
}

var reParamColon = regexp.MustCompile(`:[^/]+`)
var reParamBrace = regexp.MustCompile(`\{[^}]+\}`)
var reParamRegex = regexp.MustCompile(`\[[^\]]+\][+*?]?`)

// normalizePath replaces parameter segments with * and strips trailing slashes.
func normalizePath(path string) string {
	p := reParamColon.ReplaceAllString(path, "*")
	p = reParamBrace.ReplaceAllString(p, "*")
	p = reParamRegex.ReplaceAllString(p, "*")
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	return p
}

// baseURLFor returns the base_url prefix for a from->to service link, or "".
func (l *Linker) baseURLFor(fromSvc, toSvc string) string {
	for _, link := range l.config.Links {
		if link.From == fromSvc && link.To == toSvc && link.BaseURL != "" {
			return link.BaseURL
		}
	}
	return ""
}

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

// Link attempts to resolve cross-service HTTP connections.
// It returns synthetic edges connecting client call nodes to handler nodes.
// Clients whose paths cannot be resolved still produce an edge with confidence "unknown".
func (l *Linker) Link(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, error) {
	if l.config == nil {
		return nil, fmt.Errorf("linker: no workspace config provided")
	}

	// Index handlers and clients.
	var handlers []*graph.Node
	nodeByID := make(map[string]*graph.Node)
	clients := make([]*graph.Node, 0)

	for i := range nodes {
		n := &nodes[i]
		nodeByID[n.ID] = n
		switch n.Type {
		case graph.NodeTypeHTTPHandler:
			handlers = append(handlers, n)
		case graph.NodeTypeHTTPClient:
			clients = append(clients, n)
		}
	}

	var crossEdges []graph.Edge
	for _, client := range clients {
		// Navigation links (href/action attributes) are not API calls: they
		// get navigates_to edges when a route handler matches, and are
		// dropped silently otherwise (an unmatched page link is not an
		// unresolved API dependency worth flagging).
		isNavLink := client.Meta["nav_link"] == "true"
		method := client.Meta["method"]
		path := client.Meta["path"]
		if path == "" {
			// Most JS client patterns capture "url", not "path" (fetch, axios,
			// EventSource). Extract the path portion so handlers can match.
			path = urlToPath(stripMeta(client.Meta["url"]))
		}
		targetSvc := client.Meta["target_service"]

		// Build handler lookup maps, stripping the base_url prefix from handler paths
		// so they align with the already-stripped client paths from ApplyHints.
		// When target_service is known, only index handlers from that service.
		baseURL := l.baseURLFor(client.Service, targetSvc)
		exactHandlers := make(map[string]*graph.Node)
		normalHandlers := make(map[string]*graph.Node)
		for _, h := range handlers {
			if targetSvc != "" && h.Service != targetSvc {
				continue
			}
			hpath := h.Meta["path"]
			if hpath == "" {
				// A handler without a path can never be a real route target;
				// indexing it would let empty-path clients "match" it.
				continue
			}
			if baseURL != "" && strings.HasPrefix(hpath, baseURL) {
				hpath = hpath[len(baseURL):]
				if hpath == "" {
					hpath = "/"
				}
			}
			raw := routeKey(h.Meta["method"], hpath)
			exactHandlers[raw] = h
			norm := routeKey(h.Meta["method"], normalizePath(hpath))
			normalHandlers[norm] = h
		}

		// Find handler and determine confidence
		handler, confidence := resolveHandler(method, path, exactHandlers, normalHandlers)

		if handler != nil {
			// Nav links connect a rendered page to the route that serves the
			// target — same-service pairs are the common case (a server-side
			// template linking to its own routes), so they are kept.
			if isNavLink {
				crossEdges = append(crossEdges, graph.Edge{
					ID:         fmt.Sprintf("nav:%s->%s", client.ID, handler.ID),
					From:       client.ID,
					To:         handler.ID,
					Type:       graph.EdgeTypeNavigatesTo,
					Label:      fmt.Sprintf("%s %s", method, path),
					Confidence: confidence,
					Method:     method,
					Path:       path,
					Meta:       map[string]string{"confidence": confidence, "via": "nav_link"},
				})
				continue
			}
			// Skip same-service pairs — those are already captured by "calls" edges
			if client.Service == handler.Service {
				continue
			}
			edgeMeta := map[string]string{"confidence": confidence}
			if client.Meta["datastar"] == "true" {
				edgeMeta["via"] = "datastar_action"
			}
			crossEdges = append(crossEdges, graph.Edge{
				ID:         fmt.Sprintf("link:%s->%s", client.ID, handler.ID),
				From:       client.ID,
				To:         handler.ID,
				Type:       graph.EdgeTypeHTTPCall,
				Label:      fmt.Sprintf("%s %s", method, path),
				Confidence: confidence,
				Method:     method,
				Path:       path,
				Meta:       edgeMeta,
			})
		} else if isNavLink {
			continue // unmatched page link: no unresolved-edge noise
		} else {
			// Unresolvable: emit edge with unknown confidence so the call is visible
			targetService := client.Meta["target_service"]
			targetID := "unresolved"
			if targetService != "" {
				targetID = "unresolved:" + targetService
			}
			crossEdges = append(crossEdges, graph.Edge{
				ID:         fmt.Sprintf("link:%s->%s", client.ID, targetID),
				From:       client.ID,
				To:         targetID,
				Type:       graph.EdgeTypeHTTPCall,
				Label:      fmt.Sprintf("%s %s", method, path),
				Confidence: graph.ConfidenceUnknown,
				Method:     method,
				Path:       path,
				Meta:       map[string]string{"confidence": graph.ConfidenceUnknown},
			})
		}
	}

	return crossEdges, nil
}

// candidateMethods returns the methods to try when matching. An empty method
// means the client didn't specify one (e.g. a bare fetch() call), so we
// fall back to the most common HTTP verbs in priority order.
func candidateMethods(method string) []string {
	if method != "" {
		return []string{strings.ToUpper(method)}
	}
	return []string{"GET", "POST", "PUT", "PATCH", "DELETE", ""}
}

func resolveHandler(method, path string, exact, normal map[string]*graph.Node) (*graph.Node, string) {
	if path == "" {
		// No path information — nothing to match against; the caller emits an
		// unresolved edge instead of blind-matching every empty-path handler.
		return nil, ""
	}
	for _, m := range candidateMethods(method) {
		rawKey := routeKey(m, path)
		if h, ok := exact[rawKey]; ok {
			return h, "static"
		}
	}
	// Try normalized matching across candidate methods.
	for _, m := range candidateMethods(method) {
		prefix := m + " "
		for normKey, h := range normal {
			if m != "" && !strings.HasPrefix(normKey, prefix) {
				continue
			}
			handlerNorm := normKey
			if m != "" {
				handlerNorm = normKey[len(prefix):]
			} else if i := strings.Index(normKey, " "); i >= 0 {
				handlerNorm = normKey[i+1:]
			}
			if pathMatchesPattern(path, handlerNorm) {
				return h, "inferred"
			}
		}
	}
	return nil, ""
}

// pathMatchesPattern returns true when path matches pattern where "*" in pattern
// matches any single non-empty path segment.
func pathMatchesPattern(path, pattern string) bool {
	ps := splitPath(path)
	pp := splitPath(pattern)
	if len(ps) != len(pp) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != ps[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}
	return strings.Split(p, "/")
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}

// urlToPath extracts the request path from a captured client URL: query
// strings are dropped, absolute http(s) URLs are reduced to their path, and
// values that are neither absolute URLs nor root-relative paths (variable
// references like mermaidURL(...)) resolve to "" — unmatched, not guessed.
func urlToPath(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			url = rest[j:]
		} else {
			url = "/"
		}
	}
	if !strings.HasPrefix(url, "/") {
		return ""
	}
	if qi := strings.Index(url, "?"); qi >= 0 {
		url = url[:qi]
	}
	return url
}

// LinkBrokerChannels emits cross-service publisher→subscriber edges by matching
// channel nodes that share the same exchange/routing_key across services.
// It returns synthetic http_call-style edges of type EdgeTypePublishes connecting
// a publisher directly to the matching subscriber (via their shared channel node).
func LinkBrokerChannels(nodes []graph.Node) []graph.Edge {
	// Index channel nodes by "exchange/routing_key", grouped by service.
	type channelEntry struct {
		nodeID  string
		service string
	}
	// channelKey -> []channelEntry
	channelsByKey := make(map[string][]channelEntry)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeChannel {
			continue
		}
		ex := n.Meta["exchange"]
		rk := n.Meta["routing_key"]
		key := ex + "/" + rk
		channelsByKey[key] = append(channelsByKey[key], channelEntry{n.ID, n.Service})
	}

	// Index publishers and subscribers: channelID -> []nodeID
	pubsByChannel := make(map[string][]string)
	subsByChannel := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypePublisher:
			ex := stripMeta(n.Meta["exchange"])
			rk := stripMeta(n.Meta["routing_key"])
			if ex == "" {
				continue
			}
			key := ex + "/" + rk
			for _, ch := range channelsByKey[key] {
				if ch.service == n.Service {
					pubsByChannel[ch.nodeID] = append(pubsByChannel[ch.nodeID], n.ID)
				}
			}
		case graph.NodeTypeSubscriber:
			// Subscribers carry queue, not exchange. Match via channel nodes in the same service.
			// The channel node was synthesized from a publisher in the same service that shares
			// the exchange. We match by finding channel nodes in any service whose exchange
			// corresponds to a publisher targeting this subscriber's service.
		}
	}
	_ = subsByChannel

	// Build subscriber index: service+channelKey -> []subscriberID
	// (subscribers are linked to channels via "subscribes" edges already emitted in matcher)
	// Here we emit cross-service edges: for each (pub channel, sub channel) pair with same key
	// but different services, emit publisher→subscriber edges.
	var crossEdges []graph.Edge
	for key, entries := range channelsByKey {
		if len(entries) < 2 {
			continue
		}
		// Collect pub-side and sub-side channel node IDs across services.
		// Publishers emit "publishes" edges TO a channel; subscribers receive "subscribes" FROM a channel.
		// The channel nodes from different services with the same key are the cross-service anchor.
		pubChannels := make([]channelEntry, 0)
		subChannels := make([]channelEntry, 0)
		_ = key

		for _, e := range entries {
			hasPub := len(pubsByChannel[e.nodeID]) > 0
			if hasPub {
				pubChannels = append(pubChannels, e)
			} else {
				subChannels = append(subChannels, e)
			}
		}

		for _, pubCh := range pubChannels {
			for _, subCh := range subChannels {
				if pubCh.service == subCh.service {
					continue
				}
				crossEdges = append(crossEdges, graph.Edge{
					ID:   fmt.Sprintf("broker:%s->%s", pubCh.nodeID, subCh.nodeID),
					From: pubCh.nodeID,
					To:   subCh.nodeID,
					Type: graph.EdgeTypePublishes,
					Meta: map[string]string{"via": "amqp_channel"},
				})
			}
		}
	}

	return crossEdges
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

// LinkBrokerHints applies workspace `links:` hints of the form
// {via: rabbitmq, exchange: "dsw.builds"}. Broker publishers whose exchange
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

// LinkJobQueues links background-job enqueue call sites to the job class's
// perform method by class name (ReportJob.perform_later → ReportJob#perform).
// Generic across queue backends: delayed_job and solid_queue both enqueue
// through the ActiveJob surface, and Sidekiq workers map onto the same
// publisher/subscriber node types.
func LinkJobQueues(nodes []graph.Node) []graph.Edge {
	performByClass := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeSubscriber {
			continue
		}
		if cls := stripMeta(n.Meta["job_class"]); cls != "" {
			performByClass[cls] = append(performByClass[cls], n.ID)
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypePublisher {
			continue
		}
		cls := stripMeta(n.Meta["job_class"])
		if cls == "" {
			continue
		}
		for _, target := range performByClass[cls] {
			if target == n.ID {
				continue
			}
			edges = append(edges, graph.Edge{
				ID:         fmt.Sprintf("job:%s->%s", n.ID, target),
				From:       n.ID,
				To:         target,
				Type:       graph.EdgeTypeJobEnqueue,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"job_class": cls},
			})
		}
	}
	return edges
}

// LinkPusherChannels links server-side Pusher.trigger call sites to pusher-js
// client subscriptions on the same channel name (nextGen Rails → pusher-js),
// including across services.
func LinkPusherChannels(nodes []graph.Node) []graph.Edge {
	subsByChannel := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if !strings.HasPrefix(n.Meta["pattern"], "pusher_subscribe") &&
			!strings.HasPrefix(n.Meta["pattern"], "pusher_channel") {
			continue
		}
		if ch := stripMeta(n.Meta["channel"]); ch != "" {
			subsByChannel[ch] = append(subsByChannel[ch], n.ID)
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if !strings.HasPrefix(n.Meta["pattern"], "pusher_trigger") {
			continue
		}
		// Only quoted string literals are statically resolvable — a bare
		// identifier is a variable-held channel and must not be linked.
		raw := n.Meta["channel"]
		if len(raw) < 2 || (raw[0] != '"' && raw[0] != '\'') {
			continue
		}
		ch := stripMeta(raw)
		if ch == "" {
			continue
		}
		for _, target := range subsByChannel[ch] {
			edges = append(edges, graph.Edge{
				ID:         fmt.Sprintf("pusher:%s->%s", n.ID, target),
				From:       n.ID,
				To:         target,
				Type:       graph.EdgeTypePusherTrigger,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"channel": ch, "event": stripMeta(n.Meta["event"])},
			})
		}
	}
	return edges
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

// LinkHubFanout links SSE broadcast-hub fan-out: every hub Broadcast() call
// site reaches every Subscribe() call site in the same service (the hub's
// channel fan-out delivers each broadcast event to all subscribers). Static
// analysis cannot tell which hub instance a call goes through, so when a
// service has a single hub type the edges are "inferred"; with multiple hub
// types they degrade to "partial".
func LinkHubFanout(nodes []graph.Node) []graph.Edge {
	type hubCall struct {
		id      string
		service string
	}
	broadcasts := make(map[string][]hubCall) // service -> broadcast call sites
	subscribes := make(map[string][]hubCall) // service -> subscribe call sites
	hubTypes := make(map[string]map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		switch n.Meta["pattern"] {
		case "hub_broadcast_call":
			broadcasts[n.Service] = append(broadcasts[n.Service], hubCall{n.ID, n.Service})
		case "hub_subscribe_call":
			subscribes[n.Service] = append(subscribes[n.Service], hubCall{n.ID, n.Service})
		case "hub_method_decl":
			if ht := n.Meta["receiver"]; ht != "" {
				if hubTypes[n.Service] == nil {
					hubTypes[n.Service] = make(map[string]bool)
				}
				hubTypes[n.Service][ht] = true
			}
		}
	}

	var edges []graph.Edge
	for svc, pubs := range broadcasts {
		subs := subscribes[svc]
		confidence := graph.ConfidenceInferred
		if len(hubTypes[svc]) > 1 {
			confidence = graph.ConfidencePartial
		}
		for _, pub := range pubs {
			for _, sub := range subs {
				edges = append(edges, graph.Edge{
					ID:         fmt.Sprintf("hub:%s->%s", pub.id, sub.id),
					From:       pub.id,
					To:         sub.id,
					Type:       graph.EdgeTypeHubBroadcast,
					Confidence: confidence,
					Meta:       map[string]string{"via": "hub_fanout"},
				})
			}
		}
	}
	return edges
}

// LinkWebSocketMessages links typed WebSocket senders to the dispatch cases
// handling the same message type ({type: "battery"} → case 'battery'),
// including across services (tether client ↔ server).
func LinkWebSocketMessages(nodes []graph.Node) []graph.Edge {
	// message type → dispatch node IDs
	dispatchByType := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if strings.HasPrefix(n.Meta["pattern"], "ws_dispatch") {
			if mt := stripMeta(n.Meta["message_type"]); mt != "" {
				dispatchByType[mt] = append(dispatchByType[mt], n.ID)
			}
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if !strings.HasPrefix(n.Meta["pattern"], "ws_send") {
			continue
		}
		mt := stripMeta(n.Meta["message_type"])
		if mt == "" {
			continue
		}
		for _, target := range dispatchByType[mt] {
			if target == n.ID {
				continue
			}
			edges = append(edges, graph.Edge{
				ID:         fmt.Sprintf("ws:%s->%s", n.ID, target),
				From:       n.ID,
				To:         target,
				Type:       graph.EdgeTypeWSSend,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"message_type": mt},
			})
		}
	}
	return edges
}
