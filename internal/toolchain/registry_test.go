package toolchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSelect_ExactMatch(t *testing.T) {
	reg := Registry{
		ToolDatastar: {{VersionRange: ">=1.0.0", RuleVariant: "datastar-v1"}},
	}
	sel := reg.Select(ToolDatastar, "1.1.0")
	assert.Equal(t, ToolDatastar, sel.Tool)
	assert.Equal(t, "1.1.0", sel.Version)
	assert.Equal(t, "datastar-v1", sel.Backend.RuleVariant)
	assert.False(t, sel.Inferred)
}

func TestSelect_NearestNewestFallback(t *testing.T) {
	// Version 0.9.0 is below the >=1.0.0 threshold → fallback to first (newest) row.
	reg := Registry{
		ToolDatastar: {{VersionRange: ">=1.0.0", RuleVariant: "datastar-v1"}},
	}
	sel := reg.Select(ToolDatastar, "0.9.0")
	assert.True(t, sel.Inferred)
	assert.Equal(t, "datastar-v1", sel.Backend.RuleVariant)
}

func TestSelect_UnknownTool(t *testing.T) {
	reg := Registry{}
	sel := reg.Select("unknown-tool", "1.0.0")
	assert.True(t, sel.Inferred)
	assert.Equal(t, Backend{}, sel.Backend)
}

func TestSelect_CatchAllVersionRange(t *testing.T) {
	// Empty VersionRange is catch-all; matches any version string including "living".
	reg := Registry{
		ToolHTML: {{VersionRange: "", RuleVariant: "html-living"}},
	}
	sel := reg.Select(ToolHTML, "living")
	assert.False(t, sel.Inferred)
	assert.Equal(t, "html-living", sel.Backend.RuleVariant)
}

func TestSelect_MultipleRows_FirstMatchWins(t *testing.T) {
	reg := Registry{
		ToolDatastar: {
			{VersionRange: ">=2.0.0", RuleVariant: "datastar-v2"},
			{VersionRange: ">=1.0.0", RuleVariant: "datastar-v1"},
		},
	}
	// v1.1.0 matches the second row (>=1.0.0), not the first (>=2.0.0).
	sel := reg.Select(ToolDatastar, "1.1.0")
	assert.False(t, sel.Inferred)
	assert.Equal(t, "datastar-v1", sel.Backend.RuleVariant)

	// v2.0.0 matches the first row.
	sel2 := reg.Select(ToolDatastar, "2.0.0")
	assert.False(t, sel2.Inferred)
	assert.Equal(t, "datastar-v2", sel2.Backend.RuleVariant)
}

func TestSelect_SidecarBackend(t *testing.T) {
	reg := Registry{
		ToolTempl: {{VersionRange: ">=0.3.0", SidecarBackend: "templ-v0.3"}},
	}
	sel := reg.Select(ToolTempl, "0.3.1020")
	assert.False(t, sel.Inferred)
	assert.Equal(t, "templ-v0.3", sel.Backend.SidecarBackend)
	assert.Equal(t, "", sel.Backend.RuleVariant)
}

func TestDefaultRegistry_AllToolsPresent(t *testing.T) {
	reg := DefaultRegistry()
	required := []Tool{ToolGo, ToolJavaScript, ToolTypeScript, ToolTempl, ToolDatastar, ToolHTML, ToolRuby}
	for _, tool := range required {
		rows, ok := reg[tool]
		assert.True(t, ok, "tool %q missing from DefaultRegistry", tool)
		assert.NotEmpty(t, rows, "tool %q has no rows", tool)
	}
}

func TestDefaultRegistry_LiveToolsNeverInferred(t *testing.T) {
	reg := DefaultRegistry()
	for _, tool := range []Tool{ToolHTML, ToolJavaScript} {
		sel := reg.Select(tool, "living")
		assert.False(t, sel.Inferred, "tool %q should never be inferred for 'living'", tool)
	}
}

func TestDefaultRegistry_VersionedToolsMatchCurrentVersions(t *testing.T) {
	reg := DefaultRegistry()

	cases := []struct {
		tool    Tool
		version string
	}{
		{ToolGo, "1.25.0"},
		{ToolTypeScript, "5.0.4"},
		{ToolTempl, "0.3.1020"},
		{ToolDatastar, "1.1.0"},
		{ToolRuby, "3.2.0"},
	}
	for _, tc := range cases {
		sel := reg.Select(tc.tool, tc.version)
		assert.False(t, sel.Inferred, "tool %q version %q should not be inferred", tc.tool, tc.version)
	}
}

func TestDefaultRegistry_FutureVersionInferred(t *testing.T) {
	// A hypothetical future version not in any range triggers the fallback.
	reg := DefaultRegistry()
	sel := reg.Select(ToolDatastar, "99.0.0")
	// >=1.0.0 matches 99.0.0 so this should NOT be inferred for the current registry.
	// This test documents the expected behaviour: >=1.0.0 is an open upper bound.
	assert.False(t, sel.Inferred)
}

func TestDefaultRegistry_OldVersionInferred(t *testing.T) {
	// A version below the minimum threshold must trigger nearest-newest fallback.
	reg := DefaultRegistry()

	// Go 1.18 is below >=1.21.
	sel := reg.Select(ToolGo, "1.18.0")
	assert.True(t, sel.Inferred)
	assert.Equal(t, "go-v1", sel.Backend.RuleVariant) // nearest-newest is the only row
}
