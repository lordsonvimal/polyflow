package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deadcodeNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPHandler, Label: "handleLogin", Service: "auth", File: "auth/handler.go", Line: 20, Language: "go"},
		{ID: "n2", Type: graph.NodeTypeFunction, Label: "usedHelper", Service: "auth", File: "auth/helper.go", Line: 5, Language: "go"},
		{ID: "n3", Type: graph.NodeTypeFunction, Label: "orphanHelper", Service: "auth", File: "auth/orphan.go", Line: 8, Language: "go"},
	}
}

func deadcodeEdges() []*graph.Edge {
	return []*graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls},
	}
}

func TestHandleDeadcode_OK(t *testing.T) {
	srv := buildTestServer(t, deadcodeNodes(), deadcodeEdges())
	req := httptest.NewRequest("GET", "/api/deadcode", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp deadcode.Result
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Functions[0].ID != "n3" {
		t.Fatalf("want single orphan n3, got %+v", resp.Functions)
	}
}

func TestHandleDeadcode_ServiceFilter(t *testing.T) {
	nodes := deadcodeNodes()
	nodes = append(nodes, &graph.Node{ID: "n4", Type: graph.NodeTypeFunction, Label: "otherOrphan", Service: "billing", File: "billing/orphan.go", Line: 3, Language: "go"})
	srv := buildTestServer(t, nodes, deadcodeEdges())

	req := httptest.NewRequest("GET", "/api/deadcode?service=billing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp deadcode.Result
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Functions[0].ID != "n4" {
		t.Fatalf("want single orphan n4, got %+v", resp.Functions)
	}
}

// TestUnresolvedRefs_NonFleetMode_UsesLocalStore is the default (SetFleet
// never called) case: UnresolvedRefs falls back to this workspace's own
// store, the single-store behavior GET /api/deadcode had before fleet mode
// existed.
func TestUnresolvedRefs_NonFleetMode_UsesLocalStore(t *testing.T) {
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	local := graph.UnresolvedRef{Service: "backend", File: "app.rb", Line: 1, Name: "x", Kind: "call_ref"}
	require.NoError(t, store.UpsertUnresolvedRefs(context.Background(), []graph.UnresolvedRef{local}))

	srv := New(store, graph.NewAdjacencyIndex())
	got, err := srv.UnresolvedRefs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []graph.UnresolvedRef{local}, got)
}

// TestUnresolvedRefs_FleetMode_UsesFleetMergedLedger pins the DC.27
// structural-gap fix: once RefreshFleet has run, UnresolvedRefs must return
// the fleet-merged ledger FleetMergeFunc produced — not fall back to the
// local store's own ledger, which a fleet-wide deadcode scan needs a
// member's own erb_render_dynamic/erb_render_unresolved rows beyond.
func TestUnresolvedRefs_FleetMode_UsesFleetMergedLedger(t *testing.T) {
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	local := graph.UnresolvedRef{Service: "backend", File: "app.rb", Line: 1, Name: "local_only", Kind: "call_ref"}
	require.NoError(t, store.UpsertUnresolvedRefs(context.Background(), []graph.UnresolvedRef{local}))

	srv := New(store, graph.NewAdjacencyIndex())

	fleetRefs := []graph.UnresolvedRef{
		{Service: "backend", File: "app/views/x/_y.html.erb", Line: 2, Name: `"z/#{obj}"`, Kind: "erb_render_dynamic"},
		{Service: "web", File: "app/views/w/_v.html.erb", Line: 3, Name: "w/v", Kind: "erb_render_unresolved"},
	}
	mergeFn := func(ctx context.Context) (*graph.AdjacencyIndex, map[string]string, map[string]*semantic.Searcher, []string, []graph.UnresolvedRef, error) {
		return graph.NewAdjacencyIndex(), map[string]string{}, nil, []string{"backend", "web"}, fleetRefs, nil
	}
	srv.SetFleet(mergeFn, func(ctx context.Context, service string) error { return nil }, []string{"backend", "web"})
	require.NoError(t, srv.RefreshFleet(context.Background()))

	got, err := srv.UnresolvedRefs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, fleetRefs, got, "fleet mode must return the fleet-merged ledger, not the local store's own")
}
