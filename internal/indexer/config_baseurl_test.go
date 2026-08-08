package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// buildConfigPrefixWorkspace is the Tier CB worked example: a client whose base
// URL is read from API_URL and whose source only spells `/user-apps`, and a
// second service serving `/api/v2/user-apps`. The `/api/v2` exists nowhere in
// either service's code — only in svc-a's checked-in .env.
//
// envValue is written as API_URL so a caller can vary it (a bare host must be a
// no-op, which is what the whole juniper fleet looks like today).
func buildConfigPrefixWorkspace(t *testing.T, envValue string) *workspace.WorkspaceConfig {
	t.Helper()
	dir := t.TempDir()

	svcA := filepath.Join(dir, "svc-a")
	require.NoError(t, os.MkdirAll(svcA, 0o755))
	writeFile(t, svcA, "go.mod", "module example.com/svc-a\n\ngo 1.22\n")
	writeFile(t, svcA, ".env", "API_URL="+envValue+"\n")
	writeFile(t, svcA, "config.go", `package main

import "os"

type Config struct {
	APIURL string
}

func LoadConfig() *Config {
	return &Config{APIURL: os.Getenv("API_URL")}
}
`)
	writeFile(t, svcA, "client.go", `package main

import (
	"fmt"
	"net/http"
)

type UserAppClient struct {
	baseURL string
}

func NewUserAppClient(baseURL string) *UserAppClient {
	return &UserAppClient{baseURL: baseURL}
}

func (c *UserAppClient) List() error {
	reqURL := fmt.Sprintf("%s/user-apps", c.baseURL)
	_, err := http.NewRequest("GET", reqURL, nil)
	return err
}
`)
	writeFile(t, svcA, "main.go", `package main

func main() {
	config := LoadConfig()
	client := NewUserAppClient(config.APIURL)
	_ = client.List()
}
`)

	svcB := filepath.Join(dir, "svc-b")
	require.NoError(t, os.MkdirAll(svcB, 0o755))
	writeFile(t, svcB, "go.mod",
		"module example.com/svc-b\n\ngo 1.22\n\nrequire github.com/go-chi/chi/v5 v5.0.11\n")
	writeFile(t, svcB, "main.go", `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()
	r.Get("/api/v2/user-apps", listUserApps)
	_ = http.ListenAndServe(":8080", r)
}

func listUserApps(w http.ResponseWriter, r *http.Request) {}
`)

	return &workspace.WorkspaceConfig{
		Name: "cbtest", Version: "1",
		Services: []workspace.Service{
			{Name: "svc-a", Path: svcA, Language: "go"},
			{Name: "svc-b", Path: svcB, Language: "go"},
		},
	}
}

// findUserAppClient returns svc-a's http_client node for the /user-apps call.
func findUserAppClient(t *testing.T, idx *graph.AdjacencyIndex) *graph.Node {
	t.Helper()
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Service == "svc-a" &&
			n.Meta["path"] != "" && n.Meta["env_var"] == "API_URL" {
			return n
		}
	}
	t.Fatalf("no env-attributed http_client found in svc-a")
	return nil
}

// The whole point of the tier: the prefix lives only in config, and composing
// it makes the cross-service join possible.
func TestConfigBaseURLPrefix_LinksAcrossServices(t *testing.T) {
	cfg := buildConfigPrefixWorkspace(t, "https://svc-b.internal/api/v2")
	idx := openIndex(t, cfg, t.TempDir(), true)

	client := findUserAppClient(t, idx)
	assert.Equal(t, "*/api/v2/user-apps", client.Meta["path"],
		"config-supplied prefix must be composed onto the client path")
	assert.Equal(t, "API_URL", client.Meta["path_prefix_from"])
	assert.Equal(t, ".env:1", client.Meta["path_prefix_ref"])

	// Re-graded: two literal segments no longer need the weak-evidence
	// suppression or the partial ceiling that came with it.
	assert.NotContains(t, client.Meta, "path_evidence")
	assert.NotContains(t, client.Meta, "confidence_ceiling")

	var handlerID string
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/api/v2/user-apps" {
			handlerID = n.ID
		}
	}
	require.NotEmpty(t, handlerID, "svc-b must expose a /api/v2/user-apps handler")

	var linked bool
	var confidence string
	for _, e := range idx.AllEdges() {
		if e.Type == graph.EdgeTypeHTTPCall && e.From == client.ID && e.To == handlerID {
			linked = true
			confidence = e.Confidence
		}
	}
	assert.True(t, linked,
		"client must link to svc-b's handler through the config-supplied /api/v2 prefix")
	assert.NotEqual(t, graph.ConfidencePartial, confidence,
		"the stale weak-evidence ceiling must not cap a now-ordinary match")
}

// The fleet's actual shape: every checked-in value is a bare scheme://host:port
// and this pass is correctly a no-op. Without the prefix there is nothing to
// join, which is the pre-tier behaviour.
func TestConfigBaseURLPrefix_BareHostIsNoOp(t *testing.T) {
	cfg := buildConfigPrefixWorkspace(t, "http://localhost:3000")
	idx := openIndex(t, cfg, t.TempDir(), true)

	client := findUserAppClient(t, idx)
	assert.Equal(t, "*/user-apps", client.Meta["path"], "path must be untouched")
	assert.NotContains(t, client.Meta, "path_prefix_from")
	assert.NotContains(t, client.Meta, "path_prefix_ref")
}
