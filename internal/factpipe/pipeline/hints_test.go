package pipeline_test

// FX.8.31 (2026-09-16): hints — Tier FX migration of
// internal/linker/hints.go's retired ApplyHints (Tier JH / J.2 / J.2a /
// J.2b / J.2c), replaced by patterns/generic/hints.yaml +
// rules/generic/hints.dl, driven by the "hints" hub provider
// (internal/factpipe/hub_hints.go). Ports the retired Go test's 15 cases
// verbatim, merging pipeline.Result.Patches onto a node copy the same way
// internal/indexer/link_passes.go's apply_hints_and_enrich pass does, so
// assertions read Meta exactly like the retired test did.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func hintsApply(t *testing.T, links []workspace.Link, nodes []graph.Node) []graph.Node {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("hints")
	if fw == nil {
		t.Fatal("hints framework not embedded")
	}
	linkHints := make([]graph.LinkHint, len(links))
	for i, l := range links {
		linkHints[i] = graph.LinkHint{From: l.From, To: l.To, BaseURL: l.BaseURL, Hint: l.Hint}
	}
	res, err := pipeline.Run([]*pipeline.Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Links: linkHints})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := make([]graph.Node, len(nodes))
	copy(out, nodes)
	byID := make(map[string]int, len(out))
	for i := range out {
		byID[out[i].ID] = i
	}
	for _, p := range res.Patches {
		idx, ok := byID[p.ID]
		if !ok {
			continue
		}
		n := out[idx]
		m := make(map[string]string, len(n.Meta)+len(p.Meta))
		for k, v := range n.Meta {
			m[k] = v
		}
		for k, v := range p.Meta {
			m[k] = v
		}
		n.Meta = m
		out[idx] = n
	}
	return out
}

func TestHintsRule_BaseURL(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "/api/users", "target_service": "backend"}},
		{ID: "c2", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "POST", "path": "/other/endpoint", "target_service": "backend"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["path"]; got != "/users" {
		t.Errorf("path after base_url strip = %q, want /users", got)
	}
	if got := result[1].Meta["path"]; got != "/other/endpoint" {
		t.Errorf("unmatched path = %q, want /other/endpoint", got)
	}
}

func TestHintsRule_EnvVarHint(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "user-svc", Hint: "USER_SVC_URL=http://user-service:8080"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "/users", "url": "http://user-service:8080/users"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["target_service"]; got != "user-svc" {
		t.Errorf("target_service = %q, want user-svc", got)
	}
}

func TestHintsRule_NilLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "svc", Type: graph.NodeTypeHTTPClient, Meta: map[string]string{"path": "/foo"}},
	}
	result := hintsApply(t, nil, nodes)
	if len(result) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result))
	}
	if result[0].Meta["path"] != "/foo" {
		t.Errorf("path should be unchanged, got %q", result[0].Meta["path"])
	}
}

func TestHintsRule_EmptyLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "svc", Type: graph.NodeTypeHTTPClient, Meta: map[string]string{"path": "/foo"}},
	}
	result := hintsApply(t, []workspace.Link{}, nodes)
	if result[0].Meta["path"] != "/foo" {
		t.Errorf("path should be unchanged, got %q", result[0].Meta["path"])
	}
}

func TestHintsRule_NonClientNodesUnchanged(t *testing.T) {
	links := []workspace.Link{
		{From: "svc-a", To: "svc-b", BaseURL: "/api"},
	}
	nodes := []graph.Node{
		{ID: "h1", Service: "svc-b", Type: graph.NodeTypeHTTPHandler, Meta: map[string]string{"path": "/api/users"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["path"]; got != "/api/users" {
		t.Errorf("handler path was unexpectedly modified to %q", got)
	}
}

func TestHintsRule_NilMetaWithMatchingURL(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", Hint: "SVC_URL=http://backend"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"url": "http://backend/api/users"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["target_service"]; got != "backend" {
		t.Errorf("target_service = %q, want backend", got)
	}
}

func TestHintsRule_NilMetaNoMatch(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", Hint: "SVC_URL=http://backend"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient, Meta: nil},
	}
	result := hintsApply(t, links, nodes)
	if len(result) != 1 {
		t.Fatalf("expected 1 node")
	}
}

func TestHintsRule_BaseURLStripsToRoot(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "/api", "target_service": "backend"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["path"]; got != "/" {
		t.Errorf("path after stripping full prefix = %q, want /", got)
	}
}

func TestHintsRule_BareEnvVarName(t *testing.T) {
	links := []workspace.Link{
		{From: "migrator", To: "orion", Hint: "VEGA_API_BASE_URL"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "migrator", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "*/client_api/v1/files", "env_var": "VEGA_API_BASE_URL"}},
		{ID: "c2", Service: "migrator", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "*/api/v1/users", "env_var": "MYSYCAMORE_API_URL"}},
		{ID: "c3", Service: "migrator", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "*/health"}},
		{ID: "c4", Service: "other", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "*/x", "env_var": "VEGA_API_BASE_URL"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["target_service"]; got != "orion" {
		t.Errorf("matching env_var: target_service = %q, want orion", got)
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

func TestHintsRule_BareEnvVarName_RubyHostEnvVar(t *testing.T) {
	links := []workspace.Link{
		{From: "web", To: "lyra", Hint: "LYRA_HOST"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "web", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "POST", "path": "/jobs", "host_env_var": "LYRA_HOST"}},
	}
	if got := hintsApply(t, links, nodes)[0].Meta["target_service"]; got != "lyra" {
		t.Errorf("target_service = %q, want lyra", got)
	}
}

func TestHintsRule_HostDefaultLiteralUnclaimed(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "orion-atlas", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "*/api/v1/users", "host_default_literal": "https://atlas-dev.example.internal"}},
	}
	result := hintsApply(t, nil, nodes)
	if got := result[0].Meta["key_dynamic"]; got != "true" {
		t.Errorf("unclaimed host_default_literal: key_dynamic = %q, want true", got)
	}
	if got := result[0].Meta["target_service"]; got != "" {
		t.Errorf("unclaimed host_default_literal: target_service = %q, want empty", got)
	}
}

func TestHintsRule_HostDefaultLiteralClaimed(t *testing.T) {
	links := []workspace.Link{
		{From: "orion-atlas", To: "atlas-backend", Hint: "ATLAS_DOMAIN=https://atlas-dev.example.internal"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "orion-atlas", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "*/api/v1/users", "host_default_literal": "https://atlas-dev.example.internal"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["target_service"]; got != "atlas-backend" {
		t.Errorf("claimed host_default_literal: target_service = %q, want atlas-backend", got)
	}
	if got := result[0].Meta["key_dynamic"]; got != "" {
		t.Errorf("claimed host_default_literal: key_dynamic = %q, want empty", got)
	}
}

func TestHintsRule_HostDefaultLiteralSkipsAttributed(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Service: "svc", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "*/api/v1/users", "host_default_literal": "https://external.example.com", "target_service": "already-set"}},
		{ID: "c2", Service: "svc", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"path": "*/api/v1/users", "host_default_literal": "https://external.example.com", "key_dynamic": "true"}},
	}
	result := hintsApply(t, nil, nodes)
	if got := result[0].Meta["key_dynamic"]; got != "" {
		t.Errorf("already-attributed: key_dynamic unexpectedly set to %q", got)
	}
	if got := result[1].Meta["target_service"]; got != "" {
		t.Errorf("already-dynamic: target_service unexpectedly set to %q", got)
	}
}

func TestHintsRule_EnvVarDoesNotOverrideBaseURL(t *testing.T) {
	links := []workspace.Link{
		{From: "frontend", To: "backend", BaseURL: "/api"},
		{From: "frontend", To: "other-svc", Hint: "OTHER_URL"},
	}
	nodes := []graph.Node{
		{ID: "c1", Service: "frontend", Type: graph.NodeTypeHTTPClient,
			Meta: map[string]string{"method": "GET", "path": "/api/users", "env_var": "OTHER_URL"}},
	}
	result := hintsApply(t, links, nodes)
	if got := result[0].Meta["target_service"]; got != "backend" {
		t.Errorf("target_service = %q, want backend (base_url must win)", got)
	}
	if got := result[0].Meta["path"]; got != "/users" {
		t.Errorf("path = %q, want /users", got)
	}
}
