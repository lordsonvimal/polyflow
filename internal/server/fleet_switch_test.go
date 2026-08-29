package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// TestHandleFleetServices_NoFleet_ReportsEmpty is the default (non-fleet)
// case: SetFleet was never called, so the GR.6 endpoints report "no fleet"
// rather than an error — fleet mode is a bonus, not a requirement.
func TestHandleFleetServices_NoFleet_ReportsEmpty(t *testing.T) {
	srv := buildTestServer(t, nil, nil)

	req := httptest.NewRequest("GET", "/api/fleet/services", nil)
	w := httptest.NewRecorder()
	srv.handleFleetServices(w, req)

	var body struct {
		Services []fleetMemberRow `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Services) != 0 {
		t.Fatalf("expected no services, got %+v", body.Services)
	}
}

// TestHandleFleetActive_NoFleet_ReturnsUnavailable proves POST
// /api/fleet/active never panics or 500s on a plain (non-fleet) workspace.
func TestHandleFleetActive_NoFleet_ReturnsUnavailable(t *testing.T) {
	srv := buildTestServer(t, nil, nil)

	req := httptest.NewRequest("POST", "/api/fleet/active", strings.NewReader(`{"service":"api"}`))
	w := httptest.NewRecorder()
	srv.handleFleetActive(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// TestFleetMerge_WidensNotSwitches is GR.6's (revised) core acceptance
// shape at the handler level (no real git/clone plumbing, which
// cmd/polyflow owns via FleetMergeFunc/FleetEnsureFunc): a member ensured
// via POST /api/fleet/active is ADDED to the fleet-wide view, not swapped
// in place of the one already there — the whole fleet stays browsable
// together. Node-source reads for the newly-ensured member's node resolve
// against its own checkout root, not the test process's CWD.
func TestFleetMerge_WidensNotSwitches(t *testing.T) {
	apiNode := &graph.Node{ID: "api:n1", Type: graph.NodeTypeFunction, Label: "apiFn", Service: "api", File: "main.go", Line: 1}
	srv := buildTestServer(t, []*graph.Node{apiNode}, nil)

	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.js"), []byte("console.log(1)\n"), 0o644); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	webNode := &graph.Node{ID: "web:n1", Type: graph.NodeTypeFunction, Label: "webFn", Service: "web", File: "index.js", Line: 1}

	webResolved := false
	mergeFn := func(ctx context.Context) (*graph.AdjacencyIndex, map[string]string, map[string]*semantic.Searcher, []string, []graph.UnresolvedRef, error) {
		idx := graph.NewAdjacencyIndex()
		idx.AddNode(apiNode)
		roots := map[string]string{"api": ""}
		resolved := []string{"api"}
		if webResolved {
			idx.AddNode(webNode)
			roots["web"] = webRoot
			resolved = append(resolved, "web")
		}
		return idx, roots, nil, resolved, nil, nil
	}
	ensureFn := func(ctx context.Context, service string) error {
		if service == "web" {
			webResolved = true
		}
		return nil
	}
	srv.SetFleet(mergeFn, ensureFn, []string{"api", "web"})
	if err := srv.RefreshFleet(context.Background()); err != nil {
		t.Fatalf("initial RefreshFleet: %v", err)
	}

	// Services list: "api" already resolved (active), "web" not yet.
	req := httptest.NewRequest("GET", "/api/fleet/services", nil)
	w := httptest.NewRecorder()
	srv.handleFleetServices(w, req)
	var body struct {
		Services []fleetMemberRow `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode services: %v", err)
	}
	byName := map[string]bool{}
	for _, s := range body.Services {
		byName[s.Service] = s.Active
	}
	if !byName["api"] || byName["web"] {
		t.Fatalf("expected api active, web not yet resolved: %+v", body.Services)
	}

	// Ensure "web" via POST /api/fleet/active.
	req = httptest.NewRequest("POST", "/api/fleet/active", strings.NewReader(`{"service":"web"}`))
	w = httptest.NewRecorder()
	srv.handleFleetActive(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ensure web: %d: %s", w.Code, w.Body.String())
	}

	// Both members are now active — widened, not switched.
	req = httptest.NewRequest("GET", "/api/fleet/services", nil)
	w = httptest.NewRecorder()
	srv.handleFleetServices(w, req)
	body.Services = nil
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode services after ensure: %v", err)
	}
	byName = map[string]bool{}
	for _, s := range body.Services {
		byName[s.Service] = s.Active
	}
	if !byName["api"] || !byName["web"] {
		t.Fatalf("expected both api and web active after ensure: %+v", body.Services)
	}

	// api's node is still reachable — the merge widened, it didn't drop it.
	req = httptest.NewRequest("GET", "/api/node/api:n1", nil)
	req.SetPathValue("id", "api:n1")
	w = httptest.NewRecorder()
	srv.handleNode(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("api node still reachable: %d: %s", w.Code, w.Body.String())
	}

	// Node source for the newly-ensured "web" node resolves relative to
	// webRoot, not the test process's CWD.
	req = httptest.NewRequest("GET", "/api/node/web:n1/source", nil)
	req.SetPathValue("id", "web:n1")
	w = httptest.NewRecorder()
	srv.handleNodeSource(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("node source: %d: %s", w.Code, w.Body.String())
	}
	var src struct {
		Source string `json:"source"`
	}
	if err := json.NewDecoder(w.Body).Decode(&src); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if src.Source != "console.log(1)\n" {
		t.Fatalf("unexpected source: %q", src.Source)
	}
}
