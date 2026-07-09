package patterns_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func gatedFile(pkg, rng string) *patterns.PatternFile {
	return &patterns.PatternFile{
		Language: "go",
		Package:  pkg, VersionRange: rng,
		Patterns: []patterns.Pattern{{
			Name:  "gated_call",
			Query: `(call_expression function: (identifier) @callee)`,
		}},
	}
}

func TestForService_VersionGating(t *testing.T) {
	reg := patterns.NewRegistry()
	reg.RegisterFile(gatedFile("github.com/aws/aws-sdk-go", ">=1.0.0 <2.0.0"))
	reg.RegisterFile(&patterns.PatternFile{
		Language: "go",
		Patterns: []patterns.Pattern{{Name: "ungated", Query: `(func_literal)`}},
	})

	t.Run("v1 service activates v1 pattern", func(t *testing.T) {
		svc := reg.ForService([]deps.Dependency{
			{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go", Version: "v1.55.8"},
		})
		names := patternNames(svc.List("go"))
		assert.Contains(t, names, "gated_call")
		assert.Contains(t, names, "ungated")
	})

	t.Run("v2 version does not satisfy v1 range", func(t *testing.T) {
		svc := reg.ForService([]deps.Dependency{
			{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go", Version: "v2.1.0"},
		})
		names := patternNames(svc.List("go"))
		assert.NotContains(t, names, "gated_call")
		assert.Contains(t, names, "ungated", "ungated patterns always active")
	})

	t.Run("missing dependency deactivates gated pattern", func(t *testing.T) {
		svc := reg.ForService(nil)
		names := patternNames(svc.List("go"))
		assert.NotContains(t, names, "gated_call")
	})

	t.Run("package gate without range requires presence only", func(t *testing.T) {
		reg2 := patterns.NewRegistry()
		reg2.RegisterFile(gatedFile("github.com/gin-gonic/gin", ""))
		svc := reg2.ForService([]deps.Dependency{
			{Ecosystem: "go", Name: "github.com/gin-gonic/gin", Version: "v1.10.0"},
		})
		assert.Contains(t, patternNames(svc.List("go")), "gated_call")
	})
}

func TestVersionInRange(t *testing.T) {
	cases := []struct {
		version, rng string
		want         bool
	}{
		{"v1.55.8", ">=1.0.0 <2.0.0", true},
		{"1.55.8", ">=1.0.0 <2.0.0", true},
		{"v2.0.1", ">=1.0.0 <2.0.0", false},
		{"v2.0.1", ">=2.0.0", true},
		{"19.1.0", ">=18.0.0", true},
		{"17.0.2", ">=18.0.0", false},
		{"not-a-version", ">=1.0.0", false}, // fail closed
		{"1.2.3", "not-a-range", false},     // fail closed
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, patterns.VersionInRange(tc.version, tc.rng),
			"version %s range %s", tc.version, tc.rng)
	}
}

func TestMatcherForService_StampsVersionMetadata(t *testing.T) {
	reg := patterns.NewRegistry()
	reg.RegisterFile(&patterns.PatternFile{
		Language: "go",
		Package:  "github.com/aws/aws-sdk-go", VersionRange: ">=1.0.0 <2.0.0",
		Patterns: []patterns.Pattern{{
			Name: "s3_new",
			Query: `(call_expression
  function: (selector_expression field: (field_identifier) @fn (#eq? @fn "New"))
  arguments: (argument_list (identifier) @sess))`,
		}},
	})
	svcDeps := []deps.Dependency{
		{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go", Version: "v1.55.8"},
	}
	m := patterns.NewTreeSitterMatcherForService(reg, svcDeps)
	src := []byte("package p\nfunc f() { svc := s3.New(sess); _ = svc }\n")
	results, err := m.Match("go", "main.go", src)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "github.com/aws/aws-sdk-go", results[0].Package)
	assert.Equal(t, "v1.55.8", results[0].ResolvedVersion)

	nodes, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 1)
	assert.Equal(t, "github.com/aws/aws-sdk-go", nodes[0].Meta["package"])
	assert.Equal(t, "v1.55.8", nodes[0].Meta["resolved_version"])
}

func patternNames(ps []*patterns.Pattern) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

// TestAWSSDKGating loads the real shipped AWS pattern files and proves the
// gating level of the version split: a service pinning SDK v1 activates only
// the v1 file; a service on SDK v2 activates only the v2 file.
func TestAWSSDKGating(t *testing.T) {
	v1, err := patterns.LoadFile("../../patterns/go/aws_s3_v1.yaml")
	require.NoError(t, err)
	v2, err := patterns.LoadFile("../../patterns/go/aws_s3_v2.yaml")
	require.NoError(t, err)

	reg := patterns.NewRegistry()
	reg.RegisterFile(v1)
	reg.RegisterFile(v2)

	agent := reg.ForService([]deps.Dependency{ // dsw-agent: SDK v1
		{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go", Version: "v1.55.8"},
	})
	agentNames := patternNames(agent.List("go"))
	assert.Contains(t, agentNames, "s3_operation_v1")
	assert.NotContains(t, agentNames, "s3_operation_v2")

	manager := reg.ForService([]deps.Dependency{ // dsw-manager: SDK v2
		{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go-v2/service/s3", Version: "v1.66.0"},
	})
	managerNames := patternNames(manager.List("go"))
	assert.Contains(t, managerNames, "s3_operation_v2")
	assert.NotContains(t, managerNames, "s3_operation_v1")

	// Hypothetical future: aws-sdk-go bumped to v2.x — v1 range no longer satisfied.
	bumped := reg.ForService([]deps.Dependency{
		{Ecosystem: "go", Name: "github.com/aws/aws-sdk-go", Version: "v2.0.0"},
	})
	assert.NotContains(t, patternNames(bumped.List("go")), "s3_operation_v1")
}
