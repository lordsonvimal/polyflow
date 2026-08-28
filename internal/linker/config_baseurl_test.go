package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeConfigFixture lays out one service directory from a path→content map and
// returns the svcPaths argument ResolveConfigBaseURLPaths takes.
func writeConfigFixture(t *testing.T, svc string, files map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return map[string]string{svc: dir}
}

// clientNode is the shape ResolveGoHTTPHosts leaves behind: a wildcard-host
// path plus the env var the base URL was traced to.
func clientNode(meta map[string]string) graph.Node {
	return graph.Node{
		ID:      "c1",
		Type:    graph.NodeTypeHTTPClient,
		Service: "svc-a",
		File:    "client.go",
		Line:    41,
		Meta:    meta,
	}
}

func TestConfigURLPathPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct{ raw, want string }{
		{"https://api.example.com/v2", "/v2"},
		{"http://host/api/v1/", "/api/v1"},
		{"https://api.example.com/v2/", "/v2"},
		{"http://localhost:3000", ""},
		{"http://localhost:3000/", ""},
		{"api.example.com/v2", ""},          // no scheme: not confidently a URL
		{"${SOME_OTHER_VAR}/api", ""},       // unexpanded interpolation
		{"%(base)s/api", ""},                // python-style interpolation
		{"amqp://guest@host/vhost", ""},     // non-HTTP scheme
		{"redis://host:6379/0", ""},         // non-HTTP scheme
		{"https:///v2", ""},                 // no host
		{"", ""},                            // empty value
		{"  https://h/a/b  ", "/a/b"},       // surrounding whitespace
		{"https://h/api/v1?x=1", "/api/v1"}, // query is not path
	}
	for _, c := range cases {
		if got := configURLPathPrefix(c.raw); got != c.want {
			t.Errorf("configURLPathPrefix(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestResolveConfigBaseURLPaths_ComposesPrefix(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/user-apps",
		"env_var": "API_URL",
	})}

	changed := ResolveConfigBaseURLPaths(nodes, svcPaths)

	if len(changed) != 1 {
		t.Fatalf("expected 1 changed node, got %d", len(changed))
	}
	if got := nodes[0].Meta["path"]; got != "*/api/v2/user-apps" {
		t.Errorf("path = %q, want %q", got, "*/api/v2/user-apps")
	}
	if got := nodes[0].Meta["path_prefix_from"]; got != "API_URL" {
		t.Errorf("path_prefix_from = %q, want API_URL", got)
	}
	if got := nodes[0].Meta["path_prefix_ref"]; got != ".env:1" {
		t.Errorf("path_prefix_ref = %q, want .env:1", got)
	}
}

func TestResolveConfigBaseURLPaths_RubyHostEnvVarFallback(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "SERVICE_BASE_URL=https://svc-b.internal/api/v1\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":         "*/job_items/update_job_status",
		"host_env_var": "SERVICE_BASE_URL",
	})}

	if len(ResolveConfigBaseURLPaths(nodes, svcPaths)) != 1 {
		t.Fatal("expected the host_env_var stamp to be honoured")
	}
	if got := nodes[0].Meta["path"]; got != "*/api/v1/job_items/update_job_status" {
		t.Errorf("path = %q", got)
	}
}

func TestResolveConfigBaseURLPaths_Idempotent(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/user-apps",
		"env_var": "API_URL",
	})}

	ResolveConfigBaseURLPaths(nodes, svcPaths)
	second := ResolveConfigBaseURLPaths(nodes, svcPaths)

	if len(second) != 0 {
		t.Errorf("second run reported %d changed nodes, want 0", len(second))
	}
	if got := nodes[0].Meta["path"]; got != "*/api/v2/user-apps" {
		t.Errorf("path doubled on re-run: %q", got)
	}
}

func TestResolveConfigBaseURLPaths_PrefixAlreadyPresent(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	// The source already spelled the full path out; nothing to compose.
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/api/v2/user-apps",
		"env_var": "API_URL",
	})}

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
		t.Fatalf("expected no change, got %d", len(got))
	}
	if _, ok := nodes[0].Meta["path_prefix_from"]; ok {
		t.Error("stamped path_prefix_from on an untouched node")
	}
}

// A prefix must match on a segment boundary: `/api` does not already prefix
// `*/apiv2/x`.
func TestResolveConfigBaseURLPaths_PrefixGuardIsSegmentWise(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/apiv2/things",
		"env_var": "API_URL",
	})}

	ResolveConfigBaseURLPaths(nodes, svcPaths)
	if got := nodes[0].Meta["path"]; got != "*/api/apiv2/things" {
		t.Errorf("path = %q, want */api/apiv2/things", got)
	}
}

func TestResolveConfigBaseURLPaths_DisagreeingSourcesAbstain(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env":            "API_URL=https://svc-b.internal/api/v1\n",
		".env.production": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/user-apps",
		"env_var": "API_URL",
	})}

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
		t.Fatalf("expected abstention, got %d changed", len(got))
	}
	if got := nodes[0].Meta["path"]; got != "*/user-apps" {
		t.Errorf("path = %q, want unchanged", got)
	}
	if _, ok := nodes[0].Meta["path_prefix_from"]; ok {
		t.Error("stamped path_prefix_from while abstaining")
	}
}

func TestResolveConfigBaseURLPaths_NoPathComponentIsNoOp(t *testing.T) {
	t.Parallel()
	// The juniper fleet's shape: every checked-in value is a bare host.
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env.example": "TARGET_MANAGER_URL=http://localhost:3000\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/user-apps",
		"env_var": "TARGET_MANAGER_URL",
	})}

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
		t.Fatalf("expected no change, got %d", len(got))
	}
	if len(nodes[0].Meta) != 2 {
		t.Errorf("stamped meta on a no-op node: %v", nodes[0].Meta)
	}
}

func TestResolveConfigBaseURLPaths_SkipsIneligibleNodes(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	cases := []struct {
		name string
		node graph.Node
	}{
		{"key_dynamic node has no path to prefix", clientNode(map[string]string{
			"path":        "*/user-apps",
			"env_var":     "API_URL",
			"key_dynamic": "true",
		})},
		{"literal host is not composing onto a configured base", clientNode(map[string]string{
			"path":    "/user-apps",
			"env_var": "API_URL",
		})},
		{"no env var traced", clientNode(map[string]string{
			"path": "*/user-apps",
		})},
		{"env var absent from config", clientNode(map[string]string{
			"path":    "*/user-apps",
			"env_var": "UNSET_URL",
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes := []graph.Node{c.node}
			before := nodes[0].Meta["path"]
			if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
				t.Fatalf("expected no change, got %d", len(got))
			}
			if nodes[0].Meta["path"] != before {
				t.Errorf("path changed to %q", nodes[0].Meta["path"])
			}
		})
	}
}

func TestResolveConfigBaseURLPaths_SkipsNonHTTPClient(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	n := clientNode(map[string]string{"path": "*/user-apps", "env_var": "API_URL"})
	n.Type = graph.NodeTypePublisher
	nodes := []graph.Node{n}

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
		t.Fatalf("expected no change, got %d", len(got))
	}
}

func TestResolveConfigBaseURLPaths_UnknownServiceIsSkipped(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-other", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/user-apps",
		"env_var": "API_URL",
	})}

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 0 {
		t.Fatalf("expected no change, got %d", len(got))
	}
}

// A weak path is suppressed by the contract engine on multi-service fan-out.
// Composing the prefix on is what makes it discriminating, so the stale stamp —
// and the ceiling that came with it — must go, or this tier suppresses the very
// edge it exists to create.
func TestResolveConfigBaseURLPaths_RegradesWeakEvidence(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":               "*/user-apps",
		"env_var":            "API_URL",
		"path_evidence":      graph.PathEvidenceWeak,
		"confidence_ceiling": graph.ConfidencePartial,
	})}

	ResolveConfigBaseURLPaths(nodes, svcPaths)

	if got := nodes[0].Meta["path"]; got != "*/api/v2/user-apps" {
		t.Fatalf("path = %q", got)
	}
	if got, ok := nodes[0].Meta["path_evidence"]; ok {
		t.Errorf("path_evidence still %q, want cleared", got)
	}
	if got, ok := nodes[0].Meta["confidence_ceiling"]; ok {
		t.Errorf("confidence_ceiling still %q, want cleared", got)
	}
}

// A prefix that adds no literal segment leaves the grading alone: one literal
// segment behind an opaque host is still weak evidence.
func TestResolveConfigBaseURLPaths_KeepsWeakEvidenceWhenStillWeak(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		".env": "API_URL=https://svc-b.internal/:tenant\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":               "*/*",
		"env_var":            "API_URL",
		"path_evidence":      graph.PathEvidenceWeak,
		"confidence_ceiling": graph.ConfidencePartial,
	})}

	ResolveConfigBaseURLPaths(nodes, svcPaths)

	if nodes[0].Meta["path_evidence"] != graph.PathEvidenceWeak {
		t.Errorf("path_evidence = %q, want it kept", nodes[0].Meta["path_evidence"])
	}
	if nodes[0].Meta["confidence_ceiling"] != graph.ConfidencePartial {
		t.Errorf("confidence_ceiling = %q, want it kept", nodes[0].Meta["confidence_ceiling"])
	}
}

func TestResolveConfigBaseURLPaths_ReadsK8sAndTerraform(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
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

	if got := ResolveConfigBaseURLPaths(nodes, svcPaths); len(got) != 2 {
		t.Fatalf("expected 2 changed nodes, got %d", len(got))
	}
	if got := nodes[0].Meta["path"]; got != "*/k8s/v1/things" {
		t.Errorf("k8s path = %q", got)
	}
	if got := nodes[1].Meta["path"]; got != "*/tf/v1/things" {
		t.Errorf("terraform path = %q", got)
	}
}

// TestResolveConfigBaseURLPaths_ReadsShellExport is SH2's integration test:
// a Go host-resolver fixture (a client node ResolveGoHTTPHosts would have
// stamped Meta["env_var"] on) whose ONLY env source in the service tree is a
// deploy.sh `export` now resolves — proving the seam (configsrc.Load merging
// in shellEnvValues), not just the extractor in isolation. Zero changes to
// this file or internal/linker/go_http_hosts.go were needed: configsrc.Load
// is the only integration point SH2 touches.
func TestResolveConfigBaseURLPaths_ReadsShellExport(t *testing.T) {
	t.Parallel()
	svcPaths := writeConfigFixture(t, "svc-a", map[string]string{
		"deploy.sh": "export MYSYCAMORE_API_URL=https://svc-b.internal/api/v2\n",
	})
	nodes := []graph.Node{clientNode(map[string]string{
		"path":    "*/users",
		"env_var": "MYSYCAMORE_API_URL",
	})}

	changed := ResolveConfigBaseURLPaths(nodes, svcPaths)

	if len(changed) != 1 {
		t.Fatalf("expected 1 changed node, got %d", len(changed))
	}
	if got := nodes[0].Meta["path"]; got != "*/api/v2/users" {
		t.Errorf("path = %q, want %q", got, "*/api/v2/users")
	}
	if got := nodes[0].Meta["path_prefix_ref"]; got != "deploy.sh:1" {
		t.Errorf("path_prefix_ref = %q, want deploy.sh:1", got)
	}
}
