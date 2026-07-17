package config_resolve_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/evidence/config_resolve"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func testdataDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

// httpClientNode builds a dynamic HTTP-client producer node.
func httpClientNode(id, service, file string, line int, raw string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPClient, Service: service,
		File: file, Line: line,
		Meta: map[string]string{
			"method":          "GET",
			"key_dynamic":     "true",
			"key_dynamic_raw": raw,
		},
	}
}

// kafkaPublisherNode builds a dynamic Kafka producer node.
func kafkaPublisherNode(id, service, file string, line int, raw string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypePublisher, Service: service,
		File: file, Line: line,
		Meta: map[string]string{
			"kind":            "kafka",
			"key_dynamic":     "true",
			"key_dynamic_raw": raw,
		},
	}
}

func dynUnres(service, file, name string, line int) graph.UnresolvedRef {
	return graph.UnresolvedRef{Service: service, File: file, Line: line, Name: name, Kind: "dynamic_url"}
}

func ws(svcPath string) *workspace.WorkspaceConfig {
	return &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "svc", Path: svcPath}},
	}
}

// ── .env resolution ──────────────────────────────────────────────────────────

func TestEnv_ResolvesHTTPEdge(t *testing.T) {
	// An HTTP client node with key_dynamic_raw=os.Getenv("API_URL") resolved
	// from .env to "/api/v1" → one http_call edge with that normalized label.
	dir := filepath.Join(testdataDir(), "env")
	node := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 5)

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	// At minimum one edge for the .env value "/api/v1"
	require.NotEmpty(t, ev.Edges, "must emit at least one config edge")
	labels := edgeLabels(ev.Edges)
	assert.Contains(t, labels, "get /api/v1", "resolved .env value must become normalized label")

	for _, e := range ev.Edges {
		require.Len(t, e.Sources, 1)
		assert.Equal(t, "config", e.Sources[0].Provider)
		assert.Equal(t, graph.ConfidenceDeclared, e.Sources[0].Confidence)
		assert.NotEmpty(t, e.Sources[0].Ref)
		assert.Equal(t, graph.EdgeTypeHTTPCall, e.Type)
	}
}

// TestEnv_FanOutMultipleEnvFiles verifies that same var with different values
// per overlay produces one edge per value (bug-class rule 1: fan-out).
func TestEnv_FanOutMultipleEnvFiles(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	// API_URL is "/api/v1" in .env and "/api/v2" in .env.production → 2 edges.
	node := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 5)

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	// Two distinct resolved values → two edges
	labels := edgeLabels(ev.Edges)
	assert.Contains(t, labels, "get /api/v1")
	assert.Contains(t, labels, "get /api/v2")
	assert.GreaterOrEqual(t, len(ev.Edges), 2, "fan-out: at least one edge per environment")
}

// TestEnv_FanOut_TwoNodesSameKey verifies fan-out across nodes: when two
// dynamic nodes share the same env var, both get edges (bug-class rule 1).
func TestEnv_FanOut_TwoNodesSameKey(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	n1 := httpClientNode("c1", "svc", "a.go", 1, `os.Getenv("API_URL")`)
	n2 := httpClientNode("c2", "svc", "b.go", 2, `os.Getenv("API_URL")`)
	u1 := dynUnres("svc", "a.go", `os.Getenv("API_URL")`, 1)
	u2 := dynUnres("svc", "b.go", `os.Getenv("API_URL")`, 2)

	p := config_resolve.NewConfigProvider([]graph.Node{n1, n2}, []graph.UnresolvedRef{u1, u2})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	// Both nodes + multiple environments → at least 2 edges
	assert.GreaterOrEqual(t, len(ev.Edges), 2, "fan-out: edges for both dynamic nodes")
}

// TestEnv_KafkaEdge verifies a dynamic Kafka topic resolves to a kafka_publish edge.
func TestEnv_KafkaEdge(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	node := kafkaPublisherNode("p1", "svc", "publisher.go", 10, `os.Getenv("TOPIC_NAME")`)
	unr := graph.UnresolvedRef{Service: "svc", File: "publisher.go", Line: 10,
		Name: `os.Getenv("TOPIC_NAME")`, Kind: "dynamic_topic"}

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	require.NotEmpty(t, ev.Edges)
	labels := edgeLabels(ev.Edges)
	assert.Contains(t, labels, "orders.created")
	assert.Equal(t, graph.EdgeTypeKafkaPublish, ev.Edges[0].Type)
}

// ── k8s resolution ───────────────────────────────────────────────────────────

func TestK8s_ResolvesEnvVar(t *testing.T) {
	// The testdata/k8s fixture lives at testdataDir()/k8s/deployment.yaml.
	// We create a temp dir, symlink/copy the k8s subdir into the expected
	// location so the provider's default k8s subdir search finds it.
	dir := t.TempDir()
	k8sDir := filepath.Join(dir, "k8s")
	require.NoError(t, os.MkdirAll(k8sDir, 0o755))
	src := filepath.Join(testdataDir(), "k8s", "deployment.yaml")
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(k8sDir, "deployment.yaml"), data, 0o644))

	node := httpClientNode("c1", "svc", "client.go", 3, `os.Getenv("K8S_API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("K8S_API_URL")`, 3)
	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	labels := edgeLabels(ev.Edges)
	assert.Contains(t, labels, "get /k8s/api/v1")
}

// ── Terraform resolution ──────────────────────────────────────────────────────

func TestTerraform_ResolvesEnvVar(t *testing.T) {
	dir := t.TempDir()
	tfDir := filepath.Join(dir, "terraform")
	require.NoError(t, os.MkdirAll(tfDir, 0o755))
	src := filepath.Join(testdataDir(), "terraform", "prod.tfvars")
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tfDir, "prod.tfvars"), data, 0o644))

	node := httpClientNode("c1", "svc", "client.go", 7, `os.Getenv("TF_API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("TF_API_URL")`, 7)
	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	labels := edgeLabels(ev.Edges)
	assert.Contains(t, labels, "get /tf/api/v1")
}

// ── ClearsUnresolved (ledger clearing) ───────────────────────────────────────

func TestClearsUnresolved_ResolvedEntry(t *testing.T) {
	// A successfully resolved dynamic node must appear in ClearsUnresolved so
	// the original dynamic_url ledger entry is removed by the reconciler.
	dir := filepath.Join(testdataDir(), "env")
	node := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 5)

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	require.NotEmpty(t, ev.ClearsUnresolved, "resolved entry must be in ClearsUnresolved")
	found := false
	for _, c := range ev.ClearsUnresolved {
		if c.Name == `os.Getenv("API_URL")` && c.Service == "svc" {
			found = true
		}
	}
	assert.True(t, found, "ClearsUnresolved must name the resolved dynamic ref")
}

func TestClearsUnresolved_UnresolvedEntry(t *testing.T) {
	// An env var not in any config file → config_not_found ledger + still in
	// ClearsUnresolved (replaces dynamic_url, not duplicates it).
	dir := t.TempDir()
	node := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("MISSING_VAR")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("MISSING_VAR")`, 5)

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	assert.Empty(t, ev.Edges, "no edge when var is not in config")
	require.NotEmpty(t, ev.Unresolved, "must emit config_not_found")
	assert.Equal(t, "config_not_found", ev.Unresolved[0].Kind)
	require.NotEmpty(t, ev.ClearsUnresolved, "must clear the original dynamic entry")
}

// ── Negative: no config files → graceful degradation ─────────────────────────

func TestConfigProvider_NoConfigFiles(t *testing.T) {
	dir := t.TempDir()
	node := httpClientNode("c1", "svc", "client.go", 1, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 1)

	p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)

	assert.Empty(t, ev.Edges, "no edges without config files")
	require.NotEmpty(t, ev.Unresolved)
	assert.Equal(t, "config_not_found", ev.Unresolved[0].Kind)
}

// TestConfigProvider_NoDynamicNodes: no dynamic nodes → empty evidence, no error.
func TestConfigProvider_NoDynamicNodes(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	nodes := []graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPHandler, Service: "svc",
			Meta: map[string]string{"method": "GET", "path": "/api"}},
	}
	p := config_resolve.NewConfigProvider(nodes, nil)
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)
	assert.Empty(t, ev.Edges)
	assert.Empty(t, ev.Unresolved)
}

// TestConfigProvider_NilWorkspace: nil workspace → empty evidence, no error.
func TestConfigProvider_NilWorkspace(t *testing.T) {
	p := config_resolve.NewConfigProvider(nil, nil)
	ev, err := p.Collect(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges)
}

// TestConfigProvider_UnextractableVar: a variable whose name cannot be extracted
// (non-env expression like a member access) → config_not_found.
func TestConfigProvider_UnextractableVar(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	// "cfg.APIUrl" is a member expression — not extractable as an env var name.
	node := httpClientNode("c1", "svc", "x.go", 1, "cfg.APIUrl")
	p := config_resolve.NewConfigProvider([]graph.Node{node}, nil)
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "non-env expressions cannot be resolved from config files")
}

// ── Quote stripping (bug-class rule 6) ───────────────────────────────────────

func TestEnv_QuoteStripping(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(`
API_URL="/api/quoted"
`), 0o644))
	node := httpClientNode("c1", "svc", "c.go", 1, `os.Getenv("API_URL")`)
	p := config_resolve.NewConfigProvider([]graph.Node{node}, nil)
	ev, err := p.Collect(context.Background(), ws(dir))
	require.NoError(t, err)
	require.NotEmpty(t, ev.Edges)
	assert.Equal(t, "get /api/quoted", ev.Edges[0].Label, "quotes must be stripped from value")
}

// ── Determinism (bug-class rule 2) ───────────────────────────────────────────

func TestConfigProvider_Determinism(t *testing.T) {
	dir := filepath.Join(testdataDir(), "env")
	node := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 5)

	collect := func() []byte {
		p := config_resolve.NewConfigProvider([]graph.Node{node}, []graph.UnresolvedRef{unr})
		ev, err := p.Collect(context.Background(), ws(dir))
		require.NoError(t, err)
		b, err := json.Marshal(ev.Edges)
		require.NoError(t, err)
		return b
	}
	assert.Equal(t, collect(), collect(), "output must be byte-identical across runs")
}

// ── Reconciler integration: config verifies a static dynamic edge ─────────────

func TestReconciler_ConfigVerifiesStaticEdge(t *testing.T) {
	// A static edge that was unresolved (dynamic key) gets verified when the
	// config provider resolves its URL from an env file.
	dir := filepath.Join(testdataDir(), "env")
	svcWS := ws(dir)

	// Dynamic producer node.
	dynNode := httpClientNode("c1", "svc", "client.go", 5, `os.Getenv("API_URL")`)
	unr := dynUnres("svc", "client.go", `os.Getenv("API_URL")`, 5)

	// Assume the static pipeline also produced an edge for /api/v1 via a
	// different call site (same channel key after normalization).
	staticEdge := graph.Edge{
		ID: "e-static", From: "c1", To: "handler",
		Type:       graph.EdgeTypeHTTPCall,
		Label:      "get /api/v1",
		Confidence: graph.ConfidenceInferred,
	}

	sp := evidence.NewStaticProvider(
		[]graph.Node{dynNode},
		[]graph.Edge{staticEdge},
		[]graph.UnresolvedRef{unr},
	)
	cfgProv := config_resolve.NewConfigProvider([]graph.Node{dynNode}, []graph.UnresolvedRef{unr})

	rec, err := evidence.NewReconciler(sp, cfgProv)
	require.NoError(t, err)
	result, err := rec.Reconcile(context.Background(), svcWS)
	require.NoError(t, err)

	found := false
	for _, e := range result.Edges {
		if e.ID == "e-static" {
			found = true
			assert.Equal(t, graph.StateVerified, e.VerificationState,
				"static edge with matching config key must be verified")
		}
	}
	assert.True(t, found, "static edge must survive reconciliation")

	// The dynamic_url unresolved ref must be gone (cleared by config provider).
	for _, u := range result.Unresolved {
		if u.Kind == "dynamic_url" && strings.Contains(u.Name, "API_URL") {
			t.Errorf("dynamic_url entry was not cleared: %+v", u)
		}
	}
}

// ── Name check ───────────────────────────────────────────────────────────────

func TestConfigProvider_Name(t *testing.T) {
	p := config_resolve.NewConfigProvider(nil, nil)
	assert.Equal(t, "config", p.Name())
}

// helpers

func edgeLabels(edges []graph.Edge) []string {
	ls := make([]string, len(edges))
	for i, e := range edges {
		ls[i] = e.Label
	}
	return ls
}
