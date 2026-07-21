package semantic

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ─── node card tests ──────────────────────────────────────────────────────

// TestBuildNodeCard_Golden asserts the expected card text for a range of node
// types, ensuring each card includes label, type, service, file, and the
// appropriate routing/signature meta.
func TestBuildNodeCard_Golden(t *testing.T) {
	cases := []struct {
		name string
		node graph.Node
		want string
	}{
		{
			name: "http_handler with method and path",
			node: graph.Node{
				ID: "fn:handlePurchase", Label: "handlePurchase",
				Type: graph.NodeTypeHTTPHandler, Service: "api",
				File: "internal/api/purchase.go", Line: 41,
				Meta: map[string]string{"method": "POST", "path": "/orders"},
			},
			want: "handlePurchase http_handler api internal/api/purchase.go POST /orders",
		},
		{
			name: "route node with path only",
			node: graph.Node{
				ID: "route:GET /users", Label: "GET /users",
				Type: graph.NodeTypeRoute, Service: "api",
				File: "router.go",
				Meta: map[string]string{"path": "/users", "method": "GET"},
			},
			want: "GET /users route api router.go GET /users",
		},
		{
			name: "function with no meta",
			node: graph.Node{
				ID: "fn:processOrder", Label: "processOrder",
				Type: graph.NodeTypeFunction, Service: "api",
				File: "internal/orders/process.go",
			},
			want: "processOrder function api internal/orders/process.go",
		},
		{
			name: "publisher with exchange",
			node: graph.Node{
				ID: "pub:orders", Label: "publishOrder",
				Type: graph.NodeTypePublisher, Service: "api",
				File: "events/publisher.go",
				Meta: map[string]string{"exchange": "orders.created"},
			},
			want: "publishOrder publisher api events/publisher.go orders.created",
		},
		{
			name: "subscriber with channel",
			node: graph.Node{
				ID: "sub:payments", Label: "paymentHandler",
				Type: graph.NodeTypeSubscriber, Service: "payments",
				File: "workers/payment_worker.go",
				Meta: map[string]string{"channel": "payments.new"},
			},
			want: "paymentHandler subscriber payments workers/payment_worker.go payments.new",
		},
		{
			name: "grpc handler",
			node: graph.Node{
				ID: "grpc:UserService/GetUser", Label: "GetUser",
				Type: graph.NodeTypeGRPCHandler, Service: "users",
				File: "rpc/user_server.go",
				Meta: map[string]string{"service_method": "/UserService/GetUser"},
			},
			want: "GetUser grpc_handler users rpc/user_server.go /UserService/GetUser",
		},
		{
			name: "graphql resolver",
			node: graph.Node{
				ID: "gql:Query.user", Label: "userResolver",
				Type: graph.NodeTypeGraphQLResolver, Service: "api",
				File: "resolvers/user.go",
				Meta: map[string]string{"operation": "Query.user"},
			},
			want: "userResolver graphql_resolver api resolvers/user.go Query.user",
		},
		{
			name: "http_handler method only",
			node: graph.Node{
				ID: "fn:delete", Label: "deleteUser",
				Type: graph.NodeTypeHTTPHandler, Service: "api",
				File: "handlers/user.go",
				Meta: map[string]string{"method": "DELETE"},
			},
			want: "deleteUser http_handler api handlers/user.go DELETE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ent := BuildNodeCard(&tc.node)
			assert.Equal(t, tc.want, ent.Text)
			assert.Equal(t, tc.node.ID, ent.ID)
			assert.Equal(t, "node", ent.Type)
			assert.Equal(t, tc.node.ID, ent.NodeID)
			assert.NotEmpty(t, ent.ContentHash)
		})
	}
}

// TestBuildNodeCard_ContentHashStable verifies that two calls with the same
// node produce the same ContentHash (embedding determinism gate).
func TestBuildNodeCard_ContentHashStable(t *testing.T) {
	n := &graph.Node{
		ID: "fn:handlePurchase", Label: "handlePurchase",
		Type: graph.NodeTypeHTTPHandler, Service: "api",
		File: "internal/api/purchase.go",
		Meta: map[string]string{"method": "POST", "path": "/orders"},
	}
	e1 := BuildNodeCard(n)
	e2 := BuildNodeCard(n)
	assert.Equal(t, e1.ContentHash, e2.ContentHash)
	assert.Equal(t, e1.Text, e2.Text)
}

// ─── flow chain tests ─────────────────────────────────────────────────────

// buildTestIdx constructs an AdjacencyIndex from node and edge slices.
func buildTestIdx(nodes []graph.Node, edges []graph.Edge) *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	for i := range nodes {
		idx.AddNode(&nodes[i])
	}
	for i := range edges {
		idx.AddEdge(&edges[i])
	}
	return idx
}

// TestBuildFlowChains_Golden asserts that a simple route → handler → publisher
// graph produces one chain entity whose text carries all expected terms.
func TestBuildFlowChains_Golden(t *testing.T) {
	nodes := []graph.Node{
		{ID: "route:POST /orders", Label: "POST /orders",
			Type: graph.NodeTypeRoute, Service: "api", File: "router.go",
			Meta: map[string]string{"method": "POST", "path": "/orders"}},
		{ID: "fn:handleOrder", Label: "handleOrder",
			Type: graph.NodeTypeHTTPHandler, Service: "api", File: "handlers/order.go"},
		{ID: "pub:orders.created", Label: "publishOrderCreated",
			Type: graph.NodeTypePublisher, Service: "api", File: "events/order.go",
			Meta: map[string]string{"exchange": "orders.created"}},
	}
	edges := []graph.Edge{
		{ID: "e1", From: "route:POST /orders", To: "fn:handleOrder", Type: graph.EdgeTypeCalls},
		{ID: "e2", From: "fn:handleOrder", To: "pub:orders.created", Type: graph.EdgeTypePublishes},
	}
	idx := buildTestIdx(nodes, edges)
	chains := BuildFlowChains(idx)

	require.NotEmpty(t, chains, "expected at least one flow chain")

	// Find the chain starting from the route.
	var hit *Entity
	for i := range chains {
		if chains[i].NodeID == "route:POST /orders" {
			hit = &chains[i]
			break
		}
	}
	require.NotNil(t, hit, "expected a chain rooted at route:POST /orders")

	assert.Equal(t, "flow", hit.Type)
	assert.True(t, strings.HasPrefix(hit.ID, "chain:route:POST /orders:"),
		"chain ID prefix: %s", hit.ID)
	assert.Contains(t, hit.Text, "POST /orders")
	assert.Contains(t, hit.Text, "handleOrder")
	assert.Contains(t, hit.Text, "publishOrderCreated")
	assert.Len(t, hit.Members, 3, "chain should have 3 members")
	assert.Equal(t, "route:POST /orders", hit.Members[0])
}

// TestBuildFlowChains_FanOut verifies that two routes sharing the same handler
// produce two separate chains (fan-out, not first-match — bug-class rule 1).
func TestBuildFlowChains_FanOut(t *testing.T) {
	nodes := []graph.Node{
		{ID: "route:GET /orders", Label: "GET /orders",
			Type: graph.NodeTypeRoute, Service: "api", File: "router.go"},
		{ID: "route:POST /orders", Label: "POST /orders",
			Type: graph.NodeTypeRoute, Service: "api", File: "router.go"},
		{ID: "fn:handleOrder", Label: "handleOrder",
			Type: graph.NodeTypeFunction, Service: "api", File: "handler.go"},
	}
	edges := []graph.Edge{
		{ID: "e1", From: "route:GET /orders", To: "fn:handleOrder", Type: graph.EdgeTypeCalls},
		{ID: "e2", From: "route:POST /orders", To: "fn:handleOrder", Type: graph.EdgeTypeCalls},
	}
	idx := buildTestIdx(nodes, edges)
	chains := BuildFlowChains(idx)

	// Both routes must produce chains — not just the first one encountered.
	var routeRoots []string
	for _, c := range chains {
		routeRoots = append(routeRoots, c.NodeID)
	}
	assert.Contains(t, routeRoots, "route:GET /orders", "GET route must produce a chain")
	assert.Contains(t, routeRoots, "route:POST /orders", "POST route must produce a chain")
	assert.Equal(t, 2, len(chains), "expected exactly 2 chains (one per route)")
}

// TestBuildFlowChains_CapAt12 verifies that chains longer than 12 nodes are
// trimmed to exactly 12 members.
func TestBuildFlowChains_CapAt12(t *testing.T) {
	// Build a linear chain: one route → 14 functions.
	const chainLen = 14
	nodes := make([]graph.Node, chainLen+1)
	nodes[0] = graph.Node{ID: "route:root", Label: "root",
		Type: graph.NodeTypeRoute, Service: "svc", File: "r.go"}
	edges := make([]graph.Edge, chainLen)
	for i := 1; i <= chainLen; i++ {
		nodes[i] = graph.Node{
			ID: fmt.Sprintf("fn:f%d", i), Label: fmt.Sprintf("f%d", i),
			Type: graph.NodeTypeFunction, Service: "svc", File: "fns.go",
		}
		from := nodes[i-1].ID
		edges[i-1] = graph.Edge{
			ID: fmt.Sprintf("e%d", i), From: from, To: nodes[i].ID,
			Type: graph.EdgeTypeCalls,
		}
	}
	idx := buildTestIdx(nodes, edges)
	chains := BuildFlowChains(idx)

	require.NotEmpty(t, chains)
	for _, c := range chains {
		assert.LessOrEqual(t, len(c.Members), chainNodeCap,
			"chain must not exceed %d members, got %d", chainNodeCap, len(c.Members))
	}
}

// TestBuildFlowChains_Determinism runs BuildFlowChains twice on the same index
// and asserts byte-identical output (bug-class rule 2).
func TestBuildFlowChains_Determinism(t *testing.T) {
	nodes := []graph.Node{
		{ID: "route:A", Label: "A", Type: graph.NodeTypeRoute, Service: "svc", File: "r.go"},
		{ID: "route:B", Label: "B", Type: graph.NodeTypeRoute, Service: "svc", File: "r.go"},
		{ID: "fn:C", Label: "C", Type: graph.NodeTypeFunction, Service: "svc", File: "f.go"},
		{ID: "fn:D", Label: "D", Type: graph.NodeTypeFunction, Service: "svc", File: "f.go"},
	}
	edges := []graph.Edge{
		{ID: "e1", From: "route:A", To: "fn:C", Type: graph.EdgeTypeCalls},
		{ID: "e2", From: "route:A", To: "fn:D", Type: graph.EdgeTypeCalls},
		{ID: "e3", From: "route:B", To: "fn:C", Type: graph.EdgeTypeCalls},
	}
	idx := buildTestIdx(nodes, edges)

	run1 := BuildFlowChains(idx)
	run2 := BuildFlowChains(idx)

	require.Equal(t, len(run1), len(run2), "chain count differs between runs")
	for i := range run1 {
		assert.Equal(t, run1[i].ID, run2[i].ID, "chain ID at index %d differs", i)
		assert.Equal(t, run1[i].Text, run2[i].Text, "chain text at index %d differs", i)
		assert.Equal(t, run1[i].ContentHash, run2[i].ContentHash,
			"content hash at index %d differs", i)
		assert.Equal(t, run1[i].Members, run2[i].Members, "members at index %d differ", i)
	}
}

// ─── doc chunk tests ──────────────────────────────────────────────────────

// TestBuildDocChunks_Jargon is the acceptance fixture from the S.1 spec:
// a README says "Falcon handles purchases" and the code calls it
// PurchaseHandler — the doc chunk must carry both "Falcon" and "purchases"
// so the hybrid searcher bridges the vocabulary gap.
func TestBuildDocChunks_Jargon(t *testing.T) {
	dir := t.TempDir()

	// Write a README with jargon that doesn't appear in code.
	readme := "# Checkout System\n\nFalcon handles purchases and manages the order lifecycle.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644))

	svcPaths := []ServicePath{{Path: dir, Service: "api"}}
	chunks := BuildDocChunks(svcPaths, nil)

	require.NotEmpty(t, chunks, "expected at least one doc chunk from the README")

	// At least one chunk must carry both "Falcon" and "purchases".
	var foundJargon bool
	for _, c := range chunks {
		if strings.Contains(c.Text, "Falcon") && strings.Contains(c.Text, "purchases") {
			foundJargon = true
			break
		}
	}
	assert.True(t, foundJargon,
		"expected a doc chunk containing both 'Falcon' and 'purchases'; got chunks: %v",
		chunkTexts(chunks))
}

// TestBuildDocChunks_MarkdownSplitsOnHeaders verifies that a multi-section
// README is split into separate chunks at ATX headers.
func TestBuildDocChunks_MarkdownSplitsOnHeaders(t *testing.T) {
	dir := t.TempDir()
	content := "# Section One\n\nFirst section content.\n\n## Section Two\n\nSecond section content.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte(content), 0o644))

	svcPaths := []ServicePath{{Path: dir, Service: "api"}}
	chunks := BuildDocChunks(svcPaths, nil)

	require.GreaterOrEqual(t, len(chunks), 2,
		"expected at least 2 chunks for a 2-section README")
}

// TestBuildDocChunks_GoDocComments verifies that Go `//` comment blocks
// immediately preceding declarations are extracted as doc chunks.
func TestBuildDocChunks_GoDocComments(t *testing.T) {
	dir := t.TempDir()
	src := `package api

// PurchaseHandler processes incoming purchase requests.
// It validates the cart, reserves inventory, and queues an order.
func PurchaseHandler(w http.ResponseWriter, r *http.Request) {}

// unexposed is not preceded by a decl comment
var unexposed = 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "purchase.go"), []byte(src), 0o644))

	nodes := []graph.Node{
		{ID: "fn:PurchaseHandler", Label: "PurchaseHandler",
			File: filepath.Join(dir, "purchase.go"), Line: 5},
	}
	svcPaths := []ServicePath{{Path: dir, Service: "api"}}
	chunks := BuildDocChunks(svcPaths, nodes)

	// The PurchaseHandler comment should appear as a doc chunk.
	var found bool
	for _, c := range chunks {
		if strings.Contains(c.Text, "PurchaseHandler") && strings.Contains(c.Text, "purchase") {
			found = true
			// NodeID should point to the handler (nearest node after comment).
			assert.Equal(t, "fn:PurchaseHandler", c.NodeID,
				"doc-comment chunk should be associated with PurchaseHandler node")
			break
		}
	}
	assert.True(t, found,
		"expected doc chunk for PurchaseHandler comment; got: %v", chunkTexts(chunks))
}

// TestBuildDocChunks_Determinism runs BuildDocChunks twice on the same
// fixture and asserts byte-identical output (bug-class rule 2).
func TestBuildDocChunks_Determinism(t *testing.T) {
	dir := t.TempDir()
	content := "# My Service\n\nThis handles the checkout flow.\n\n## API\n\nEndpoints here.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte(content), 0o644))

	svcPaths := []ServicePath{{Path: dir, Service: "api"}}

	run1 := BuildDocChunks(svcPaths, nil)
	run2 := BuildDocChunks(svcPaths, nil)

	require.Equal(t, len(run1), len(run2), "chunk count differs between runs")
	for i := range run1 {
		assert.Equal(t, run1[i].ID, run2[i].ID, "chunk ID at index %d differs", i)
		assert.Equal(t, run1[i].Text, run2[i].Text, "chunk text at index %d differs", i)
		assert.Equal(t, run1[i].ContentHash, run2[i].ContentHash,
			"content hash at index %d differs", i)
	}
}

// TestBuildDocChunks_EmptyDir produces no chunks for an empty service directory.
func TestBuildDocChunks_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	chunks := BuildDocChunks([]ServicePath{{Path: dir, Service: "svc"}}, nil)
	assert.Empty(t, chunks)
}

// TestBuildDocChunks_LargeMarkdownSplits verifies that a large markdown
// section (> docChunkMaxChars) is split into multiple chunks.
func TestBuildDocChunks_LargeMarkdownSplits(t *testing.T) {
	dir := t.TempDir()
	// Build a paragraph that's clearly over 800 chars.
	long := strings.Repeat("This is content about the checkout flow. ", 30) // ~1200 chars
	content := "# Overview\n\n" + long + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte(content), 0o644))

	chunks := BuildDocChunks([]ServicePath{{Path: dir, Service: "api"}}, nil)
	assert.Greater(t, len(chunks), 1, "large section should be split into multiple chunks")
}

// ─── helpers ──────────────────────────────────────────────────────────────

func chunkTexts(chunks []Entity) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}
