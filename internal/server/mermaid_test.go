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

func TestMermaidFile_Golden(t *testing.T) {
	nodes, edges := mermaidFixture()
	// The shared fixture has no file paths; assign them for the file level.
	nodes[0].File = "web/src/app.js"
	nodes[1].File = "api/main.go"
	nodes[2].File = "api/main.go"
	nodes[3].File = "api/s3.go"
	checkGolden(t, "mermaid_file.golden", MermaidFile(nodes, edges))
}

func TestMermaidStructure_Golden(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "api:models.go:struct:User:5", Label: "User", Type: graph.NodeTypeStruct, Service: "api",
			Meta: map[string]string{"fields": `[{"name":"Name","type":"string"},{"name":"Age","type":"int"}]`}},
		{ID: "api:state.go:variable:counter:3", Label: "counter", Type: graph.NodeTypeVariable, Service: "api",
			Meta: map[string]string{"data_type": "int"}},
		{ID: "api:main.go:function:bump:10", Label: "bump", Type: graph.NodeTypeFunction, Service: "api"},
		{ID: "api:main.go:function:orphan:20", Label: "orphan", Type: graph.NodeTypeFunction, Service: "api"},
		{ID: "api:main.go:http_handler:POST /users:30", Label: "POST /users", Type: graph.NodeTypeHTTPHandler, Service: "api"},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: nodes[2].ID, To: nodes[1].ID, Type: graph.EdgeTypeWrites, Confidence: graph.ConfidenceStatic},
		{ID: "e2", From: nodes[2].ID, To: nodes[0].ID, Type: graph.EdgeTypeUsesType},
		{ID: "e3", From: nodes[1].ID, To: nodes[2].ID, Type: graph.EdgeTypeFlowsTo,
			Meta: map[string]string{"mode": "value"}},
		// http edge must be excluded from the structure view
		{ID: "e4", From: nodes[4].ID, To: nodes[2].ID, Type: graph.EdgeTypeHTTPCall},
	}
	checkGolden(t, "mermaid_structure.golden", MermaidStructure(nodes, edges))
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
