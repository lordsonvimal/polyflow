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

func TestApplyHints_NilMetaWithMatchingURL(t *testing.T) {
	// Client node with nil Meta but a URL that matches the hint — ensureMeta
	// must initialise the map rather than panic on nil assignment.
	links := []workspace.Link{
		{From: "frontend", To: "backend", Hint: "SVC_URL=http://backend"},
	}
	nodes := []graph.Node{
		{
			ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			// Meta is nil — ensureMeta is called when the URL matches.
			Meta: map[string]string{"url": "http://backend/api/users"},
		},
	}
	result := ApplyHints(links, nodes, nil)
	if got := result[0].Meta["target_service"]; got != "backend" {
		t.Errorf("target_service = %q, want backend", got)
	}
}

func TestApplyHints_NilMetaNoMatch(t *testing.T) {
	// Node with nil Meta — no matching hint, must not panic.
	links := []workspace.Link{
		{From: "frontend", To: "backend", Hint: "SVC_URL=http://backend"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient, Meta: nil},
	}
	result := ApplyHints(links, nodes, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 node")
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

// J.2a/J.2c: `polyflow init` writes a value-less env-var hint. It must act as a
// service allowlist for clients whose base URL was traced to that env var.
func TestApplyHints_BareEnvVarName(t *testing.T) {
	links := []workspace.Link{
		{From: "migrator", To: "nextGen", Hint: "CDR_API_BASE_URL"},
	}
	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "migrator",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "*/client_api/v1/files", "env_var": "CDR_API_BASE_URL"},
		},
		{
			ID:      "c2",
			Service: "migrator",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "*/api/v1/users", "env_var": "MYSYCAMORE_API_URL"},
		},
		{
			ID:      "c3",
			Service: "migrator",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "*/health"},
		},
		{
			ID:      "c4", // right env var, wrong source service
			Service: "other",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "*/x", "env_var": "CDR_API_BASE_URL"},
		},
	}

	result := ApplyHints(links, nodes, nil)

	if got := result[0].Meta["target_service"]; got != "nextGen" {
		t.Errorf("matching env_var: target_service = %q, want nextGen", got)
	}
	if got := result[1].Meta["target_service"]; got != "" {
		t.Errorf("different env_var: target_service = %q, want empty", got)
	}
	if got := result[2].Meta["target_service"]; got != "" {
		t.Errorf("no env_var: target_service = %q, want empty", got)
	}
	if got := result[3].Meta["target_service"]; got != "" {
		t.Errorf("other service: target_service = %q, want empty", got)
	}
}

// Tier L stamps host_env_var on Ruby clients; it means the same thing.
func TestApplyHints_BareEnvVarName_RubyHostEnvVar(t *testing.T) {
	links := []workspace.Link{
		{From: "web", To: "sce", Hint: "SCE_HOST"},
	}
	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "web",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "POST", "path": "/jobs", "host_env_var": "SCE_HOST"},
		},
	}
	if got := ApplyHints(links, nodes, nil)[0].Meta["target_service"]; got != "sce" {
		t.Errorf("target_service = %q, want sce", got)
	}
}

// A base_url is direct evidence of the target; an env-var name is an inference
// from deploy config. When both rules fire, base_url wins.
func TestApplyHints_EnvVarDoesNotOverrideBaseURL(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
		{From: "frontend", To: "other-svc", Hint: "OTHER_URL"},
	}
	nodes := []graph.Node{
		{
			ID:      "c1",
			Service: "frontend",
			Type:    graph.NodeTypeHTTPClient,
			Meta:    map[string]string{"method": "GET", "path": "/api/users", "env_var": "OTHER_URL"},
		},
	}

	result := ApplyHints(links, nodes, nil)

	if got := result[0].Meta["target_service"]; got != "backend" {
		t.Errorf("target_service = %q, want backend (base_url must win)", got)
	}
	if got := result[0].Meta["path"]; got != "/users" {
		t.Errorf("path = %q, want /users", got)
	}
}
