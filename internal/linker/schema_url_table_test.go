package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// writeSchemaFixture writes files (name→content) under a fresh temp dir and
// returns the service→abs-file-list map LoadSchemaURLTables expects.
func writeSchemaFixture(t *testing.T, files map[string]string) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	var list []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		list = append(list, p)
	}
	return map[string][]string{"svc": list}
}

func handlerSet(paths ...string) map[string]map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return map[string]map[string]bool{"svc": m}
}

func TestNormalizeSchemaPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/api/gadgets/{gadget_id}/widgets", "/api/gadgets/*/widgets", true},
		{"/api/widgets/<id>", "/api/widgets/*", true},
		{"/api/gadgets/:id/reorder", "/api/gadgets/*/reorder", true},
		{"/api/things/${id}?type=Form&x=1", "/api/things/*", true},
		{"/api//double///slash/", "/api/double/slash", true},
		{"relative/path", "", false},
		{"https://host/x", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeSchemaPath(c.in)
		assert.Equal(t, c.ok, ok, c.in)
		if c.ok {
			assert.Equal(t, c.want, got, c.in)
		}
	}
}

const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`

func TestLoadSchemaURLTables_JSONDialectQualifies(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"config/resources.json": widgetJSON})
	handlers := handlerSet(
		"/api/widgets", "/api/widgets/*/reorder",
		"/api/gadgets", "/api/gadgets/*/reorder",
		"/api/sprockets", "/api/sprockets/*",
	)
	tables, ledger := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	require.Contains(t, tables, "svc")
	tbl := tables["svc"]
	assert.ElementsMatch(t, []string{"gadget", "sprocket", "widget"}, tbl.Entities())

	e, ok := tbl.Lookup("widget", "reorder")
	require.True(t, ok)
	assert.Equal(t, "/api/widgets/*/reorder", e.Path)
	assert.Equal(t, "/api/widgets/:id/reorder", e.Raw)

	e, ok = tbl.Lookup("sprocket", "update")
	require.True(t, ok)
	assert.Equal(t, "/api/sprockets/*", e.Path)

	// exactly one schema_asset_loaded row, no dead rows.
	var loaded, dead int
	for _, l := range ledger {
		switch l.Kind {
		case "schema_asset_loaded":
			loaded++
		case "schema_route_dead":
			dead++
		}
	}
	assert.Equal(t, 1, loaded)
	assert.Equal(t, 0, dead)
}

const widgetYAML = `resources:
  widget:
    endpoints:
      list:   /api/widgets
      detail: /api/widgets/:id
  gadget:
    endpoints:
      list:   /api/gadgets
      detail: /api/gadgets/:id
  sprocket:
    endpoints:
      list:   /api/sprockets
      detail: /api/sprockets/:id
`

func TestLoadSchemaURLTables_YAMLTwoLevelNest(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"cfg/res.yaml": widgetYAML})
	handlers := handlerSet(
		"/api/widgets", "/api/widgets/*",
		"/api/gadgets", "/api/gadgets/*",
		"/api/sprockets", "/api/sprockets/*",
	)
	tables, _ := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	require.Contains(t, tables, "svc")
	tbl := tables["svc"]
	assert.ElementsMatch(t, []string{"gadget", "sprocket", "widget"}, tbl.Entities())
	e, ok := tbl.Lookup("widget", "endpoints.list")
	require.True(t, ok)
	assert.Equal(t, "/api/widgets", e.Path)
	assert.Equal(t, "endpoints.list", e.Key)
}

func TestLoadSchemaURLTables_RejectsNonAssets(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{
		"package.json":  `{"name":"x","scripts":{"build":"tsc"},"bin":"/usr/bin/env"}`,
		"tsconfig.json": `{"compilerOptions":{"outDir":"/dist"}}`,
		"responses.json": `{"a":{"url":"/api/widgets"},"b":{"url":"/api/gadgets"},"c":{"note":"nope"},` +
			`"d":{"x":1},"e":{"y":2},"f":{"z":3},"g":{"w":4},"h":{"v":5}}`,
	})
	handlers := handlerSet("/api/widgets", "/api/gadgets", "/api/sprockets", "/api/things", "/api/more")
	tables, _ := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	assert.Empty(t, tables)
}

func TestLoadSchemaURLTables_OpenAPISkipped(t *testing.T) {
	t.Parallel()
	// declared so the skip row is emitted even though it never corroborates
	sf := writeSchemaFixture(t, map[string]string{"openapi.json": `{"openapi":"3.0.0","paths":{"/api/widgets":{"get":{}}}}`})
	tables, ledger := LoadSchemaURLTables(sf, handlerSet("/api/widgets"),
		workspace.SchemaConfig{Assets: []string{"**/openapi.json"}})
	assert.Empty(t, tables)
	var skipped bool
	for _, l := range ledger {
		if l.Kind == "schema_asset_skipped" {
			skipped = true
			assert.Contains(t, l.Targets, "openapi")
		}
	}
	assert.True(t, skipped)
}

func TestLoadSchemaURLTables_FixtureCopySkipped(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"spec/fixtures/resources.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, ledger := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	assert.Empty(t, tables)
	assert.Empty(t, ledger)
}

func TestLoadSchemaURLTables_Alias(t *testing.T) {
	t.Parallel()
	j := `{"resources":{
      "widget":{"model":"widget_v2","endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"model":"gadget_v2","endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"},
      "sprocket":{"model":"sprocket_v2","endpoint":"/api/sprockets","update":"/api/sprockets/:id"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, _ := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	require.Contains(t, tables, "svc")
	tbl := tables["svc"]
	assert.Equal(t, "widget", tbl.Aliases["widget_v2"])
	e, ok := tbl.Lookup("widget_v2", "reorder")
	require.True(t, ok)
	assert.Equal(t, "/api/widgets/*/reorder", e.Path)
}

func TestLoadSchemaURLTables_DeadEntry(t *testing.T) {
	t.Parallel()
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"},
      "sprocket":{"endpoint":"/api/sprockets","gone":"/api/sprockets/:id/legacy_action"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets")
	_, ledger := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	var dead []string
	for _, l := range ledger {
		if l.Kind == "schema_route_dead" {
			dead = append(dead, l.Name)
		}
	}
	require.Len(t, dead, 1)
	assert.Equal(t, "/api/sprockets/:id/legacy_action", dead[0])
}

func TestLoadSchemaURLTables_Deterministic(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"r.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	t1, l1 := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	t2, l2 := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{})
	assert.Equal(t, t1, t2)
	assert.Equal(t, l1, l2)
}

func TestLoadSchemaURLTables_Disable(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"r.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, ledger := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{Disable: true})
	assert.Empty(t, tables)
	assert.Empty(t, ledger)
}

func TestLoadSchemaURLTables_TunedVsTightened(t *testing.T) {
	t.Parallel()
	// 4 corroborated paths — below the default floor of 5, above a loosened 3.
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets", "/api/gadgets/*/reorder")

	_, loose := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{MinCorroboratedPaths: 3})
	var tuned bool
	for _, l := range loose {
		if l.Kind == "schema_asset_loaded_tuned" {
			tuned = true
			assert.Contains(t, l.Targets, "min_corroborated_paths=3")
		}
	}
	assert.True(t, tuned, "loosened threshold must emit schema_asset_loaded_tuned")

	// tightening can only reject — never a tuned row.
	_, tight := LoadSchemaURLTables(sf, handlers, workspace.SchemaConfig{MinCorroboratedPaths: 10})
	for _, l := range tight {
		assert.NotEqual(t, "schema_asset_loaded_tuned", l.Kind)
	}
}

func TestLoadSchemaURLTables_DeclaredBypassesGate(t *testing.T) {
	t.Parallel()
	// only 2 corroborated paths; would never self-discover.
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","gone":"/api/widgets/legacy"},
      "gadget":{"endpoint":"/api/gadgets","gone":"/api/gadgets/legacy"}}}`
	sf := writeSchemaFixture(t, map[string]string{"config/r.json": j})
	handlers := handlerSet("/api/widgets", "/api/gadgets")
	tables, ledger := LoadSchemaURLTables(sf, handlers,
		workspace.SchemaConfig{Assets: []string{"**/config/r.json"}})
	require.Contains(t, tables, "svc")
	var dead int
	for _, l := range ledger {
		if l.Kind == "schema_route_dead" {
			dead++
		}
	}
	assert.Equal(t, 2, dead)
}

func TestSchemaConfigEffective(t *testing.T) {
	t.Parallel()
	got, loosened := workspace.SchemaConfig{}.Effective()
	assert.Equal(t, workspace.DefaultMinCorroboratedPaths, got.MinCorroboratedPaths)
	assert.Equal(t, workspace.DefaultMinCorroboratedRatio, got.MinCorroboratedRatio)
	assert.Equal(t, workspace.DefaultMinEntityDiscrimination, got.MinEntityDiscrimination)
	assert.False(t, loosened)

	orig := workspace.SchemaConfig{MinCorroboratedPaths: 2}
	_, loosened = orig.Effective()
	assert.True(t, loosened)
	assert.Equal(t, 2, orig.MinCorroboratedPaths, "Effective must not mutate the receiver")

	_, loosened = workspace.SchemaConfig{MinCorroboratedPaths: 9}.Effective()
	assert.False(t, loosened, "tightening is not loosening")
}
