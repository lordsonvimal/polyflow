package pipeline_test

// FX.8.30 (2026-09-15): config_baseurl — Tier FX migration of
// internal/linker/config_baseurl.go's ResolveConfigBaseURLPaths (Tier CB),
// replaced by patterns/generic/config_baseurl.yaml + rules/generic/
// config_baseurl.dl, driven by the "config_baseurl" hub provider
// (internal/factpipe/hub_config_baseurl.go). Genuinely cross-language (any
// HTTPClient node any Go/Ruby/JS host-resolution pass stamped), so it lives
// under patterns/generic/ rather than one language directory, and needs
// graph.Snapshot.ServicePath (a real service checkout directory for
// internal/configsrc.Load) — the reason hub.go's HubProvider signature
// grew a third svcPath parameter.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func cbActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("config_baseurl")
	if fw == nil {
		t.Fatal("config_baseurl framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

// cbFixture writes files (relative path -> contents) under a temp service
// root and returns the root.
func cbFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func cbClientNode(meta map[string]string) graph.Node {
	return graph.Node{
		ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
		File: "client.go", Line: 41, Meta: meta,
	}
}

func cbRun(t *testing.T, svcPath string, nodes []graph.Node) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(cbActive(t), nil, graph.Snapshot{Nodes: nodes, ServicePath: svcPath})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func cbApply(nodes []graph.Node, res pipeline.Result) {
	byID := make(map[string]int, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = i
	}
	for _, p := range res.Patches {
		idx, ok := byID[p.ID]
		if !ok {
			continue
		}
		n := &nodes[idx]
		if n.Meta == nil {
			n.Meta = map[string]string{}
		}
		for k, v := range p.Meta {
			n.Meta[k] = v
		}
		for _, k := range p.DeleteMeta {
			delete(n.Meta, k)
		}
	}
}

func TestConfigBaseURLRule_ComposesPrefix(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/user-apps", "env_var": "API_URL",
	})}
	res := cbRun(t, root, nodes)
	if len(res.Patches) != 1 {
		t.Fatalf("expected 1 patch, got %d: %+v", len(res.Patches), res.Patches)
	}
	cbApply(nodes, res)
	if got := nodes[0].Meta["path"]; got != "*/api/v2/user-apps" {
		t.Errorf("path = %q", got)
	}
	if got := nodes[0].Meta["path_prefix_from"]; got != "API_URL" {
		t.Errorf("path_prefix_from = %q", got)
	}
	if got := nodes[0].Meta["path_prefix_ref"]; got != ".env:1" {
		t.Errorf("path_prefix_ref = %q", got)
	}
}

func TestConfigBaseURLRule_HostEnvVarFallback(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "SERVICE_BASE_URL=https://svc-b.internal/api/v1\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/job_items/update_job_status", "host_env_var": "SERVICE_BASE_URL",
	})}
	res := cbRun(t, root, nodes)
	cbApply(nodes, res)
	if got := nodes[0].Meta["path"]; got != "*/api/v1/job_items/update_job_status" {
		t.Errorf("path = %q", got)
	}
}

func TestConfigBaseURLRule_Idempotent(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/user-apps", "env_var": "API_URL",
	})}
	cbApply(nodes, cbRun(t, root, nodes))
	res2 := cbRun(t, root, nodes)
	if len(res2.Patches) != 0 {
		t.Errorf("second run produced %d patches, want 0: %+v", len(res2.Patches), res2.Patches)
	}
}

func TestConfigBaseURLRule_PrefixAlreadyPresent(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/api/v2/user-apps", "env_var": "API_URL",
	})}
	res := cbRun(t, root, nodes)
	if len(res.Patches) != 0 {
		t.Fatalf("expected no patches, got %+v", res.Patches)
	}
}

func TestConfigBaseURLRule_PrefixGuardIsSegmentWise(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/apiv2/things", "env_var": "API_URL",
	})}
	cbApply(nodes, cbRun(t, root, nodes))
	if got := nodes[0].Meta["path"]; got != "*/api/apiv2/things" {
		t.Errorf("path = %q, want */api/apiv2/things", got)
	}
}

func TestConfigBaseURLRule_DisagreeingSourcesAbstain(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env":            "API_URL=https://svc-b.internal/api/v1\n",
		".env.production": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/user-apps", "env_var": "API_URL",
	})}
	res := cbRun(t, root, nodes)
	if len(res.Patches) != 0 {
		t.Fatalf("expected abstention, got %+v", res.Patches)
	}
}

func TestConfigBaseURLRule_NoPathComponentIsNoOp(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env.example": "TARGET_MANAGER_URL=http://localhost:3000\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/user-apps", "env_var": "TARGET_MANAGER_URL",
	})}
	res := cbRun(t, root, nodes)
	if len(res.Patches) != 0 {
		t.Fatalf("expected no patches, got %+v", res.Patches)
	}
}

func TestConfigBaseURLRule_SkipsIneligibleNodes(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	cases := []struct {
		name string
		node graph.Node
	}{
		{"key_dynamic node has no path to prefix", cbClientNode(map[string]string{
			"path": "*/user-apps", "env_var": "API_URL", "key_dynamic": "true",
		})},
		{"literal host is not composing onto a configured base", cbClientNode(map[string]string{
			"path": "/user-apps", "env_var": "API_URL",
		})},
		{"no env var traced", cbClientNode(map[string]string{
			"path": "*/user-apps",
		})},
		{"env var absent from config", cbClientNode(map[string]string{
			"path": "*/user-apps", "env_var": "UNSET_URL",
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes := []graph.Node{c.node}
			if res := cbRun(t, root, nodes); len(res.Patches) != 0 {
				t.Fatalf("expected no patches, got %+v", res.Patches)
			}
		})
	}
}

func TestConfigBaseURLRule_SkipsNonHTTPClient(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	n := cbClientNode(map[string]string{"path": "*/user-apps", "env_var": "API_URL"})
	n.Type = graph.NodeTypePublisher
	if res := cbRun(t, root, []graph.Node{n}); len(res.Patches) != 0 {
		t.Fatalf("expected no patches, got %+v", res.Patches)
	}
}

func TestConfigBaseURLRule_RegradesWeakEvidence(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/user-apps", "env_var": "API_URL",
		"path_evidence": graph.PathEvidenceWeak, "confidence_ceiling": graph.ConfidencePartial,
	})}
	cbApply(nodes, cbRun(t, root, nodes))
	if got := nodes[0].Meta["path"]; got != "*/api/v2/user-apps" {
		t.Fatalf("path = %q", got)
	}
	if _, ok := nodes[0].Meta["path_evidence"]; ok {
		t.Error("path_evidence still present, want cleared")
	}
	if _, ok := nodes[0].Meta["confidence_ceiling"]; ok {
		t.Error("confidence_ceiling still present, want cleared")
	}
}

func TestConfigBaseURLRule_KeepsWeakEvidenceWhenStillWeak(t *testing.T) {
	root := cbFixture(t, map[string]string{
		".env": "API_URL=https://svc-b.internal/:tenant\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/*", "env_var": "API_URL",
		"path_evidence": graph.PathEvidenceWeak, "confidence_ceiling": graph.ConfidencePartial,
	})}
	cbApply(nodes, cbRun(t, root, nodes))
	if nodes[0].Meta["path_evidence"] != graph.PathEvidenceWeak {
		t.Errorf("path_evidence = %q, want kept", nodes[0].Meta["path_evidence"])
	}
	if nodes[0].Meta["confidence_ceiling"] != graph.ConfidencePartial {
		t.Errorf("confidence_ceiling = %q, want kept", nodes[0].Meta["confidence_ceiling"])
	}
}

func TestConfigBaseURLRule_ReadsK8sAndTerraform(t *testing.T) {
	root := cbFixture(t, map[string]string{
		"k8s/deploy.yaml": "spec:\n  template:\n    spec:\n      containers:\n" +
			"        - name: api\n          env:\n            - name: K8S_URL\n" +
			"              value: \"https://svc-b.internal/k8s/v1\"\n",
		"terraform/prod.tfvars": "TF_URL = \"https://svc-b.internal/tf/v1\"\n",
	})
	nodes := []graph.Node{
		{ID: "k", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"path": "*/things", "env_var": "K8S_URL"}},
		{ID: "t", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"path": "*/things", "env_var": "TF_URL"}},
	}
	res := cbRun(t, root, nodes)
	cbApply(nodes, res)
	if len(res.Patches) != 2 {
		t.Fatalf("expected 2 patches, got %d: %+v", len(res.Patches), res.Patches)
	}
	if got := nodes[0].Meta["path"]; got != "*/k8s/v1/things" {
		t.Errorf("k8s path = %q", got)
	}
	if got := nodes[1].Meta["path"]; got != "*/tf/v1/things" {
		t.Errorf("terraform path = %q", got)
	}
}

func TestConfigBaseURLRule_ReadsShellExport(t *testing.T) {
	root := cbFixture(t, map[string]string{
		"deploy.sh": "export MYSYCAMORE_API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{cbClientNode(map[string]string{
		"path": "*/users", "env_var": "MYSYCAMORE_API_URL",
	})}
	res := cbRun(t, root, nodes)
	cbApply(nodes, res)
	if got := nodes[0].Meta["path"]; got != "*/api/v2/users" {
		t.Errorf("path = %q", got)
	}
	if got := nodes[0].Meta["path_prefix_ref"]; got != "deploy.sh:1" {
		t.Errorf("path_prefix_ref = %q", got)
	}
}
