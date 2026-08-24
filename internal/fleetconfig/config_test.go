package fleetconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
)

func writeYAMLInDir(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "fleet.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestLoad_OK(t *testing.T) {
	dir := t.TempDir()
	path := writeYAMLInDir(t, dir, `
name: juniper-fleet
version: "1"
services:
  - name: maple-agent
    git: https://github.com/Nordic/juniper.git
    subpath: maple-agent
    ref: main
    language: go
  - name: willow
    git: https://github.com/Nordic/willow.git
    ref: main
    language: go
links:
  - from: maple-agent
    to: willow
    via: rabbitmq
    exchange: build_jobs
`)
	cfg, err := fleetconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "juniper-fleet", cfg.Name)
	require.Len(t, cfg.Services, 2)
	assert.Equal(t, "maple-agent", cfg.Services[0].Name)
	assert.Equal(t, "maple-agent", cfg.Services[0].Subpath)
	assert.Equal(t, "", cfg.Services[1].Subpath)
	require.Len(t, cfg.Links, 1)
	assert.Equal(t, "build_jobs", cfg.Links[0].Exchange)
	assert.True(t, cfg.HasService("willow"))
	assert.False(t, cfg.HasService("nope"))
}

func TestLoad_MissingGit(t *testing.T) {
	dir := t.TempDir()
	path := writeYAMLInDir(t, dir, `
name: f
version: "1"
services:
  - name: svc
    ref: main
`)
	_, err := fleetconfig.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git is required")
}

func TestLoad_MissingRef(t *testing.T) {
	dir := t.TempDir()
	path := writeYAMLInDir(t, dir, `
name: f
version: "1"
services:
  - name: svc
    git: https://example.com/svc.git
`)
	_, err := fleetconfig.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ref is required")
}

func TestLoad_DuplicateService(t *testing.T) {
	dir := t.TempDir()
	path := writeYAMLInDir(t, dir, `
name: f
version: "1"
services:
  - name: svc
    git: https://example.com/svc.git
    ref: main
  - name: svc
    git: https://example.com/svc2.git
    ref: main
`)
	_, err := fleetconfig.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate service name")
}

func TestLoad_UnknownField(t *testing.T) {
	dir := t.TempDir()
	path := writeYAMLInDir(t, dir, `
name: f
version: "1"
services:
  - name: svc
    git: https://example.com/svc.git
    ref: main
    bogus: true
`)
	_, err := fleetconfig.Load(path)
	require.Error(t, err)
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.yml")
	cfg := &fleetconfig.Config{
		Name:    "f",
		Version: "1",
		Services: []fleetconfig.Service{
			{Name: "svc", Git: "https://example.com/svc.git", Ref: "main", Language: "go"},
		},
	}
	require.NoError(t, fleetconfig.Save(path, cfg))

	got, err := fleetconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, cfg.Name, got.Name)
	require.Len(t, got.Services, 1)
	assert.Equal(t, cfg.Services[0], got.Services[0])
}
