package toolchain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/deps"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestResolveToolchain_GoProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module example.com/svc

go 1.25.0

require (
	github.com/a-h/templ v0.3.1020
	github.com/starfederation/datastar-go v1.1.0
)
`)
	svcDeps := []deps.Dependency{
		{Ecosystem: deps.EcosystemGo, Name: "github.com/a-h/templ", Version: "v0.3.1020", Kind: deps.KindProd},
		{Ecosystem: deps.EcosystemGo, Name: "github.com/starfederation/datastar-go", Version: "v1.1.0", Kind: deps.KindProd},
	}
	versions := ResolveToolchain(dir, svcDeps)

	assert.Equal(t, "1.25.0", versions[ToolGo])
	assert.Equal(t, "0.3.1020", versions[ToolTempl])
	assert.Equal(t, "1.1.0", versions[ToolDatastar])
	assert.Equal(t, "living", versions[ToolHTML])
	assert.Equal(t, "living", versions[ToolJavaScript])
	// TypeScript and Ruby not present.
	_, hasTS := versions[ToolTypeScript]
	assert.False(t, hasTS)
}

func TestResolveToolchain_JSProject(t *testing.T) {
	dir := t.TempDir()
	svcDeps := []deps.Dependency{
		{Ecosystem: deps.EcosystemNPM, Name: "typescript", Version: "5.0.4", Kind: deps.KindDev},
		{Ecosystem: deps.EcosystemNPM, Name: "@starfederation/datastar", Version: "1.0.0", Kind: deps.KindProd},
	}
	versions := ResolveToolchain(dir, svcDeps)

	assert.Equal(t, "5.0.4", versions[ToolTypeScript])
	assert.Equal(t, "1.0.0", versions[ToolDatastar])
	assert.Equal(t, "living", versions[ToolHTML])
	assert.Equal(t, "living", versions[ToolJavaScript])
	// Go not present (no go.mod).
	_, hasGo := versions[ToolGo]
	assert.False(t, hasGo)
}

func TestResolveToolchain_DatastarGoDepPreferredOverNPM(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/svc\n\ngo 1.22.0\n\nrequire github.com/starfederation/datastar-go v1.2.0\n")
	svcDeps := []deps.Dependency{
		{Ecosystem: deps.EcosystemGo, Name: "github.com/starfederation/datastar-go", Version: "v1.2.0", Kind: deps.KindProd},
		{Ecosystem: deps.EcosystemNPM, Name: "@starfederation/datastar", Version: "0.9.0", Kind: deps.KindProd},
	}
	versions := ResolveToolchain(dir, svcDeps)
	// Go dep takes precedence.
	assert.Equal(t, "1.2.0", versions[ToolDatastar])
}

func TestResolveToolchain_RubyVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".ruby-version", "ruby-3.2.0\n")
	versions := ResolveToolchain(dir, nil)
	assert.Equal(t, "3.2.0", versions[ToolRuby])
}

func TestResolveToolchain_RubyVersionNoPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".ruby-version", "3.1.4")
	versions := ResolveToolchain(dir, nil)
	assert.Equal(t, "3.1.4", versions[ToolRuby])
}

func TestResolveToolchain_EmptyProject(t *testing.T) {
	dir := t.TempDir()
	versions := ResolveToolchain(dir, nil)
	// Only living constants present.
	assert.Equal(t, "living", versions[ToolHTML])
	assert.Equal(t, "living", versions[ToolJavaScript])
	assert.Len(t, versions, 2)
}

func TestResolveToolchain_TemplVPrefixStripped(t *testing.T) {
	dir := t.TempDir()
	svcDeps := []deps.Dependency{
		{Ecosystem: deps.EcosystemGo, Name: "github.com/a-h/templ", Version: "v0.3.960", Kind: deps.KindProd},
	}
	versions := ResolveToolchain(dir, svcDeps)
	assert.Equal(t, "0.3.960", versions[ToolTempl])
}

func TestSelectAll_CoverageNoteOnInferred(t *testing.T) {
	reg := Registry{
		ToolDatastar: {{VersionRange: ">=1.0.0", RuleVariant: "datastar-v1"}},
		ToolHTML:     {{VersionRange: "", RuleVariant: "html-living"}},
	}
	versions := map[Tool]string{
		ToolDatastar: "0.5.0", // below >=1.0.0 → inferred
		ToolHTML:     "living",
	}
	selections, notes := SelectAll(reg, "svc-test", versions)

	assert.Len(t, selections, 2)

	require.Len(t, notes, 1)
	assert.Equal(t, "svc-test", notes[0].Service)
	assert.Equal(t, ToolDatastar, notes[0].Tool)
	assert.Equal(t, "0.5.0", notes[0].RequestedVersion)
	assert.Equal(t, "datastar-v1", notes[0].UsedProfile)
}

func TestSelectAll_NoCoverageNotesWhenAllMatch(t *testing.T) {
	reg := DefaultRegistry()
	versions := map[Tool]string{
		ToolHTML:       "living",
		ToolJavaScript: "living",
		ToolGo:         "1.25.0",
		ToolTempl:      "0.3.1020",
		ToolDatastar:   "1.1.0",
	}
	_, notes := SelectAll(reg, "svc-test", versions)
	assert.Empty(t, notes)
}

func TestSelectAll_SidecarBackendInCoverageNote(t *testing.T) {
	reg := Registry{
		ToolTempl: {{VersionRange: ">=0.3.0", SidecarBackend: "templ-v0.3"}},
	}
	// Version below the range → inferred; UsedProfile comes from SidecarBackend.
	versions := map[Tool]string{ToolTempl: "0.2.0"}
	_, notes := SelectAll(reg, "svc", versions)
	require.Len(t, notes, 1)
	assert.Equal(t, "templ-v0.3", notes[0].UsedProfile)
}
