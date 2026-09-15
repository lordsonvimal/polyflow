package pipeline_test

// FX.8.32 (2026-09-15): file_routes — Tier FX migration of
// internal/linker/file_routes.go's SynthesizeFileRoutes (Phase M.0),
// replaced by patterns/generic/file_routes.yaml + rules/generic/
// file_routes.dl, driven by the "file_routes" hub provider
// (internal/factpipe/hub_file_routes.go). The retired Go test suite only
// covered the pure path-mapping helpers (ported same-package in
// internal/factpipe/hub_file_routes_test.go) — these are new end-to-end
// cases through pipeline.Run with a real temp-dir file tree + package.json,
// closing a gap the retired suite never had.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func frActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("file_routes")
	if fw == nil {
		t.Fatal("file_routes framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

// frFixture writes files (relative path -> contents) under a temp service
// root and returns the root plus the absolute file list.
func frFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var out []string
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, abs)
	}
	return root, out
}

func frRun(t *testing.T, svc, svcPath string, files []string) pipeline.Result {
	t.Helper()
	nodes := []graph.Node{{ID: "placeholder", Type: graph.NodeTypeService, Service: svc}}
	res, err := pipeline.Run(frActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files, ServicePath: svcPath})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// TestFileRoutesRule_NextPages: pages/about.tsx synthesizes a page route,
// pages/api/users/[id].ts synthesizes an ALL-method handler, both wired to
// their (unparsed, in this fixture) file node via component_impl.
func TestFileRoutesRule_NextPages(t *testing.T) {
	root, files := frFixture(t, map[string]string{
		"package.json":            `{"dependencies": {"next": "14.0.0"}}`,
		"pages/about.tsx":         "export default function About() {}",
		"pages/api/users/[id].ts": "export default function handler() {}",
	})
	res := frRun(t, "web", root, files)

	var route, handler *graph.Node
	for i := range res.Nodes {
		switch res.Nodes[i].Type {
		case graph.NodeTypeRoute:
			route = &res.Nodes[i]
		case graph.NodeTypeHTTPHandler:
			handler = &res.Nodes[i]
		}
	}
	if route == nil || route.Meta["path"] != "/about" || route.Meta["framework"] != "next" {
		t.Fatalf("route = %+v", route)
	}
	if handler == nil || handler.Meta["path"] != "/api/users/:id" || handler.Meta["method"] != "" {
		t.Fatalf("handler = %+v", handler)
	}
	implEdges := filterEdges(res.Edges, graph.EdgeTypeComponentImpl)
	if len(implEdges) != 2 {
		t.Fatalf("component_impl edges = %d, want 2: %+v", len(implEdges), implEdges)
	}
	var fileNodes int
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeFile {
			fileNodes++
		}
	}
	if fileNodes != 2 {
		t.Fatalf("file nodes = %d, want 2 (one per route/handler file)", fileNodes)
	}
}

// TestFileRoutesRule_NextAppVerbFunction: a route.ts exporting GET/POST
// resolves each verb to its OWN function node (not the file fallback) when
// the function was already parsed into the graph.
func TestFileRoutesRule_NextAppVerbFunction(t *testing.T) {
	root, files := frFixture(t, map[string]string{
		"package.json":       `{"dependencies": {"next": "14.0.0"}}`,
		"app/users/route.ts": "export function GET() {}\nexport function POST() {}",
	})
	svc := "web"
	// Find the route.ts path explicitly (map iteration order isn't guaranteed).
	var routeFile string
	for _, f := range files {
		if filepath.Base(f) == "route.ts" {
			routeFile = f
		}
	}
	getFn := graph.Node{ID: "fn:GET", Type: graph.NodeTypeFunction, Label: "GET", Service: svc, File: routeFile}
	postFn := graph.Node{ID: "fn:POST", Type: graph.NodeTypeFunction, Label: "POST", Service: svc, File: routeFile}

	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("file_routes")
	if fw == nil {
		t.Fatal("file_routes framework not embedded")
	}
	nodes := []graph.Node{
		{ID: "placeholder", Type: graph.NodeTypeService, Service: svc},
		getFn, postFn,
	}
	res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: files, ServicePath: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	implEdges := filterEdges(res.Edges, graph.EdgeTypeComponentImpl)
	if len(implEdges) != 2 {
		t.Fatalf("component_impl edges = %d, want 2: %+v", len(implEdges), implEdges)
	}
	targets := map[string]bool{}
	for _, e := range implEdges {
		targets[e.To] = true
	}
	if !targets["fn:GET"] || !targets["fn:POST"] {
		t.Errorf("expected edges to fn:GET and fn:POST, got targets=%v", targets)
	}
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeFile {
			t.Errorf("no file-node fallback expected when both verb functions resolved, got %+v", n)
		}
	}
}

// TestFileRoutesRule_NoDepNoOp: without "next" in package.json, no next
// convention activates even though a pages/ directory exists.
func TestFileRoutesRule_NoDepNoOp(t *testing.T) {
	root, files := frFixture(t, map[string]string{
		"package.json":    `{"dependencies": {}}`,
		"pages/about.tsx": "export default function About() {}",
	})
	res := frRun(t, "web", root, files)
	if len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Fatalf("expected no output without the next dependency, got nodes=%+v edges=%+v", res.Nodes, res.Edges)
	}
}

// TestFileRoutesRule_ParallelRouteLedgers: a Next.js app-router parallel
// route (@modal) is unmappable and must ledger, not synthesize a route.
func TestFileRoutesRule_ParallelRouteLedgers(t *testing.T) {
	root, files := frFixture(t, map[string]string{
		"package.json":              `{"dependencies": {"next": "14.0.0"}}`,
		"app/@modal/inbox/page.tsx": "export default function Inbox() {}",
	})
	res := frRun(t, "web", root, files)
	if len(res.Nodes) != 0 {
		t.Fatalf("expected no nodes for a parallel route, got %+v", res.Nodes)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "route_convention_unresolved" {
		t.Fatalf("unresolved = %+v, want one route_convention_unresolved", res.Unresolved)
	}
}
