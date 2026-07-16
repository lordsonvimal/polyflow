package contract_test

// Tests for G.7 EnrichAliases: alias/instance bindings, one-hop wrappers,
// publisher aliases, factory closures, and reassignment ledger.
//
// All node inputs are created manually to isolate EnrichAliases behaviour
// from pattern extraction (consistent with the G.6 test convention).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ── helper constructors ───────────────────────────────────────────────────────

func aliasBindingNode(service, file string, line int, aliasName, sourceKind, sourceMethod, baseURL string) graph.Node {
	meta := map[string]string{
		"alias_name":        aliasName,
		"alias_source_kind": sourceKind,
	}
	if sourceMethod != "" {
		meta["alias_source_method"] = sourceMethod
	}
	if baseURL != "" {
		meta["alias_base_url"] = baseURL
	}
	return graph.Node{
		ID: service + ":" + file + ":variable:" + aliasName + ":1",
		Type: graph.NodeTypeVariable, Service: service, File: file, Line: line,
		Label: aliasName, Meta: meta,
	}
}

func instanceBindingNode(service, file string, line int, instanceName, sourceKind, baseURL string) graph.Node {
	meta := map[string]string{
		"instance_name":        instanceName,
		"instance_source_kind": sourceKind,
	}
	if baseURL != "" {
		meta["instance_base_url"] = baseURL
	}
	return graph.Node{
		ID: service + ":" + file + ":variable:" + instanceName + ":1",
		Type: graph.NodeTypeVariable, Service: service, File: file, Line: line,
		Label: instanceName, Meta: meta,
	}
}

func aliasCallNode(service, file string, line int, viaAlias, method, url string) graph.Node {
	return graph.Node{
		ID: service + ":" + file + ":http_client:" + viaAlias + ":" + url + ":" + file + "." + string(rune('0'+line)),
		Type: graph.NodeTypeHTTPClient, Service: service, File: file, Line: line,
		Meta: map[string]string{
			"via_alias": viaAlias,
			"method":    method,
			"url":       url,
			"path":      url,
		},
	}
}

func wrapperFunctionNode(service, file string, line int, funcLabel, wrapperFor, wrapperBaseURL string) graph.Node {
	meta := map[string]string{
		"wrapper_for": wrapperFor,
	}
	if wrapperBaseURL != "" {
		meta["wrapper_base_url"] = wrapperBaseURL
	}
	return graph.Node{
		ID: service + ":" + file + ":function:" + funcLabel + ":" + string(rune('0'+line)),
		Type: graph.NodeTypeFunction, Service: service, File: file, Line: line,
		Label: funcLabel, Meta: meta,
	}
}

func wrapperCallNode(service, file string, line int, wrapperName, url string) graph.Node {
	return graph.Node{
		ID: service + ":" + file + ":http_client:wrapper_call:" + url + ":" + string(rune('0'+line)),
		Type: graph.NodeTypeHTTPClient, Service: service, File: file, Line: line,
		Meta: map[string]string{
			"wrapper_name": wrapperName,
			"url":          url,
			"path":         url,
		},
	}
}

func publisherAliasBinding(service, file string, line int, aliasName, sourceKind string) graph.Node {
	return graph.Node{
		ID:    service + ":" + file + ":variable:" + aliasName + ":1",
		Type:  graph.NodeTypeVariable,
		Service: service, File: file, Line: line,
		Label: aliasName,
		Meta: map[string]string{
			"alias_name":        aliasName,
			"alias_source_kind": sourceKind,
		},
	}
}

func publisherCallNode(service, file string, line int, viaAlias, topic string) graph.Node {
	return graph.Node{
		ID:    service + ":" + file + ":publisher:via_" + viaAlias + ":" + string(rune('0'+line)),
		Type:  graph.NodeTypePublisher,
		Service: service, File: file, Line: line,
		Meta: map[string]string{
			"via_alias": viaAlias,
			"topic":     topic,
		},
	}
}

// ── positive fixtures ─────────────────────────────────────────────────────────

// TestEnrichAliases_FetchAlias: var f = fetch → f('/path') → resolved http_client with via=alias.
func TestEnrichAliases_FetchAlias(t *testing.T) {
	nodes := []graph.Node{
		aliasBindingNode("svc-a", "requests.js", 1, "f", "http", "", ""),
		aliasCallNode("svc-a", "requests.js", 5, "f", "GET", "/users"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved, "no ledger entries for resolved alias")
	require.Len(t, enriched, 1, "binding node removed; call node kept")
	n := enriched[0]
	assert.Equal(t, "alias", n.Meta["via"])
	assert.Equal(t, "", n.Meta["via_alias"], "via_alias must be removed after resolution")
	assert.Equal(t, graph.NodeTypeHTTPClient, n.Type)
	assert.Equal(t, "GET", n.Meta["method"])
	assert.Equal(t, "/users", n.Meta["url"])
}

// TestEnrichAliases_JQueryAlias: var a = $.ajax → a('/path') → resolved http_client.
func TestEnrichAliases_JQueryAlias(t *testing.T) {
	nodes := []graph.Node{
		aliasBindingNode("svc-a", "app.js", 1, "a", "http", "", ""),
		aliasCallNode("svc-a", "app.js", 3, "a", "POST", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 1)
	assert.Equal(t, "alias", enriched[0].Meta["via"])
	assert.Equal(t, "POST", enriched[0].Meta["method"])
}

// TestEnrichAliases_AxiosDestructure: const { post } = axios → post('/orders') → method=POST + via=alias.
func TestEnrichAliases_AxiosDestructure(t *testing.T) {
	nodes := []graph.Node{
		aliasBindingNode("svc-a", "api.js", 2, "post", "http", "POST", ""),
		aliasCallNode("svc-a", "api.js", 5, "post", "", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 1)
	n := enriched[0]
	assert.Equal(t, "alias", n.Meta["via"])
	assert.Equal(t, "POST", n.Meta["method"], "method filled from alias source_method")
}

// TestEnrichAliases_AxiosInstance: const api = axios.create({baseURL:'/api'}) → api.get('/orders').
func TestEnrichAliases_AxiosInstance(t *testing.T) {
	nodes := []graph.Node{
		instanceBindingNode("svc-a", "client.js", 1, "api", "http", "/api"),
		aliasCallNode("svc-a", "client.js", 8, "api", "GET", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 1)
	n := enriched[0]
	assert.Equal(t, "alias", n.Meta["via"])
	assert.Equal(t, "/api/orders", n.Meta["url"], "base URL prepended to path")
}

// TestEnrichAliases_WrapperResolution: function callService(path) wraps axios.post(BASE+path).
func TestEnrichAliases_WrapperResolution(t *testing.T) {
	nodes := []graph.Node{
		wrapperFunctionNode("svc-a", "http.js", 1, "callService", "http", "/api"),
		wrapperCallNode("svc-a", "http.js", 10, "callService", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	// wrapper function node stays (it's a real function node)
	// wrapper call node is resolved
	wrapperFns := findNodesByMeta(enriched, "wrapper_for", "http")
	require.Len(t, wrapperFns, 1, "wrapper function node preserved")

	calls := findNodesByType(enriched, graph.NodeTypeHTTPClient)
	require.Len(t, calls, 1, "resolved call node in output")
	assert.Equal(t, "wrapper", calls[0].Meta["via"])
	assert.Equal(t, "/api/orders", calls[0].Meta["url"])
	assert.Equal(t, "", calls[0].Meta["wrapper_name"], "wrapper_name removed after resolution")
}

// TestEnrichAliases_WrapperNoBaseURL: wrapper with no base URL — just via=wrapper, URL unchanged.
func TestEnrichAliases_WrapperNoBaseURL(t *testing.T) {
	nodes := []graph.Node{
		wrapperFunctionNode("svc-a", "http.js", 1, "callAPI", "http", ""),
		wrapperCallNode("svc-a", "http.js", 10, "callAPI", "/users"),
	}
	enriched, _ := contract.EnrichAliases(nodes)

	calls := findNodesByType(enriched, graph.NodeTypeHTTPClient)
	require.Len(t, calls, 1)
	assert.Equal(t, "wrapper", calls[0].Meta["via"])
	assert.Equal(t, "/users", calls[0].Meta["url"], "URL unchanged when no base URL")
}

// TestEnrichAliases_PublisherAlias: const pub = produce → pub('orders.created') → publisher + via=alias.
func TestEnrichAliases_PublisherAlias(t *testing.T) {
	nodes := []graph.Node{
		publisherAliasBinding("svc-a", "events.js", 1, "pub", "kafka"),
		publisherCallNode("svc-a", "events.js", 5, "pub", "orders.created"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 1)
	n := enriched[0]
	assert.Equal(t, "alias", n.Meta["via"])
	assert.Equal(t, graph.NodeTypePublisher, n.Type)
}

// TestEnrichAliases_MultipleCallsThroughSameAlias: one binding → two calls resolved.
func TestEnrichAliases_MultipleCallsThroughSameAlias(t *testing.T) {
	nodes := []graph.Node{
		aliasBindingNode("svc-a", "client.js", 1, "http", "http", "", ""),
		aliasCallNode("svc-a", "client.js", 5, "http", "GET", "/users"),
		aliasCallNode("svc-a", "client.js", 7, "http", "POST", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 2, "both calls resolved")
	for _, n := range enriched {
		assert.Equal(t, "alias", n.Meta["via"])
		assert.Equal(t, "", n.Meta["via_alias"])
	}
}

// ── negative fixtures ─────────────────────────────────────────────────────────

// TestEnrichAliases_ReassignedAlias: alias bound twice → alias_reassigned ledger + calls dropped.
func TestEnrichAliases_ReassignedAlias(t *testing.T) {
	nodes := []graph.Node{
		aliasBindingNode("svc-a", "util.js", 1, "client", "http", "", ""),
		aliasBindingNode("svc-a", "util.js", 3, "client", "http", "", ""),
		aliasCallNode("svc-a", "util.js", 5, "client", "GET", "/users"),
		aliasCallNode("svc-a", "util.js", 6, "client", "POST", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Len(t, unresolved, 1, "exactly one alias_reassigned ledger entry")
	assert.Equal(t, "alias_reassigned", unresolved[0].Kind)
	assert.Equal(t, "client", unresolved[0].Name)
	// All call nodes dropped (unresolvable)
	assert.Empty(t, findNodesByType(enriched, graph.NodeTypeHTTPClient))
}

// TestEnrichAliases_UnknownWrapper: wrapper_name not in wrapper table → factory_dynamic ledger.
func TestEnrichAliases_UnknownWrapper(t *testing.T) {
	// No wrapper function node for "mystery" is provided.
	nodes := []graph.Node{
		wrapperCallNode("svc-a", "util.js", 10, "mystery", "/orders"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Len(t, unresolved, 1)
	assert.Equal(t, "factory_dynamic", unresolved[0].Kind)
	assert.Equal(t, "mystery", unresolved[0].Name)
	assert.Empty(t, findNodesByType(enriched, graph.NodeTypeHTTPClient))
}

// TestEnrichAliases_FactoryClosure: factory_closure=true node → factory_dynamic ledger + drop.
func TestEnrichAliases_FactoryClosure(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc-a:factory.js:http_client:closure:1", Type: graph.NodeTypeHTTPClient,
			Service: "svc-a", File: "factory.js", Line: 1,
			Label: "makeRequest",
			Meta:  map[string]string{"factory_closure": "true"},
		},
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Len(t, unresolved, 1)
	assert.Equal(t, "factory_dynamic", unresolved[0].Kind)
	assert.Empty(t, findNodesByType(enriched, graph.NodeTypeHTTPClient))
}

// TestEnrichAliases_CrossFileAlias: via_alias with no same-file binding → node passes through,
// via_alias stripped, engine processes with existing key fields.
func TestEnrichAliases_CrossFileAlias(t *testing.T) {
	// Only the call node, no binding node in this file.
	nodes := []graph.Node{
		aliasCallNode("svc-a", "service.js", 5, "externalClient", "GET", "/health"),
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved, "cross-file alias produces no ledger entry")
	require.Len(t, enriched, 1, "node passes through for engine to handle")
	n := enriched[0]
	assert.Equal(t, "", n.Meta["via_alias"], "via_alias stripped on unresolved cross-file alias")
	assert.Equal(t, "GET", n.Meta["method"], "method preserved")
	assert.Equal(t, "/health", n.Meta["url"], "url preserved")
}

// TestEnrichAliases_NonAliasNodesPassThrough: regular http_client nodes are unchanged.
func TestEnrichAliases_NonAliasNodesPassThrough(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc-a:api.js:http_client:POST /users:1", Type: graph.NodeTypeHTTPClient,
			Service: "svc-a", File: "api.js", Line: 1,
			Meta: map[string]string{"method": "POST", "url": "/users"},
		},
		{
			ID: "svc-a:handlers.go:http_handler:GET /items:5", Type: graph.NodeTypeHTTPHandler,
			Service: "svc-a", File: "handlers.go", Line: 5,
			Meta: map[string]string{"method": "GET", "path": "/items"},
		},
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	require.Len(t, enriched, 2, "non-alias nodes pass through unchanged")
}

// TestEnrichAliases_EmptyInput: empty node slice returns empty output.
func TestEnrichAliases_EmptyInput(t *testing.T) {
	enriched, unresolved := contract.EnrichAliases(nil)
	assert.Empty(t, enriched)
	assert.Empty(t, unresolved)
}

// TestEnrichAliases_WrapperKindToPublisher: wrapper that wraps kafka → call node becomes publisher.
func TestEnrichAliases_WrapperKindToPublisher(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc-a:events.js:function:publishOrder:1",
			Type: graph.NodeTypeFunction, Service: "svc-a", File: "events.js", Line: 1,
			Label: "publishOrder",
			Meta:  map[string]string{"wrapper_for": "kafka", "wrapper_base_url": ""},
		},
		{
			ID: "svc-a:events.js:publisher:wrapper_call:5",
			Type: graph.NodeTypePublisher, Service: "svc-a", File: "events.js", Line: 5,
			Meta: map[string]string{"wrapper_name": "publishOrder", "topic": "orders.created"},
		},
	}
	enriched, unresolved := contract.EnrichAliases(nodes)

	require.Empty(t, unresolved)
	pubs := findNodesByType(enriched, graph.NodeTypePublisher)
	require.Len(t, pubs, 1)
	assert.Equal(t, "wrapper", pubs[0].Meta["via"])
}

// TestEnrichAliases_EngineIntegration: alias-resolved node is matched by Engine.Link.
func TestEnrichAliases_EngineIntegration(t *testing.T) {
	nodes := []graph.Node{
		instanceBindingNode("svc-a", "client.js", 1, "api", "http", "/api"),
		aliasCallNode("svc-a", "client.js", 5, "api", "GET", "/users"),
		{
			ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/api/users"},
		},
	}

	enriched, aliasUnresolved := contract.EnrichAliases(nodes)
	require.Empty(t, aliasUnresolved)

	rule := contract.Rule{
		Kind: contract.KindHTTP,
		Producer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPClient,
			Key:  []string{"method", "path"},
			KeyFallbacks: map[string][]string{
				"path": {"url"},
			},
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPHandler,
			Key:  []string{"method", "path"},
		},
		Normalizers: []string{},
		Match:       []contract.MatchTier{contract.TierExact},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeHTTPCall,
			IDPrefix:    "http",
			SameService: "skip",
		},
		Unmatched: contract.UnmatchedLedger,
	}

	eng := &contract.Engine{}
	result := eng.Link(enriched, []contract.Rule{rule}, nil)

	require.Len(t, result.Edges, 1, "alias-resolved call matched handler")
	assert.Equal(t, "alias", result.Edges[0].Meta["via"], "via=alias propagated to edge")
	assert.Equal(t, "h1", result.Edges[0].To)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func findNodesByType(nodes []graph.Node, t graph.NodeType) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Type == t {
			out = append(out, n)
		}
	}
	return out
}

func findNodesByMeta(nodes []graph.Node, key, val string) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Meta[key] == val {
			out = append(out, n)
		}
	}
	return out
}
