package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func serviceNames(cfg *WorkspaceConfig) []string {
	out := make([]string, len(cfg.Services))
	for i, s := range cfg.Services {
		out[i] = s.Name
	}
	return out
}

func findService(cfg *WorkspaceConfig, name string) *Service {
	for i := range cfg.Services {
		if cfg.Services[i].Name == name {
			return &cfg.Services[i]
		}
	}
	return nil
}

func TestDiscover_GoWorkMultiModule(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.work", "go 1.22\n\nuse (\n\t./core\n\t./sis\n)\n")
	write(t, root, "core/go.mod", "module example.com/core\n\ngo 1.22\n\nrequire github.com/gin-gonic/gin v1.10.0\n")
	write(t, root, "sis/go.mod", "module example.com/sis\n\ngo 1.22\n")

	cfg, err := Discover(root)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"core", "sis"}, serviceNames(cfg))

	core := findService(cfg, "core")
	require.NotNil(t, core)
	assert.Equal(t, "go", core.Language)
	assert.Equal(t, "./core", core.Path)
	assert.Contains(t, core.Frameworks, "gin")
}

func TestDiscover_YarnWorkspacesWithPortal(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"private": true, "workspaces": ["apps/*", "packages/*"]}`)
	write(t, root, "apps/web/package.json", `{"name": "web", "dependencies": {"react": "^19.0.0", "@org/theme": "portal:../../packages/theme"}}`)
	write(t, root, "packages/theme/package.json", `{"name": "@org/theme"}`)
	write(t, root, "apps/web/tsconfig.json", `{}`)

	cfg, err := Discover(root)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"web", "theme"}, serviceNames(cfg))

	web := findService(cfg, "web")
	require.NotNil(t, web)
	assert.Equal(t, "typescript", web.Language)
	assert.Contains(t, web.Frameworks, "react")

	require.Len(t, cfg.Links, 1)
	assert.Equal(t, "web", cfg.Links[0].From)
	assert.Equal(t, "theme", cfg.Links[0].To)
	assert.Equal(t, "portal", cfg.Links[0].Via)
}

func TestDiscover_RailsApp(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Gemfile", `source "https://rubygems.org"
gem "rails"
gem "delayed_job_active_record"
gem "pusher"
`)
	cfg, err := Discover(root)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	svc := cfg.Services[0]
	assert.Equal(t, "ruby", svc.Language)
	assert.Equal(t, ".", svc.Path)
	assert.Contains(t, svc.Frameworks, "rails")
	assert.Contains(t, svc.Frameworks, "pusher")
}

func TestDiscover_NxProjectJSON(t *testing.T) {
	root := t.TempDir()
	write(t, root, "apps/tether/project.json", `{"name": "tether"}`)
	write(t, root, "apps/tether/package.json", `{"name": "tether"}`)

	cfg, err := Discover(root)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, "tether", cfg.Services[0].Name)
}

func TestDiscover_SkipsNodeModulesAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/app\n\ngo 1.22\n")
	write(t, root, "node_modules/dep/package.json", `{"name": "dep"}`)
	write(t, root, "vendor/lib/go.mod", "module vendored\n")

	cfg, err := Discover(root)
	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	assert.Equal(t, ".", cfg.Services[0].Path, "paths must be relative, never absolute")
}

func TestDiscover_Empty(t *testing.T) {
	cfg, err := Discover(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, cfg.Services)
}
