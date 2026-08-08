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

// buildThreeHopWorkspace is the full migrate-from-legacy shape, end to end: the
// env var reaches the client struct only after being forwarded through an
// intermediate constructor's parameter, and the `/api/v1` the handler serves
// exists only in that variable's checked-in value.
//
// Neither tier links this alone. The hop-limit tier recovers *which* variable
// the base URL is (`apiURL` is nothing but NewTilePublisher's parameter); Tier
// CB reads the prefix out of that variable's value. This is the acceptance
// test for the two composing.
func buildThreeHopWorkspace(t *testing.T) *workspace.WorkspaceConfig {
	t.Helper()
	dir := t.TempDir()

	svcA := filepath.Join(dir, "migrator")
	require.NoError(t, os.MkdirAll(svcA, 0o755))
	writeFile(t, svcA, "go.mod", "module example.com/migrator\n\ngo 1.22\n")
	writeFile(t, svcA, ".env", "MAPLE_MANAGER_API_URL=http://maple-manager.internal:3100/api/v1\n")
	writeFile(t, svcA, "config.go", `package main

import "os"

type Config struct {
	MapleManagerAPIURL string
	MapleAPIKey        string
}

func LoadConfig() *Config {
	return &Config{
		MapleManagerAPIURL: os.Getenv("MAPLE_MANAGER_API_URL"),
		MapleAPIKey:        os.Getenv("MAPLE_API_KEY"),
	}
}
`)
	writeFile(t, svcA, "tile_publisher.go", `package main

type TilePublisher struct {
	client *MapleManagerUserAppClient
}

func NewTilePublisher(apiURL, authToken string) *TilePublisher {
	return &TilePublisher{
		client: NewMapleManagerUserAppClient(apiURL, authToken),
	}
}
`)
	writeFile(t, svcA, "maple_manager_user_app_client.go", `package main

import (
	"fmt"
	"net/http"
)

type MapleManagerUserAppClient struct {
	baseURL   string
	authToken string
}

func NewMapleManagerUserAppClient(baseURL, authToken string) *MapleManagerUserAppClient {
	return &MapleManagerUserAppClient{baseURL: baseURL, authToken: authToken}
}

func (c *MapleManagerUserAppClient) CreateUserApp() error {
	reqURL := fmt.Sprintf("%s/user-apps", c.baseURL)
	_, err := http.NewRequest("POST", reqURL, nil)
	return err
}
`)
	writeFile(t, svcA, "main.go", `package main

func main() {
	config := LoadConfig()
	_ = NewTilePublisher(config.MapleManagerAPIURL, config.MapleAPIKey)
}
`)

	svcB := filepath.Join(dir, "maple-manager")
	require.NoError(t, os.MkdirAll(svcB, 0o755))
	writeFile(t, svcB, "go.mod",
		"module example.com/maple-manager\n\ngo 1.22\n\nrequire github.com/go-chi/chi/v5 v5.0.11\n")
	writeFile(t, svcB, "main.go", `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()
	r.Post("/api/v1/user-apps", createUserApp)
	_ = http.ListenAndServe(":3100", r)
}

func createUserApp(w http.ResponseWriter, r *http.Request) {}
`)

	return &workspace.WorkspaceConfig{
		Name: "hoptest", Version: "1",
		Services: []workspace.Service{
			{Name: "migrator", Path: svcA, Language: "go"},
			{Name: "maple-manager", Path: svcB, Language: "go"},
		},
	}
}

func TestGoHTTPHostHops_ForwardedParamLinksAcrossServices(t *testing.T) {
	cfg := buildThreeHopWorkspace(t)
	idx := openIndex(t, cfg, t.TempDir(), true)

	var client *graph.Node
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Service == "migrator" &&
			n.Meta["path"] != "" && n.Meta["synthesized"] == "sprintf_url" {
			client = n
		}
	}
	require.NotNil(t, client, "the */user-apps client node must exist")

	// The hop tier: apiURL is only ever NewTilePublisher's parameter.
	assert.Equal(t, "MAPLE_MANAGER_API_URL", client.Meta["env_var"],
		"env var must survive being forwarded through an intermediate constructor")
	// Tier CB: /api/v1 exists nowhere in either service's code.
	assert.Equal(t, "*/api/v1/user-apps", client.Meta["path"])

	var handlerID string
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/api/v1/user-apps" {
			handlerID = n.ID
		}
	}
	require.NotEmpty(t, handlerID, "maple-manager must expose POST /api/v1/user-apps")

	var linked bool
	for _, e := range idx.AllEdges() {
		if e.Type == graph.EdgeTypeHTTPCall && e.From == client.ID && e.To == handlerID {
			linked = true
		}
	}
	assert.True(t, linked,
		"the three-hop env trace plus the config-supplied prefix must link migrator → maple-manager")
}
