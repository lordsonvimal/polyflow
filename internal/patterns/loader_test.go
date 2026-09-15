package patterns_test

import (
	"os"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGoPatterns(t *testing.T) {
	files, err := patterns.Load("../../patterns/go")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files), 3, "expected at least 3 Go pattern files")

	var totalPatterns int
	for _, pf := range files {
		assert.Equal(t, "go", pf.Language, "all files should be language=go")
		// A Tier FX hub-only framework (e.g. gorm_tables.yaml) has no
		// `patterns:` block by design — its facts come from a HubProvider,
		// not tree-sitter extraction — so this loader (which only knows
		// about the legacy `patterns:`/`package`/`reflect_dispatched_
		// methods` fields, not `hub:`/`emit:`) legitimately sees it as
		// empty. Only files this loader actually drives need patterns.
		totalPatterns += len(pf.Patterns)
	}
	assert.Greater(t, totalPatterns, 0, "should have loaded patterns")
}

func TestLoadFileChiRoutes(t *testing.T) {
	pf, err := patterns.LoadFile("../../patterns/go/chi_routes.yaml")
	require.NoError(t, err)
	assert.Equal(t, "go", pf.Language)
	assert.NotEmpty(t, pf.Patterns)

	var names []string
	for _, p := range pf.Patterns {
		names = append(names, p.Name)
		assert.NotEmpty(t, p.Query, "pattern %q should have a query", p.Name)
	}
	assert.Contains(t, names, "chi_get")
}

func TestLoadFileJavaScriptPatterns(t *testing.T) {
	files, err := patterns.Load("../../patterns/javascript")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(files), 3, "expected at least 3 JavaScript pattern files")
	for _, pf := range files {
		assert.Equal(t, "javascript", pf.Language)
		if len(pf.ReflectDispatchedMethods) > 0 {
			// A reflect_dispatched_methods-only file (e.g. react.yaml) has
			// nothing for the tree-sitter matcher to look for — same shape as
			// patterns/ruby/devise.yaml.
			continue
		}
		if len(pf.Patterns) == 0 {
			// A hub-only framework (Tier FX's cross-framework fact-sharing
			// mechanism, internal/factpipe/hub.go — e.g. pusher_js_consumer.yaml)
			// carries no `patterns:` tree-sitter queries at all — nothing for
			// this harness to check. Its correctness is covered by
			// internal/factpipe/pipeline/*_test.go instead, same as
			// internal/patterns/fixtures_test.go's own skip for this shape.
			continue
		}
		assert.NotEmpty(t, pf.Patterns)
	}
}

func TestLoadFileErrorPath(t *testing.T) {
	_, err := patterns.LoadFile("/nonexistent/path/does_not_exist.yaml")
	assert.Error(t, err, "loading a nonexistent file should return an error")
}

func TestDefaultRegistry(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)
	langs := reg.Languages()
	assert.Contains(t, langs, "go")

	goPatterns := reg.List("go")
	assert.NotEmpty(t, goPatterns)
}


func TestLoad_NonexistentDir(t *testing.T) {
	_, err := patterns.Load("/no/such/dir")
	assert.Error(t, err)
}

func TestLoadFile_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/bad.yaml"
	require.NoError(t, os.WriteFile(path, []byte(":\t[invalid"), 0o644))
	_, err := patterns.LoadFile(path)
	assert.Error(t, err)
}

func TestDefaultRegistry_NonexistentDir(t *testing.T) {
	_, err := patterns.DefaultRegistry("/no/such/patterns")
	assert.Error(t, err)
}

func TestLoad_WithInvalidYAMLFile(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(tmp+"/bad.yaml", []byte(":\t[invalid"), 0o644))
	_, err := patterns.Load(tmp)
	assert.Error(t, err)
}
