package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// seamNodes/seamEdges build a channel with two consumers (rule 1) — a
// separate fixture from testNodes()/testEdges() since those have no channel.
func seamNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "p1", Type: graph.NodeTypeFunction, Label: "publish", Service: "svc-a", File: "pub.go", Line: 1},
		{ID: "ch1", Type: graph.NodeTypeChannel, Label: "orders", Service: "svc-a", Meta: map[string]string{"exchange": "orders"}},
		{ID: "s1", Type: graph.NodeTypeSubscriber, Label: "sub1", Service: "svc-b", File: "sub1.go", Line: 1},
		{ID: "s2", Type: graph.NodeTypeSubscriber, Label: "sub2", Service: "svc-c", File: "sub2.go", Line: 1},
	}
}

func seamEdges() []*graph.Edge {
	return []*graph.Edge{
		{ID: "e-p1-ch1", From: "p1", To: "ch1", Type: graph.EdgeTypePublishes, VerificationState: graph.StateVerified},
		{ID: "e-ch1-s1", From: "ch1", To: "s1", Type: graph.EdgeTypeSubscribes},
		{ID: "e-ch1-s2", From: "ch1", To: "s2", Type: graph.EdgeTypeSubscribes},
	}
}

// --- /api/flows/entrypoints ---

func TestHandleFlowsEntrypoints_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/entrypoints", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.EntrypointsResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Entrypoints) != 1 || resp.Entrypoints[0].NodeID != "n2" {
		t.Fatalf("want single entrypoint n2, got %+v", resp.Entrypoints)
	}
}

func TestHandleFlowsEntrypoints_KindFilter(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/entrypoints?kind=worker", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp graph.EntrypointsResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Entrypoints) != 0 {
		t.Fatalf("want zero entrypoints for kind=worker, got %+v", resp.Entrypoints)
	}
}

// --- /api/flows/through/{id} ---

func TestHandleFlowsThrough_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/through/n1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.FlowsThroughResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Flows) != 1 {
		t.Fatalf("want one flow through n1, got %+v", resp.Flows)
	}
	if resp.Flows[0].Entrypoint.NodeID != "n2" {
		t.Fatalf("want entrypoint n2, got %s", resp.Flows[0].Entrypoint.NodeID)
	}
	found := false
	for _, h := range resp.Flows[0].Chain {
		if h.NodeID == "n1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("chain does not pass through n1: %+v", resp.Flows[0].Chain)
	}
}

// TestHandleFlowsThrough_DeadEnd verifies that a worker entrypoint with zero
// outgoing flow edges (e.g. a goroutine whose body only calls stdlib code)
// reports dead_end=true, distinguishing "genuinely nothing downstream" from
// an unresolved link — the two states FlowLane's empty-state message needs
// to tell apart.
func TestHandleFlowsThrough_DeadEnd(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "leaf", Type: graph.NodeTypeWorker, Label: "goroutine_anon", Service: "svc", File: "x.go", Line: 1, Language: "go"},
	}
	srv := buildTestServer(t, nodes, nil)
	req := httptest.NewRequest("GET", "/api/flows/through/leaf", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.FlowsThroughResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Flows) != 0 {
		t.Fatalf("want zero flows for a dead-end worker, got %+v", resp.Flows)
	}
	if !resp.DeadEnd {
		t.Fatalf("want dead_end=true for a worker with no outgoing edges, got false")
	}
}

func TestHandleFlowsThrough_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/through/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// --- /api/flows/paths ---

func TestHandleFlowsPaths_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/paths?from=n2&to=n3", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.FlowPathsResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if !resp.Reachable || len(resp.Paths) != 1 {
		t.Fatalf("want one reachable path, got %+v", resp)
	}
}

func TestHandleFlowsPaths_Unreachable(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/paths?from=n3&to=n2", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (honest unreachable, not an error), got %d: %s", w.Code, w.Body)
	}
	var resp graph.FlowPathsResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Reachable {
		t.Fatalf("want reachable=false, got %+v", resp)
	}
}

func TestHandleFlowsPaths_MissingParams(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/paths?from=n2", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- /api/flows/refine ---

func TestHandleFlowsRefine_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/refine?waypoints=n2,n1,n3", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.RefineResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Chain) != 3 {
		t.Fatalf("want 3-hop stitched chain, got %+v", resp.Chain)
	}
}

func TestHandleFlowsRefine_Disconnected(t *testing.T) {
	nodes := append(testNodes(), &graph.Node{ID: "iso", Type: graph.NodeTypeFunction, Label: "iso", Service: "auth", File: "iso.go", Line: 1})
	srv := buildTestServer(t, nodes, testEdges())
	req := httptest.NewRequest("GET", "/api/flows/refine?waypoints=n2,iso", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleFlowsRefine_MissingParam(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/flows/refine", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- /api/seam/{id} ---

func TestHandleSeam_TwoConsumers(t *testing.T) {
	srv := buildTestServer(t, seamNodes(), seamEdges())
	req := httptest.NewRequest("GET", "/api/seam/e-p1-ch1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.SeamResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Consumers) != 2 {
		t.Fatalf("want 2 consumers (rule 1 fan-out), got %+v", resp.Consumers)
	}
	if len(resp.Producers) != 1 {
		t.Fatalf("want 1 producer, got %+v", resp.Producers)
	}
}

func TestHandleSeam_NotFound(t *testing.T) {
	srv := buildTestServer(t, seamNodes(), seamEdges())
	req := httptest.NewRequest("GET", "/api/seam/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// --- /api/services/channels ---

func TestHandleServiceChannels_OK(t *testing.T) {
	srv := buildTestServer(t, seamNodes(), seamEdges())
	req := httptest.NewRequest("GET", "/api/services/channels?from=svc-a&to=svc-b", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.ServiceChannelsResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("want 1 channel, got %+v", resp.Channels)
	}
	if resp.Channels[0].EdgeID != "e-ch1-s1" {
		t.Fatalf("want representative edge e-ch1-s1, got %s", resp.Channels[0].EdgeID)
	}
}

func TestHandleServiceChannels_MissingParams(t *testing.T) {
	srv := buildTestServer(t, seamNodes(), seamEdges())
	req := httptest.NewRequest("GET", "/api/services/channels?from=svc-a", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- /api/stack ---

func TestHandleStack_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/stack", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Services []graph.ServiceStack `json:"services"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Services) != 1 || resp.Services[0].Name != "auth" {
		t.Fatalf("want single service 'auth', got %+v", resp.Services)
	}
}

// --- /api/health ---

func TestHandleHealth_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	evalSection, ok := resp["eval"].(map[string]any)
	if !ok || evalSection["present"] != false {
		t.Fatalf("want eval.present=false (no eval/baseline.json in test fixture), got %+v", resp["eval"])
	}
	idxSection, ok := resp["index"].(map[string]any)
	if !ok || int(idxSection["nodes"].(float64)) != len(testNodes()) {
		t.Fatalf("want index.nodes=%d, got %+v", len(testNodes()), resp["index"])
	}
}

// TestHandleHealth_UnresolvedByKind asserts the health payload breaks
// unresolved refs down by kind (UO.3's Health dashboard needs the
// distribution, not just the total the payload already carried).
func TestHandleHealth_UnresolvedByKind(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	ctx := t.Context()
	if err := srv.db.(*graph.SQLiteStore).ReplaceUnresolvedRefs(ctx, []graph.UnresolvedRef{
		{Service: "auth", File: "auth/user.go", Line: 12, Name: "Unknown", Kind: "call_ref"},
		{Service: "auth", File: "auth/other.go", Line: 3, Name: "Other", Kind: "call_ref"},
		{Service: "other", File: "x.go", Line: 1, Name: "Import", Kind: "import_ref"},
	}); err != nil {
		t.Fatalf("seed unresolved refs: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if int(resp["unresolved_total"].(float64)) != 3 {
		t.Fatalf("want unresolved_total=3, got %+v", resp["unresolved_total"])
	}
	byKind, ok := resp["unresolved_by_kind"].(map[string]any)
	if !ok {
		t.Fatalf("want unresolved_by_kind map, got %+v", resp["unresolved_by_kind"])
	}
	if int(byKind["call_ref"].(float64)) != 2 || int(byKind["import_ref"].(float64)) != 1 {
		t.Fatalf("want call_ref=2 import_ref=1, got %+v", byKind)
	}
	idxSection := resp["index"].(map[string]any)
	if _, ok := idxSection["parse_error_list"]; !ok {
		t.Fatalf("want index.parse_error_list present, got %+v", idxSection)
	}
}

// --- /api/unresolved ---

func TestHandleUnresolved_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	ctx := t.Context()
	if err := srv.db.(*graph.SQLiteStore).ReplaceUnresolvedRefs(ctx, []graph.UnresolvedRef{
		{Service: "auth", File: "auth/user.go", Line: 12, Name: "Unknown", Kind: "call_ref"},
		{Service: "other", File: "x.go", Line: 1, Name: "Other", Kind: "import_ref"},
	}); err != nil {
		t.Fatalf("seed unresolved refs: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/unresolved?service=auth", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if int(resp["total"].(float64)) != 1 {
		t.Fatalf("want total=1 for service=auth filter, got %+v", resp)
	}
}

// --- /api/node/{id}/links (UF.8) ---

func TestHandleNodeLinks_Downstream(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/n2/links?direction=downstream", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.LinkExplorerResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 || resp.Rows[0].NodeID != "n1" {
		t.Fatalf("want single downstream row n1, got %+v", resp.Rows)
	}
	if resp.Total != 1 || resp.Truncated {
		t.Fatalf("want total=1 truncated=false, got total=%d truncated=%v", resp.Total, resp.Truncated)
	}
}

func TestHandleNodeLinks_Upstream(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/n1/links?direction=upstream", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp graph.LinkExplorerResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 || resp.Rows[0].NodeID != "n2" {
		t.Fatalf("want single upstream row n2, got %+v", resp.Rows)
	}
}

func TestHandleNodeLinks_DepthAndKindFilter(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/n2/links?direction=downstream&depth=2&kind=function", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp graph.LinkExplorerResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Fatalf("want total=2 (n1, n3) at depth 2, got %+v", resp.Rows)
	}
	for _, row := range resp.Rows {
		if row.NodeID == "n3" && len(row.Via) != 1 {
			t.Fatalf("want n3's via path to include n1's label, got %+v", row.Via)
		}
	}
}

func TestHandleNodeLinks_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/does-not-exist/links", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}
