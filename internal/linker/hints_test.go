package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func TestApplyHints_BaseURL(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
	}

	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "/api/users", "target_service": "backend"},
		},
		{
			ID:      "c2",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "POST", "path": "/other/endpoint", "target_service": "backend"},
		},
	}

	result := ApplyHints(links, nodes, nil)

	if got := result[0].Meta["path"]; got != "/users" {
		t.Errorf("path after base_url strip = %q, want /users", got)
	}
	// path without matching prefix should be unchanged
	if got := result[1].Meta["path"]; got != "/other/endpoint" {
		t.Errorf("unmatched path = %q, want /other/endpoint", got)
	}
}

func TestApplyHints_EnvVarHint(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "user-svc", Hint: "USER_SVC_URL=http://user-service:8080"},
	}

	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "/users", "url": "http://user-service:8080/users"},
		},
	}

	result := ApplyHints(links, nodes, nil)

	if got := result[0].Meta["target_service"]; got != "user-svc" {
		t.Errorf("target_service = %q, want user-svc", got)
	}
}

func TestApplyHints_NilLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "svc", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "/foo"}},
	}
	result := ApplyHints(nil, nodes, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result))
	}
	if result[0].Meta["path"] != "/foo" {
		t.Errorf("path should be unchanged, got %q", result[0].Meta["path"])
	}
}

func TestApplyHints_EmptyLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "svc", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "/foo"}},
	}
	result := ApplyHints([]workspace.Link{}, nodes, nil)
	if result[0].Meta["path"] != "/foo" {
		t.Errorf("path should be unchanged, got %q", result[0].Meta["path"])
	}
}

func TestApplyHints_NonClientNodesUnchanged(t *testing.T) {
	links := []workspace.Link{
		{From: "svc-a", To: "svc-b", BaseURL: "/api"},
	}
	nodes := []graph.Node{
		{ID: "h1", Service: "svc-b", Type: graph.NodeTypeHTTPHandler,
			Meta: map[string]string{"path": "/api/users"}},
	}
	result := ApplyHints(links, nodes, nil)
	// handler path should not be modified
	if got := result[0].Meta["path"]; got != "/api/users" {
		t.Errorf("handler path was unexpectedly modified to %q", got)
	}
}

func TestApplyHints_BaseURLStripsToRoot(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
	}
	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "/api", "target_service": "backend"},
		},
	}
	result := ApplyHints(links, nodes, nil)
	if got := result[0].Meta["path"]; got != "/" {
		t.Errorf("path after stripping full prefix = %q, want /", got)
	}
}
