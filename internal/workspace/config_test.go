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

// writeYAMLInDir writes a polyflow.yml (with the given content) into dir
// and returns its path, so relative service paths inside content resolve
// against a known workspace directory.
func writeYAMLInDir(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "polyflow.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoad_OK(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "svc-a"), 0o755))
	path := writeYAMLInDir(t, dir, `
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
	assert.Equal(t, filepath.Join(dir, "svc-a"), cfg.Services[0].Path)
	assert.Equal(t, []string{"chi"}, cfg.Services[0].Frameworks)
	require.Len(t, cfg.Links, 1)
	assert.Equal(t, "/api", cfg.Links[0].BaseURL)
	assert.Equal(t, []string{"**/vendor/**"}, cfg.Index.Exclude)
	assert.Equal(t, 9401, cfg.Settings.Port)
	assert.Equal(t, 50, cfg.Settings.SnippetLines)
	assert.Equal(t, "dagre-lr", cfg.Settings.DefaultLayout)
	assert.Equal(t, 3, cfg.Settings.DefaultDepth)
}

// TestLoad_PathResolvesAgainstWorkspaceDirNotCWD proves relative service
// paths resolve against the directory containing polyflow.yml — not the
// process's current working directory — per Z.0.
func TestLoad_PathResolvesAgainstWorkspaceDirNotCWD(t *testing.T) {
	wsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "svc-a"), 0o755))
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-a
    path: ./svc-a
    language: go
`)

	// Run from an unrelated CWD to prove resolution ignores it.
	elsewhere := t.TempDir()
	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(elsewhere))
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := workspace.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, filepath.Join(wsDir, "svc-a"), cfg.Services[0].Path)
}

// TestLoad_TildeExpansion proves a leading "~/" expands to the user's home
// directory, and that "~user/"-style expansion is rejected with a named
// message rather than silently mis-resolving.
func TestLoad_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	sub := filepath.Join(home, "polyflow-z0-test-"+t.Name())
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(sub) })

	wsDir := t.TempDir()
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-home
    path: "~/`+filepath.Base(sub)+`"
    language: go
`)
	cfg, err := workspace.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, sub, cfg.Services[0].Path)
}

func TestLoad_TildeUserExpansionErrors(t *testing.T) {
	wsDir := t.TempDir()
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-a
    path: "~someuser/repo"
    language: go
`)
	_, err := workspace.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "~user/-style home-directory expansion is not supported")
}

// TestLoad_NonexistentPathErrors proves a resolved path that does not exist
// fails Load naming both the service and the raw/resolved paths, rather than
// silently indexing an empty directory.
func TestLoad_NonexistentPathErrors(t *testing.T) {
	wsDir := t.TempDir()
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: missing-svc
    path: ./does-not-exist
    language: go
`)
	_, err := workspace.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-svc")
	assert.Contains(t, err.Error(), "does-not-exist")
}

// TestLoad_DuplicateResolvedRootsError proves two services resolving to the
// same directory fail the load — duplicate service roots corrupt
// same-service scoping.
// TestLoad_DuplicateServiceNameError proves two services with the same name
// but different (non-colliding) paths are rejected — name uniqueness was
// previously only implicitly relied on (FR.2: HasService looks services up
// by name).
func TestLoad_DuplicateServiceNameError(t *testing.T) {
	wsDir := t.TempDir()
	dirA := filepath.Join(wsDir, "a")
	dirB := filepath.Join(wsDir, "b")
	require.NoError(t, os.MkdirAll(dirA, 0o755))
	require.NoError(t, os.MkdirAll(dirB, 0o755))
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc
    path: ./a
    language: go
  - name: svc
    path: ./b
    language: go
`)
	_, err := workspace.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "svc")
}

func TestLoad_DuplicateResolvedRootsError(t *testing.T) {
	wsDir := t.TempDir()
	shared := filepath.Join(wsDir, "shared")
	require.NoError(t, os.MkdirAll(shared, 0o755))
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-a
    path: ./shared
    language: go
  - name: svc-b
    path: ./shared
    language: go
`)
	_, err := workspace.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "svc-a")
	assert.Contains(t, err.Error(), "svc-b")
}

// TestLoad_AbsolutePathPassesThroughUnchanged proves an already-absolute
// service path is used as-is (Clean'd) and not re-joined to the workspace
// directory.
func TestLoad_AbsolutePathPassesThroughUnchanged(t *testing.T) {
	svcDir := t.TempDir()
	wsDir := t.TempDir()
	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-abs
    path: "`+filepath.ToSlash(svcDir)+`"
    language: go
`)
	cfg, err := workspace.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, filepath.Clean(svcDir), cfg.Services[0].Path)
}

// TestLoad_SymlinkedServiceRootDereferenced: X.5 fleet workspaces commonly
// point Service.Path at a symlink (eval/.cache/<name> -> an out-of-tree
// repo, the chessleap/Z.2 precedent). filepath.WalkDir treats a symlink
// root as a single non-dir entry and never descends into it, so Load must
// resolve the symlink to its physical target — discovered by indexing a
// real 3-repo fleet workspace where every symlinked service silently
// walked 0 files.
func TestLoad_SymlinkedServiceRootDereferenced(t *testing.T) {
	realDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "marker.go"), []byte("package x"), 0o644))

	wsDir := t.TempDir()
	linkPath := filepath.Join(wsDir, "svc-link")
	require.NoError(t, os.Symlink(realDir, linkPath))

	path := writeYAMLInDir(t, wsDir, `
name: ws
version: "1"
services:
  - name: svc-symlink
    path: ./svc-link
    language: go
`)
	cfg, err := workspace.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)

	resolvedReal, err := filepath.EvalSymlinks(realDir)
	require.NoError(t, err)
	assert.Equal(t, resolvedReal, cfg.Services[0].Path)

	// Prove the fix actually matters: filepath.WalkDir on the raw symlink
	// path (what Load would have stored pre-fix) sees only the symlink
	// itself, not marker.go inside it.
	var sawMarker bool
	_ = filepath.WalkDir(cfg.Services[0].Path, func(p string, d os.DirEntry, err error) error {
		if filepath.Base(p) == "marker.go" {
			sawMarker = true
		}
		return nil
	})
	assert.True(t, sawMarker, "WalkDir on the resolved path must see files inside the symlink target")
}

// TestLoad_SingleRepoRegression loads this repo's own polyflow.yml from
// its own directory (CWD == workspace dir) and asserts resolved service
// paths equal the pre-Z.0 behavior (filepath.Abs from CWD).
func TestLoad_SingleRepoRegression(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	wsPath := filepath.Join(repoRoot, "polyflow.yml")
	if _, err := os.Stat(wsPath); err != nil {
		t.Skipf("repo polyflow.yml not found: %v", err)
	}

	oldWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	cfg, err := workspace.Load(wsPath)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Services)
	for _, svc := range cfg.Services {
		assert.True(t, filepath.IsAbs(svc.Path))
	}
	// Direct equivalence check against the documented pre-Z.0 behavior
	// (filepath.Abs against CWD) for the two known services.
	for _, svc := range cfg.Services {
		var rel string
		switch svc.Name {
		case "polyflow":
			rel = "."
		case "web":
			rel = "./web"
		default:
			continue
		}
		want, err := filepath.Abs(rel)
		require.NoError(t, err)
		assert.Equal(t, want, svc.Path)
	}
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
	path := filepath.Join(dir, "polyflow.yml")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "svc"), 0o755))

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
	path := filepath.Join(dir, "polyflow.yml")
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
	path := "/dev/null/impossible/polyflow.yml"
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

func TestHasService(t *testing.T) {
	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "api"}, {Name: "web"}},
	}
	assert.True(t, cfg.HasService("api"))
	assert.True(t, cfg.HasService("web"))
	assert.False(t, cfg.HasService("missing"))
}
