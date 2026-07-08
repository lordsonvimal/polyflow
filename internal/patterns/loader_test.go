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
		assert.NotEmpty(t, pf.Patterns, "each file should have at least one pattern")
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
