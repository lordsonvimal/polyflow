package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// buildFiberPlugin compiles the Phase 1 reference plugin
// (testdata/fiberplugin) and copies its manifest + pattern files alongside
// the binary into <dir>/.polyflow/plugins/fiber/ — the exact on-disk shape
// docs/linker-plugin-architecture-plan.md's "Directory + manifest
// convention" describes for a project-local plugin. This is an ordinary `go
// build` of the plugin author's own package, same as
// internal/pluginloader/roundtrip_test.go's buildFakePlugin.
func buildFiberPlugin(t *testing.T, workspaceDir string) {
	t.Helper()
	pluginDir := filepath.Join(workspaceDir, ".polyflow", "plugins", "fiber")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "patterns"), 0o755))

	srcDir, err := filepath.Abs("testdata/fiberplugin")
	require.NoError(t, err)

	bin := filepath.Join(pluginDir, "fiber")
	cmd := exec.Command("go", "build", "-o", bin, srcDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go build fiber plugin: %s", out)

	copyFile(t, filepath.Join(srcDir, "manifest.yaml"), filepath.Join(pluginDir, "manifest.yaml"))
	copyFile(t, filepath.Join(srcDir, "patterns", "routes.yaml"), filepath.Join(pluginDir, "patterns", "routes.yaml"))
	copyFile(t, filepath.Join(srcDir, "patterns", "middleware.yaml"), filepath.Join(pluginDir, "patterns", "middleware.yaml"))
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o644))
}

const fiberServiceMain = `package main

import "github.com/gofiber/fiber/v2"

func listUsers(c *fiber.Ctx) error { return nil }
func authMiddleware(c *fiber.Ctx) error { return c.Next() }

func main() {
	app := fiber.New()
	app.Use(authMiddleware)
	app.Get("/users", listUsers)
	_ = app.Listen(":3000")
}
`

// fiberWorkspace builds a one-service Go workspace and t.Chdir's into it —
// indexer.Run resolves both npm/go dependency lookups and (Phase 1) plugin
// discovery relative to the process's cwd (see indexer.go's workspaceRoot),
// so the test must run from the workspace root, exactly like a real
// `polyflow index` invocation from a repo checkout.
func fiberWorkspace(t *testing.T, withFiberDep bool) (*workspace.WorkspaceConfig, string) {
	t.Helper()
	dir := t.TempDir()

	svcDir := filepath.Join(dir, "api")
	require.NoError(t, os.MkdirAll(svcDir, 0o755))

	goMod := "module example.com/api\n\ngo 1.22\n"
	if withFiberDep {
		// A real go.sum entry would need network access this test can't rely
		// on; a local `replace` needs none (no checksum required for a
		// filesystem replacement) and still leaves the `require` line intact
		// so deps.Resolve reports the same (name, version) a real dependency
		// would — the only thing PackageQualifies/patterns.ForService look at.
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "fiberstub"), 0o755))
		writeFile(t, dir, "fiberstub/go.mod", "module github.com/gofiber/fiber/v2\n\ngo 1.22\n")
		writeFile(t, dir, "fiberstub/fiber.go", `package fiber

type Ctx struct{}

func (c *Ctx) Next() error { return nil }

type App struct{}

func New() *App { return &App{} }

func (a *App) Get(path string, h func(*Ctx) error)  {}
func (a *App) Post(path string, h func(*Ctx) error) {}
func (a *App) Use(h func(*Ctx) error)                {}
func (a *App) Listen(addr string) error              { return nil }
`)
		goMod += "\nrequire github.com/gofiber/fiber/v2 v2.52.0\n\nreplace github.com/gofiber/fiber/v2 => ../fiberstub\n"
	}
	writeFile(t, svcDir, "go.mod", goMod)
	writeFile(t, svcDir, "main.go", fiberServiceMain)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{
			{Name: "api", Path: svcDir, Language: "go"},
		},
	}
	return cfg, dir
}

// TestRun_PluginProducesRoutesAndMiddlewareEdges is Phase 1's acceptance
// test: the fiber reference plugin, built and dropped into a fixture's
// .polyflow/plugins/, produces correct edges on a full `polyflow index` run
// with zero changes to internal/linker/, internal/parser/, or any pattern
// file outside the plugin's own directory.
func TestRun_PluginProducesRoutesAndMiddlewareEdges(t *testing.T) {
	cfg, dir := fiberWorkspace(t, true)
	buildFiberPlugin(t, dir)
	patternsDir, err := filepath.Abs("../../patterns")
	require.NoError(t, err)
	t.Chdir(dir)

	dbDir := filepath.Join(dir, ".polyflow")

	stats, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: patternsDir,
		Workers:     2,
		Full:        true,
	})
	require.NoError(t, err)
	require.Greater(t, stats.Nodes, 0)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	var routesTo, calls []*graph.Edge
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			switch e.Type {
			case "routes_to":
				routesTo = append(routesTo, e)
			case "calls":
				if e.Meta["guarded_by"] == "fiber_middleware" {
					calls = append(calls, e)
				}
			}
		}
	}

	require.Len(t, routesTo, 1, "routes component should resolve exactly one handler")
	route := routesTo[0]
	assert.Equal(t, "Get", route.Method)
	assert.Equal(t, "/users", route.Path)
	handlerNode := idx.Nodes[route.To]
	require.NotNil(t, handlerNode)
	assert.Equal(t, "listUsers", handlerNode.Label)

	require.Len(t, calls, 1, "reconcile should join the route's handler to the file's middleware")
	guard := calls[0]
	assert.Equal(t, route.To, guard.From, "the resolved handler function should be the calls edge source")
	mwNode := idx.Nodes[guard.To]
	require.NotNil(t, mwNode)
	assert.Equal(t, "authMiddleware", mwNode.Label)

	// Reconcile's carrier UnresolvedRef entries must be retracted, not left
	// sitting in the ledger — they were a join key, never a real miss.
	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	for _, u := range unresolved {
		assert.NotEqual(t, "fiber_route_resolved", u.Kind)
		assert.NotEqual(t, "fiber_middleware_resolved", u.Kind)
	}
}

// TestRun_PluginSkipsServiceWithoutDependency is the negative fixture:
// identical source, but go.mod does not declare github.com/gofiber/fiber/v2,
// so the plugin's own pattern files (package-gated exactly like a built-in
// patterns/*.yaml file) never match and the plugin's Link is never called
// for this service — zero fiber edges, not a crash and not silently-wrong
// edges (docs/linker-plugin-architecture-plan.md Phase 1's negative-fixture
// ground rule).
func TestRun_PluginSkipsServiceWithoutDependency(t *testing.T) {
	cfg, dir := fiberWorkspace(t, false)
	buildFiberPlugin(t, dir)
	patternsDir, err := filepath.Abs("../../patterns")
	require.NoError(t, err)
	t.Chdir(dir)

	dbDir := filepath.Join(dir, ".polyflow")

	_, err = Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: patternsDir,
		Workers:     2,
		Full:        true,
	})
	require.NoError(t, err)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			assert.NotEqual(t, "routes_to", string(e.Type))
		}
	}
}
