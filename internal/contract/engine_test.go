package contract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// httpRule builds a minimal HTTP-like rule for engine tests.
func httpRule(sameService string, unmatched contract.UnmatchedPolicy) contract.Rule {
	return contract.Rule{
		Kind: contract.KindHTTP,
		Producer: contract.EndpointSpec{
			Node:  graph.NodeTypeHTTPClient,
			Where: map[string]string{"nav_link": ""},
			Key:   []string{"method", "path"},
			KeyFallbacks: map[string][]string{
				"path": {"url"},
			},
			MethodFallback:    []string{"GET", "POST", "PUT", "PATCH", "DELETE", ""},
			TargetServiceMeta: "target_service",
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPHandler,
			Key:  []string{"method", "path"},
		},
		Normalizers: []string{"url_to_path", "query_strip", "param_wildcard", "trim_slash"},
		Match:       []contract.MatchTier{contract.TierExact, contract.TierNormalized, contract.TierWildcardAnchored},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeHTTPCall,
			IDPrefix:    "link",
			SameService: sameService,
			ViaMeta:     map[string]string{"datastar": "datastar_action"},
		},
		Unmatched: unmatched,
	}
}

func client(id, service, method, path string, extra ...string) graph.Node {
	meta := map[string]string{"method": method, "path": path}
	for i := 0; i+1 < len(extra); i += 2 {
		meta[extra[i]] = extra[i+1]
	}
	return graph.Node{ID: id, Type: graph.NodeTypeHTTPClient, Service: service, Meta: meta}
}

func handler(id, service, method, path string) graph.Node {
	return graph.Node{
		ID:      id,
		Type:    graph.NodeTypeHTTPHandler,
		Service: service,
		Meta:    map[string]string{"method": method, "path": path},
	}
}

// --- Exact tier ---

func TestEngine_ExactMatch_CrossService(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users"),
		handler("h1", "svc-b", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
	assert.Equal(t, graph.ConfidenceStatic, result.Edges[0].Confidence)
	assert.Equal(t, graph.EdgeTypeHTTPCall, result.Edges[0].Type)
}

func TestEngine_ExactMatch_SameServiceSkipped(t *testing.T) {
	// same_service=skip: same-service match → producer goes to unmatched
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users"),
		handler("h1", "svc-a", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, graph.ConfidenceUnknown, result.Edges[0].Confidence)
	assert.Contains(t, result.Edges[0].To, "unresolved")
}

func TestEngine_ExactMatch_SameServiceKept(t *testing.T) {
	// same_service=keep: same-service match is allowed
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users"),
		handler("h1", "svc-a", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("keep", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

func TestEngine_SameService_SkipUnlessMeta(t *testing.T) {
	// skip_unless_meta:datastar: same-service skip, EXCEPT datastar clients
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users"),         // no datastar meta
		client("c2", "svc-a", "POST", "/action", "datastar", "true"), // datastar
		handler("h1", "svc-a", "GET", "/users"),
		handler("h2", "svc-a", "POST", "/action"),
	}
	rule := httpRule("skip_unless_meta:datastar", contract.UnmatchedDrop)
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{rule}, nil)

	require.Len(t, result.Edges, 1, "only the datastar client should emit an edge")
	assert.Equal(t, "link:c2->h2", result.Edges[0].ID)
	assert.Equal(t, "datastar_action", result.Edges[0].Meta["via"])
}

// --- Normalized tier ---

func TestEngine_NormalizedMatch_ParamWildcard(t *testing.T) {
	// Handler has :id param; client has literal ID
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users/123"),
		handler("h1", "svc-b", "GET", "/users/:id"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, graph.ConfidenceInferred, result.Edges[0].Confidence)
}

func TestEngine_NormalizedMatch_URLFallback(t *testing.T) {
	// Client provides url meta, not path; key_fallback should pick it up
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "url": "https://api.svc-b.local/users"}},
		handler("h1", "svc-b", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

// --- Wildcard tier ---

func TestEngine_WildcardMatch_ClientHasWildcard(t *testing.T) {
	// Client path has a wildcard (datastar partial); handler has literal segment
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users/*/posts"),
		handler("h1", "svc-b", "GET", "/users/:id/posts"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	// client norm: "GET /users/*/posts", handler norm: "GET /users/*/posts" → normalized match
	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

func TestEngine_WildcardAnchor_AllWildcards_NoMatch(t *testing.T) {
	// Negative: fully-wildcarded client path must not match any handler
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "*"),
		handler("h1", "svc-b", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, graph.ConfidenceUnknown, result.Edges[0].Confidence, "all-wildcard path must not match")
}

// --- Method fallback ---

func TestEngine_MethodFallback_EmptyClientMethod(t *testing.T) {
	// Client has no method; should try GET first and match handler
	nodes := []graph.Node{
		client("c1", "svc-a", "", "/users"),
		handler("h1", "svc-b", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

// --- Unmatched policies ---

func TestEngine_Unmatched_UnknownEdge(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/no-handler", "target_service", "svc-b"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->unresolved:svc-b", result.Edges[0].ID)
	assert.Equal(t, graph.ConfidenceUnknown, result.Edges[0].Confidence)
	require.Len(t, result.Nodes, 1)
	assert.Equal(t, "unresolved:svc-b", result.Nodes[0].ID)
}

func TestEngine_Unmatched_Ledger(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/missing"),
	}
	rule := httpRule("skip", contract.UnmatchedLedger)
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{rule}, nil)

	assert.Empty(t, result.Edges)
	require.Len(t, result.Unresolved, 1)
	assert.Equal(t, "svc-a", result.Unresolved[0].Service)
	assert.Equal(t, string(contract.KindHTTP), result.Unresolved[0].Kind)
}

func TestEngine_Unmatched_Drop(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/missing"),
	}
	rule := httpRule("skip", contract.UnmatchedDrop)
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{rule}, nil)

	assert.Empty(t, result.Edges)
	assert.Empty(t, result.Nodes)
	assert.Empty(t, result.Unresolved)
}

// --- Synthetic node deduplication ---

func TestEngine_SyntheticNode_Deduped(t *testing.T) {
	// Two unmatched producers targeting the same service → one synthetic node
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/x", "target_service", "svc-b"),
		client("c2", "svc-a", "POST", "/y", "target_service", "svc-b"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	assert.Len(t, result.Edges, 2)
	assert.Len(t, result.Nodes, 1, "synthetic unresolved node must be deduped")
}

// --- Target service filtering ---

func TestEngine_TargetService_FiltersConsumers(t *testing.T) {
	// Client targets svc-b; handler in svc-c must not match
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users", "target_service", "svc-b"),
		handler("h1", "svc-b", "GET", "/users"),
		handler("h2", "svc-c", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

// --- base_url_strip ---

func TestEngine_BaseURLStrip_ConsumerPath(t *testing.T) {
	// Handler's path has base_url prefix that must be stripped for matching.
	links := []workspace.Link{{From: "svc-a", To: "svc-b", BaseURL: "/api"}}
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users", "target_service", "svc-b"),
		handler("h1", "svc-b", "GET", "/api/users"),
	}
	rule := contract.Rule{
		Kind: contract.KindHTTP,
		Producer: contract.EndpointSpec{
			Node:              graph.NodeTypeHTTPClient,
			Key:               []string{"method", "path"},
			TargetServiceMeta: "target_service",
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPHandler,
			Key:  []string{"method", "path"},
		},
		Normalizers: []string{"base_url_strip", "trim_slash"},
		Match:       []contract.MatchTier{contract.TierNormalized},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeHTTPCall,
			IDPrefix:    "link",
			SameService: "skip",
		},
		Unmatched: contract.UnmatchedDrop,
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{rule}, links)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "link:c1->h1", result.Edges[0].ID)
}

// --- Where gate ---

func TestEngine_WhereGate_NavLink(t *testing.T) {
	// Nav-link variant only matches producers where nav_link="true"
	navRule := contract.Rule{
		Kind: contract.KindHTTP,
		Producer: contract.EndpointSpec{
			Node:  graph.NodeTypeHTTPClient,
			Where: map[string]string{"nav_link": "true"},
			Key:   []string{"method", "path"},
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPHandler,
			Key:  []string{"method", "path"},
		},
		Normalizers: []string{"trim_slash"},
		Match:       []contract.MatchTier{contract.TierExact},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeNavigatesTo,
			IDPrefix:    "nav",
			SameService: "keep",
		},
		Unmatched: contract.UnmatchedDrop,
	}
	nodes := []graph.Node{
		// nav_link client
		{ID: "nl1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/users", "nav_link": "true"}},
		// regular client (should NOT be picked up by nav rule)
		client("c1", "svc-a", "GET", "/users"),
		handler("h1", "svc-a", "GET", "/users"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "nav:nl1->h1", result.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeNavigatesTo, result.Edges[0].Type)
}

// --- No rules / no matching nodes ---

func TestEngine_NoRules(t *testing.T) {
	nodes := []graph.Node{client("c1", "svc-a", "GET", "/users")}
	e := &contract.Engine{}
	result := e.Link(nodes, nil, nil)

	assert.Empty(t, result.Edges)
	assert.Empty(t, result.Nodes)
	assert.Empty(t, result.Unresolved)
}

func TestEngine_NoMatchingNodes(t *testing.T) {
	// Rule exists but no nodes match the producer type
	rule := httpRule("skip", contract.UnmatchedDrop)
	nodes := []graph.Node{handler("h1", "svc-b", "GET", "/users")}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{rule}, nil)

	assert.Empty(t, result.Edges)
}
