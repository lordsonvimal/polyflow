package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// mermaidFixture builds a small two-service graph with a version-gated cloud
// call and a partial-confidence datastore edge, covering every rendering rule.
func mermaidFixture() ([]*graph.Node, []*graph.Edge) {
	nodes := []*graph.Node{
		{ID: "web:app.js:http_client:POST /api/users:3", Label: "POST /api/users",
			Type: graph.NodeTypeHTTPClient, Service: "web"},
		{ID: "api:main.go:http_handler:POST /users:10", Label: "POST /users",
			Type: graph.NodeTypeHTTPHandler, Service: "api"},
		{ID: "api:main.go:function:CreateUser:20", Label: "CreateUser",
			Type: graph.NodeTypeFunction, Service: "api"},
		{ID: "api:s3.go:external_service:PutObject:30", Label: "PutObject",
			Type: graph.NodeTypeExternalService, Service: "api",
			Meta: map[string]string{"package": "github.com/aws/aws-sdk-go", "resolved_version": "1.55.8"}},
		{ID: "api:datastore:sqlite", Label: "sqlite",
			Type: graph.NodeTypeDatastore, Service: "api"},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: nodes[0].ID, To: nodes[1].ID, Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic},
		{ID: "e2", From: nodes[1].ID, To: nodes[2].ID, Type: graph.EdgeTypeCalls},
		{ID: "e3", From: nodes[2].ID, To: nodes[3].ID, Type: graph.EdgeTypeCloudCall},
		{ID: "e4", From: nodes[2].ID, To: nodes[4].ID, Type: graph.EdgeTypeQueries, Confidence: graph.ConfidencePartial},
	}
	return nodes, edges
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s — got output:\n%s", path, got)
	}
	assert.Equal(t, string(want), got, "output diverges from %s", path)
}

func TestMermaidFunction_Golden(t *testing.T) {
	nodes, edges := mermaidFixture()
	checkGolden(t, "mermaid_function.golden", MermaidFunction(nodes, edges))
}

func TestMermaidService_Golden(t *testing.T) {
	nodes, edges := mermaidFixture()
	checkGolden(t, "mermaid_service.golden", MermaidService(nodes, edges))
}

func TestMermaidService_AggregatesCounts(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "a1", Service: "svc-a"}, {ID: "a2", Service: "svc-a"},
		{ID: "b1", Service: "svc-b"},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: "a1", To: "b1", Type: graph.EdgeTypeHTTPCall},
		{ID: "e2", From: "a2", To: "b1", Type: graph.EdgeTypeHTTPCall},
		// same-service edge must be omitted at service level
		{ID: "e3", From: "a1", To: "a2", Type: graph.EdgeTypeCalls},
	}
	out := MermaidService(nodes, edges)
	assert.Contains(t, out, "svc_a -->|http_call x2| svc_b")
	assert.NotContains(t, out, "calls")
}

func TestExportMermaid_Endpoint(t *testing.T) {
	nodes, edges := mermaidFixture()
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	for _, e := range edges {
		idx.AddEdge(e)
	}

	srv := &Server{idx: idx, mux: http.NewServeMux()}
	srv.registerRoutes()
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	// Whole-graph service level.
	res, err := http.Get(ts.URL + "/api/export/mermaid?level=service")
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Contains(t, string(body), "flowchart LR")
	assert.Contains(t, string(body), "web -->|http_call| api")

	// Rooted function level scopes to the trace subgraph.
	root := "api:main.go:function:CreateUser:20"
	res, err = http.Get(ts.URL + "/api/export/mermaid?level=function&root=" + root + "&direction=forward&depth=5")
	require.NoError(t, err)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	assert.Equal(t, http.StatusOK, res.StatusCode)
	out := string(body)
	assert.Contains(t, out, "CreateUser")
	assert.Contains(t, out, "github.com/aws/aws-sdk-go@1.55.8", "versioned boundary nodes carry package@version")
	assert.Contains(t, out, "-.->", "partial-confidence edges render dashed")
	assert.NotContains(t, out, "POST /api/users", "backward nodes excluded in forward direction")

	// Bad level is rejected.
	res, err = http.Get(ts.URL + "/api/export/mermaid?level=nope")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusBadRequest, res.StatusCode)
}
