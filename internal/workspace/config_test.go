package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "workspace*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func TestLoad_OK(t *testing.T) {
	path := writeYAML(t, `
name: my-workspace
version: "1"
services:
  - name: svc-a
    path: ./svc-a
    language: go
    frameworks: [chi]
links:
  - from: svc-a
    to: svc-b
    base_url: "/api"
index:
  exclude:
    - "**/vendor/**"
settings:
  port: 9401
  snippet_lines: 50
  default_layout: dagre-lr
  default_depth: 3
`)
	cfg, err := workspace.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "my-workspace", cfg.Name)
	assert.Equal(t, "1", cfg.Version)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, "svc-a", cfg.Services[0].Name)
	assert.Equal(t, []string{"chi"}, cfg.Services[0].Frameworks)
	require.Len(t, cfg.Links, 1)
	assert.Equal(t, "/api", cfg.Links[0].BaseURL)
	assert.Equal(t, []string{"**/vendor/**"}, cfg.Index.Exclude)
	assert.Equal(t, 9401, cfg.Settings.Port)
	assert.Equal(t, 50, cfg.Settings.SnippetLines)
	assert.Equal(t, "dagre-lr", cfg.Settings.DefaultLayout)
	assert.Equal(t, 3, cfg.Settings.DefaultDepth)
}

func TestLoad_NotFound(t *testing.T) {
	_, err := workspace.Load("/does/not/exist.yaml")
	assert.Error(t, err)
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeYAML(t, ":\tinvalid: [yaml")
	_, err := workspace.Load(path)
	assert.Error(t, err)
}

func TestSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.yaml")

	cfg := &workspace.WorkspaceConfig{
		Name:    "test-ws",
		Version: "1",
		Services: []workspace.Service{
			{Name: "svc", Path: "./svc", Language: "go", Frameworks: []string{"chi"}},
		},
		Settings: workspace.Settings{Port: 9402},
	}
	require.NoError(t, workspace.Save(path, cfg))

	loaded, err := workspace.Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg.Name, loaded.Name)
	assert.Equal(t, cfg.Settings.Port, loaded.Settings.Port)
	assert.Equal(t, cfg.Services[0].Frameworks, loaded.Services[0].Frameworks)
}

func TestSave_AtomicNoPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.yaml")
	cfg := &workspace.WorkspaceConfig{Name: "ws", Version: "1"}
	require.NoError(t, workspace.Save(path, cfg))
	assert.FileExists(t, path)
	// .tmp file must be cleaned up
	assert.NoFileExists(t, path+".tmp")
}

func TestEffectivePort_FromSettings(t *testing.T) {
	cfg := &workspace.WorkspaceConfig{Settings: workspace.Settings{Port: 8080}}
	assert.Equal(t, 8080, cfg.EffectivePort())
}

func TestEffectivePort_Default(t *testing.T) {
	cfg := &workspace.WorkspaceConfig{}
	assert.Equal(t, meta.DefaultPort, cfg.EffectivePort())
}

func TestDetectFrameworks_GoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example"), 0o644))

	hints, err := workspace.DetectFrameworks(dir)
	require.NoError(t, err)
	require.NotEmpty(t, hints)
	found := false
	for _, h := range hints {
		if h.Language == "go" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestDetectFrameworks_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Gemfile"), []byte(""), 0o644))

	hints, err := workspace.DetectFrameworks(dir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(hints), 2)
}

func TestDetectFrameworks_GoFrameworks(t *testing.T) {
	dir := t.TempDir()
	gomod := "module example\n\nrequire (\n\tgithub.com/go-chi/chi v5.0.0\n\tgithub.com/a-h/templ v0.2.0\n)\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644))

	hints, err := workspace.DetectFrameworks(dir)
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, h := range hints {
		names[h.Name] = true
	}
	assert.True(t, names["chi"], "expected chi detected")
	assert.True(t, names["templ"], "expected templ detected")
}

func TestDetectFrameworks_NodeFrameworks(t *testing.T) {
	dir := t.TempDir()
	pkgjson := `{"dependencies":{"axios":"^1.0","solid-js":"^1.8"}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgjson), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte("{}"), 0o644))

	hints, err := workspace.DetectFrameworks(dir)
	require.NoError(t, err)

	names := make(map[string]bool)
	lang := ""
	for _, h := range hints {
		names[h.Name] = true
		if h.Language != "" {
			lang = h.Language
		}
	}
	assert.Equal(t, "typescript", lang, "expected typescript detected via tsconfig.json")
	assert.True(t, names["axios"], "expected axios detected")
	assert.True(t, names["solid"], "expected solid-js detected")
}

func TestDetectFrameworks_Empty(t *testing.T) {
	dir := t.TempDir()
	hints, err := workspace.DetectFrameworks(dir)
	require.NoError(t, err)
	assert.Empty(t, hints)
}

func TestSave_ErrorOnReadonlyDir(t *testing.T) {
	path := "/dev/null/impossible/workspace.yaml"
	cfg := &workspace.WorkspaceConfig{Name: "x"}
	err := workspace.Save(path, cfg)
	assert.Error(t, err)
}

func TestLoadIgnoreFile_Missing(t *testing.T) {
	assert.Nil(t, workspace.LoadIgnoreFile(t.TempDir()))
}

func TestLoadIgnoreFile_PatternsAndComments(t *testing.T) {
	dir := t.TempDir()
	content := "# fixtures\npatterns/\n**/*.gen.go\n\n  docs  \n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, workspace.IgnoreFileName), []byte(content), 0o644))

	got := workspace.LoadIgnoreFile(dir)
	assert.Equal(t, []string{"patterns", "patterns/**", "**/*.gen.go", "docs", "docs/**"}, got)
}
