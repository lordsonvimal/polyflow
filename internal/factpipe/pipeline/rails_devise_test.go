package pipeline_test

// FX.8.28 (2026-09-15): rails_devise — Tier FX migration of
// internal/linker/rails_devise.go's LinkDeviseDefaultRoutes (Phase DV.2),
// replaced by patterns/ruby/rails_devise.yaml + rules/ruby/
// rails_devise.dl, driven by the "rails_devise" hub provider
// (internal/factpipe/hub_rails_devise.go). Real file-I/O tests (temp-dir
// fixture files, porting the retired Go test's fixtures verbatim) — the
// hub derives its `svc` string from nodes[0].Service, so every fixture
// supplies one placeholder node purely to name the service.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func rdActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("rails_devise")
	if fw == nil {
		t.Fatal("rails_devise framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func rdWriteFixture(t *testing.T, routes, model string) (routesFile, modelFile string) {
	t.Helper()
	dir := t.TempDir()
	routesFile = filepath.Join(dir, "config", "routes.rb")
	modelFile = filepath.Join(dir, "app", "models", "user.rb")
	if err := os.MkdirAll(filepath.Dir(routesFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(modelFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routesFile, []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelFile, []byte(model), 0o644); err != nil {
		t.Fatal(err)
	}
	return routesFile, modelFile
}

// rdRun mirrors the real caller (internal/indexer/link_passes.go): the hub
// needs nodes[0].Service to name the service in every minted node's ID, so
// a placeholder node (never itself asserted against) supplies it.
func rdRun(t *testing.T, svc string, files []string) pipeline.Result {
	t.Helper()
	nodes := []graph.Node{{ID: "placeholder", Type: graph.NodeTypeService, Service: svc}}
	res, err := pipeline.Run(rdActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func rdRouteLabels(nodes []graph.Node) map[string]*graph.Node {
	out := map[string]*graph.Node{}
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "devise_default_route" {
			out[nodes[i].Label] = &nodes[i]
		}
	}
	return out
}

// TestRailsDeviseRule_EnabledScopesSynthesize is orion-atlas's real shape:
// `devise_for :users` with no controllers:/skip: at all, and a model
// declaring every core module except :confirmable.
func TestRailsDeviseRule_EnabledScopesSynthesize(t *testing.T) {
	routesFile, modelFile := rdWriteFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable, :recoverable,
         :rememberable, :validatable, :lockable, :jwt_authenticatable
end
`)
	res := rdRun(t, "orion-atlas", []string{routesFile, modelFile})
	got := rdRouteLabels(res.Nodes)

	for _, want := range []string{
		"POST /users/sign_in",
		"DELETE /users/sign_out",
		"POST /users",
		"PATCH /users",
		"GET /users/password/new",
		"POST /users/unlock",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q: enabled module must synthesize its scope's routes", want)
		}
	}
	for label, n := range got {
		if n.Meta["resource"] == "confirmations" {
			t.Errorf("no :confirmable in model, got %q", label)
		}
		if n.Meta["controller_module"] != "" {
			t.Errorf("%q: controller_module = %q, want empty", label, n.Meta["controller_module"])
		}
		if n.Language != "ruby" {
			t.Errorf("%q: language = %q, want ruby", label, n.Language)
		}
	}
}

// TestRailsDeviseRule_ControllersOverrideNotDuplicated: a scope named in
// controllers: is DV.1's territory — DV.2 must not also synthesize it.
func TestRailsDeviseRule_ControllersOverrideNotDuplicated(t *testing.T) {
	routesFile, modelFile := rdWriteFixture(t, `
Rails.application.routes.draw do
  devise_for :users, controllers: { sessions: "sessions" }
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable
end
`)
	res := rdRun(t, "orion", []string{routesFile, modelFile})
	got := rdRouteLabels(res.Nodes)

	if _, ok := got["POST /users/sign_in"]; ok {
		t.Error("sessions is DV.1's territory via controllers:")
	}
	if _, ok := got["POST /users"]; !ok {
		t.Error("registrations was not overridden, so DV.2 must still synthesize it")
	}
}

// TestRailsDeviseRule_SkipDropsScope: skip: removes a scope from DV.2's
// default set exactly as it does DV.1's override set.
func TestRailsDeviseRule_SkipDropsScope(t *testing.T) {
	routesFile, modelFile := rdWriteFixture(t, `
Rails.application.routes.draw do
  devise_for :users, skip: [:registrations]
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable
end
`)
	res := rdRun(t, "orion", []string{routesFile, modelFile})
	got := rdRouteLabels(res.Nodes)

	if _, ok := got["POST /users"]; ok {
		t.Error("skip: must drop registrations even though the model declares :registerable")
	}
	if _, ok := got["POST /users/sign_in"]; !ok {
		t.Error("sessions was not skipped, so it must still synthesize")
	}
}

// TestRailsDeviseRule_DisabledModuleProducesNothing: a module the model
// does not declare produces zero nodes for its scope.
func TestRailsDeviseRule_DisabledModuleProducesNothing(t *testing.T) {
	routesFile, modelFile := rdWriteFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable
end
`)
	res := rdRun(t, "orion", []string{routesFile, modelFile})
	got := rdRouteLabels(res.Nodes)

	if _, ok := got["POST /users/sign_in"]; !ok {
		t.Error("missing POST /users/sign_in")
	}
	if _, ok := got["POST /users"]; ok {
		t.Error("no :registerable declared, no registrations routes")
	}
	if _, ok := got["GET /users/password/new"]; ok {
		t.Error("no :recoverable declared, no passwords routes")
	}
}

// TestRailsDeviseRule_NoDanglingEdges: every synthesized node has no
// in-repo controller behind it, so rails_route_actions must ledger every
// single one and produce zero calls edges — never a fabricated link.
func TestRailsDeviseRule_NoDanglingEdges(t *testing.T) {
	routesFile, modelFile := rdWriteFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable, :recoverable, :confirmable, :lockable
end
`)
	res := rdRun(t, "orion", []string{routesFile, modelFile})
	if len(res.Nodes) == 0 {
		t.Fatal("expected synthesized nodes")
	}

	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	active := reg.Active([]deps.Dependency{{Name: "railties", Version: "7.1.0"}})
	rraRes, err := pipeline.Run(active, nil, graph.Snapshot{Nodes: res.Nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rraRes.Edges) != 0 {
		t.Errorf("no controller exists for any default-scope node; must never fabricate a calls edge, got %+v", rraRes.Edges)
	}
	if len(rraRes.Unresolved) != len(res.Nodes) {
		t.Fatalf("every synthesized node must be ledgered: got %d unresolved, want %d", len(rraRes.Unresolved), len(res.Nodes))
	}
	for _, u := range rraRes.Unresolved {
		if u.Kind != "rails_route_action_unresolved" {
			t.Errorf("unresolved kind = %q, want rails_route_action_unresolved", u.Kind)
		}
	}
}

// TestRailsDeviseRule_NoRoutesFileIsNoOp: a service with no
// config/routes.rb synthesizes nothing.
func TestRailsDeviseRule_NoRoutesFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "app", "models", "user.rb")
	if err := os.MkdirAll(filepath.Dir(modelFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelFile, []byte(`
class User < ApplicationRecord
  devise :database_authenticatable
end
`), 0o644); err != nil {
		t.Fatal(err)
	}
	res := rdRun(t, "svc", []string{modelFile})
	if len(res.Nodes) != 0 {
		t.Errorf("expected no nodes, got %+v", res.Nodes)
	}
}
