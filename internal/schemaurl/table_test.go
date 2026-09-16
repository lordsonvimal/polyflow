package schemaurl_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/artifact"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
)

// writeSchemaFixture writes files (name→content) under a fresh temp dir and
// returns the service→abs-file-list map LoadTables expects.
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
		got, ok := schemaurl.NormalizeSchemaPath(c.in)
		assert.Equal(t, c.ok, ok, c.in)
		if c.ok {
			assert.Equal(t, c.want, got, c.in)
		}
	}
}

// TestEndpointTableNormalizerChainMatchesNormalizeSchemaPath: the
// endpoint_table gate's normalizer chain must stay byte-identical to
// NormalizeSchemaPath — a hub builds handlerPaths with the latter and the
// gate corroborates against it, so any drift silently drops corroboration.
func TestEndpointTableNormalizerChainMatchesNormalizeSchemaPath(t *testing.T) {
	t.Parallel()
	m, ok := artifact.MappingFor("endpoint_table")
	if !ok {
		t.Fatal("endpoint_table mapping missing")
	}
	g := m.GateSpec()

	cases := []string{
		"/api/gadgets/{gadget_id}/widgets",
		"/api/widgets/<id>",
		"/api/gadgets/:id/reorder",
		"/api/things/${id}?type=Form&x=1",
		"/api//double///slash/",
		"/api/standards/${standard_id}/forms/${id}",
		"relative/path",
		"https://host/x",
		"",
		"/",
		"#frag-only",
		"/trailing/",
		"/a/b#x?y",
	}
	for _, in := range cases {
		want, wantOK := schemaurl.NormalizeSchemaPath(in)
		got, gotOK := g.Norm(in)
		if gotOK != wantOK || got != want {
			t.Errorf("%q: gate.Norm = (%q,%v), NormalizeSchemaPath = (%q,%v)", in, got, gotOK, want, wantOK)
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

func TestLoadTables_JSONDialectQualifies(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"config/resources.json": widgetJSON})
	handlers := handlerSet(
		"/api/widgets", "/api/widgets/*/reorder",
		"/api/gadgets", "/api/gadgets/*/reorder",
		"/api/sprockets", "/api/sprockets/*",
	)
	tables, ledger := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
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

func TestLoadTables_YAMLTwoLevelNest(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"cfg/res.yaml": widgetYAML})
	handlers := handlerSet(
		"/api/widgets", "/api/widgets/*",
		"/api/gadgets", "/api/gadgets/*",
		"/api/sprockets", "/api/sprockets/*",
	)
	tables, _ := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	require.Contains(t, tables, "svc")
	tbl := tables["svc"]
	assert.ElementsMatch(t, []string{"gadget", "sprocket", "widget"}, tbl.Entities())
	e, ok := tbl.Lookup("widget", "endpoints.list")
	require.True(t, ok)
	assert.Equal(t, "/api/widgets", e.Path)
	assert.Equal(t, "endpoints.list", e.Key)
}

func TestLoadTables_RejectsNonAssets(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{
		"package.json":  `{"name":"x","scripts":{"build":"tsc"},"bin":"/usr/bin/env"}`,
		"tsconfig.json": `{"compilerOptions":{"outDir":"/dist"}}`,
		"responses.json": `{"a":{"url":"/api/widgets"},"b":{"url":"/api/gadgets"},"c":{"note":"nope"},` +
			`"d":{"x":1},"e":{"y":2},"f":{"z":3},"g":{"w":4},"h":{"v":5}}`,
	})
	handlers := handlerSet("/api/widgets", "/api/gadgets", "/api/sprockets", "/api/things", "/api/more")
	tables, _ := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	assert.Empty(t, tables)
}

func TestLoadTables_OpenAPISkipped(t *testing.T) {
	t.Parallel()
	// declared so the skip row is emitted even though it never corroborates
	sf := writeSchemaFixture(t, map[string]string{"openapi.json": `{"openapi":"3.0.0","paths":{"/api/widgets":{"get":{}}}}`})
	tables, ledger := schemaurl.LoadTables(sf, handlerSet("/api/widgets"),
		schemaurl.Config{Assets: []string{"**/openapi.json"}})
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

func TestLoadTables_FixtureCopySkipped(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"spec/fixtures/resources.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, ledger := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	assert.Empty(t, tables)
	assert.Empty(t, ledger)
}

func TestLoadTables_Alias(t *testing.T) {
	t.Parallel()
	j := `{"resources":{
      "widget":{"model":"widget_v2","endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"model":"gadget_v2","endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"},
      "sprocket":{"model":"sprocket_v2","endpoint":"/api/sprockets","update":"/api/sprockets/:id"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, _ := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	require.Contains(t, tables, "svc")
	tbl := tables["svc"]
	assert.Equal(t, "widget", tbl.Aliases["widget_v2"])
	e, ok := tbl.Lookup("widget_v2", "reorder")
	require.True(t, ok)
	assert.Equal(t, "/api/widgets/*/reorder", e.Path)
}

func TestLoadTables_DeadEntry(t *testing.T) {
	t.Parallel()
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"},
      "sprocket":{"endpoint":"/api/sprockets","gone":"/api/sprockets/:id/legacy_action"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets")
	_, ledger := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	var dead []string
	for _, l := range ledger {
		if l.Kind == "schema_route_dead" {
			dead = append(dead, l.Name)
		}
	}
	require.Len(t, dead, 1)
	assert.Equal(t, "/api/sprockets/:id/legacy_action", dead[0])
}

func TestLoadTables_Deterministic(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"r.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	t1, l1 := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	t2, l2 := schemaurl.LoadTables(sf, handlers, schemaurl.Config{})
	assert.Equal(t, t1, t2)
	assert.Equal(t, l1, l2)
}

func TestLoadTables_Disable(t *testing.T) {
	t.Parallel()
	sf := writeSchemaFixture(t, map[string]string{"r.json": widgetJSON})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets",
		"/api/gadgets/*/reorder", "/api/sprockets", "/api/sprockets/*")
	tables, ledger := schemaurl.LoadTables(sf, handlers, schemaurl.Config{Disable: true})
	assert.Empty(t, tables)
	assert.Empty(t, ledger)
}

func TestLoadTables_TunedVsTightened(t *testing.T) {
	t.Parallel()
	// 4 corroborated paths — below the default floor of 5, above a loosened 3.
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
      "gadget":{"endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"}}}`
	sf := writeSchemaFixture(t, map[string]string{"r.json": j})
	handlers := handlerSet("/api/widgets", "/api/widgets/*/reorder", "/api/gadgets", "/api/gadgets/*/reorder")

	_, loose := schemaurl.LoadTables(sf, handlers, schemaurl.Config{MinCorroboratedPaths: 3})
	var tuned bool
	for _, l := range loose {
		if l.Kind == "schema_asset_loaded_tuned" {
			tuned = true
			assert.Contains(t, l.Targets, "min_corroborated_paths=3")
		}
	}
	assert.True(t, tuned, "loosened threshold must emit schema_asset_loaded_tuned")

	// tightening can only reject — never a tuned row.
	_, tight := schemaurl.LoadTables(sf, handlers, schemaurl.Config{MinCorroboratedPaths: 10})
	for _, l := range tight {
		assert.NotEqual(t, "schema_asset_loaded_tuned", l.Kind)
	}
}

func TestLoadTables_DeclaredBypassesGate(t *testing.T) {
	t.Parallel()
	// only 2 corroborated paths; would never self-discover.
	j := `{"resources":{
      "widget":{"endpoint":"/api/widgets","gone":"/api/widgets/legacy"},
      "gadget":{"endpoint":"/api/gadgets","gone":"/api/gadgets/legacy"}}}`
	sf := writeSchemaFixture(t, map[string]string{"config/r.json": j})
	handlers := handlerSet("/api/widgets", "/api/gadgets")
	tables, ledger := schemaurl.LoadTables(sf, handlers,
		schemaurl.Config{Assets: []string{"**/config/r.json"}})
	require.Contains(t, tables, "svc")
	var dead int
	for _, l := range ledger {
		if l.Kind == "schema_route_dead" {
			dead++
		}
	}
	assert.Equal(t, 2, dead)
}
