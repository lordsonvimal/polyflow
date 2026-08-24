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

// TestFleetSwitch_ServicesListActiveFlagAndSourceResolution is GR.6's core
// acceptance shape at the handler level (no real git/clone plumbing, which
// cmd/polyflow owns via FleetSwitchFunc): the services list marks the
// active member, POST /api/fleet/active swaps db/idx, and node-source reads
// resolve against the switched-to member's checkout root rather than the
// process CWD (the gap the plan calls out for a scratch-cloned member).
func TestFleetSwitch_ServicesListActiveFlagAndSourceResolution(t *testing.T) {
	apiNodes := []*graph.Node{
		{ID: "api:n1", Type: graph.NodeTypeFunction, Label: "apiFn", Service: "api", File: "main.go", Line: 1},
	}
	srv := buildTestServer(t, apiNodes, nil)

	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.js"), []byte("console.log(1)\n"), 0o644); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	webStore, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open web store: %v", err)
	}
	t.Cleanup(func() { webStore.Close() })
	webNode := &graph.Node{ID: "web:n1", Type: graph.NodeTypeFunction, Label: "webFn", Service: "web", File: "index.js", Line: 1}
	if err := webStore.UpsertNode(context.Background(), webNode); err != nil {
		t.Fatalf("upsert web node: %v", err)
	}
	webIdx := graph.NewAdjacencyIndex()
	webIdx.AddNode(webNode)

	var switchedTo string
	srv.SetFleet(func(ctx context.Context, service string) (graph.Store, *graph.AdjacencyIndex, *semantic.Searcher, string, error) {
		switchedTo = service
		return webStore, webIdx, nil, webRoot, nil
	}, []string{"api", "web"}, "api")

	// Services list: two members, "api" active.
	req := httptest.NewRequest("GET", "/api/fleet/services", nil)
	w := httptest.NewRecorder()
	srv.handleFleetServices(w, req)
	var body struct {
		Services []fleetMemberRow `json:"services"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode services: %v", err)
	}
	if len(body.Services) != 2 {
		t.Fatalf("expected 2 members, got %+v", body.Services)
	}
	byName := map[string]bool{}
	for _, s := range body.Services {
		byName[s.Service] = s.Active
	}
	if !byName["api"] || byName["web"] {
		t.Fatalf("expected api active, web inactive: %+v", body.Services)
	}

	// Switch to "web".
	req = httptest.NewRequest("POST", "/api/fleet/active", strings.NewReader(`{"service":"web"}`))
	w = httptest.NewRecorder()
	srv.handleFleetActive(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("switch to web: %d: %s", w.Code, w.Body.String())
	}
	if switchedTo != "web" {
		t.Fatalf("expected switcher called with web, got %q", switchedTo)
	}

	// Services list now reports "web" active.
	req = httptest.NewRequest("GET", "/api/fleet/services", nil)
	w = httptest.NewRecorder()
	srv.handleFleetServices(w, req)
	body.Services = nil
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode services after switch: %v", err)
	}
	byName = map[string]bool{}
	for _, s := range body.Services {
		byName[s.Service] = s.Active
	}
	if byName["api"] || !byName["web"] {
		t.Fatalf("expected web active after switch: %+v", body.Services)
	}

	// Node source for the now-active "web" node resolves relative to
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
