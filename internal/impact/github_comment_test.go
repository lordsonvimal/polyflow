package impact_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

func TestFormatGitHubComment_ContainsRequiredSections(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 16}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false, 0)
	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 20, Name: "dynCall", Kind: "call_ref"},
	})

	md := impact.FormatGitHubComment(out, 0)

	// Header section
	assert.Contains(t, md, "## Polyflow Impact")
	assert.Contains(t, md, "changed file")
	assert.Contains(t, md, "changed node")
	assert.Contains(t, md, "in blast radius")

	// Changed nodes section
	assert.Contains(t, md, "### Changed nodes")
	assert.Contains(t, md, "handleUser")

	// Blast radius table with column headers
	assert.Contains(t, md, "### Blast radius")
	assert.Contains(t, md, "| Depth | File | Nodes | Via | Service |")
	assert.Contains(t, md, "main.go")
	assert.Contains(t, md, "api.js")

	// Verification section — always present
	assert.Contains(t, md, "### Verification")

	// Unresolved section — always present
	assert.Contains(t, md, "### Unresolved references")
	assert.Contains(t, md, "dynCall")

	// Unmapped section — always present
	assert.Contains(t, md, "### Unmapped hunks")
	assert.Contains(t, md, "None — all changed spans are mapped")
}

func TestFormatGitHubComment_NoChanges(t *testing.T) {
	idx := diffFixtureIndex()
	out := impact.BuildDiff(idx, nil, 10, "", false, 0)

	md := impact.FormatGitHubComment(out, 0)

	assert.Contains(t, md, "## Polyflow Impact")
	assert.Contains(t, md, "0 changed file")
	// No changed nodes section when empty.
	assert.NotContains(t, md, "### Changed nodes")
	assert.Contains(t, md, "### Blast radius")
	assert.Contains(t, md, "No files in blast radius")
	// Always-present sections still appear.
	assert.Contains(t, md, "### Verification")
	assert.Contains(t, md, "### Unresolved references")
	assert.Contains(t, md, "### Unmapped hunks")
}

func TestFormatGitHubComment_UnmappedHunksAlwaysPresent(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "README.md", Spans: []gitdiff.Span{{Start: 1, End: 3}}},
		{Path: "gone.go", Deleted: true},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false, 0)

	md := impact.FormatGitHubComment(out, 0)

	assert.Contains(t, md, "### Unmapped hunks")
	assert.Contains(t, md, "README.md")
	assert.Contains(t, md, "gone.go")
	assert.Contains(t, md, "deleted")
}

func TestFormatGitHubComment_SizeCapTrimsFilesTableNotFooter(t *testing.T) {
	// Build a result with 60 callers each in a unique file so the table is large.
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "root", Type: graph.NodeTypeFunction, Label: "root", Service: "svc", File: "root.go", Line: 1, Meta: map[string]string{"end_line": "100"}})
	for i := 0; i < 60; i++ {
		callerID := fmt.Sprintf("caller_%d", i)
		// Each caller in a unique deep file path so every caller is a distinct rollup row.
		fileName := fmt.Sprintf("internal/service/subsystem/module/pkg/handler_%d.go", i)
		idx.AddNode(&graph.Node{
			ID: callerID, Type: graph.NodeTypeFunction, Label: callerID,
			Service: "svc", File: fileName, Line: i + 10,
		})
		idx.AddEdge(&graph.Edge{ID: "e" + callerID, From: callerID, To: "root", Type: graph.EdgeTypeCalls})
	}

	changes := []gitdiff.FileChange{
		{Path: "root.go", Spans: []gitdiff.Span{{Start: 5, End: 5}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false, 0)
	require.Equal(t, 60, len(out.Callers), "setup: need 60 distinct file rows")

	// Use a small maxBytes to force trimming (footer ~500 bytes + header ~200 bytes).
	md := impact.FormatGitHubComment(out, 1500)

	// The omission note must be present when files were trimmed.
	require.Contains(t, md, "omitted", "expected omission note when table is trimmed")

	// Footer sections must never be trimmed — always present even when files are cut.
	assert.Contains(t, md, "### Verification")
	assert.Contains(t, md, "### Unresolved references")
	assert.Contains(t, md, "### Unmapped hunks")
}

func TestFormatGitHubComment_Determinism(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 16}}},
	}
	out1 := impact.BuildDiff(idx, changes, 10, "", false, 0)
	out1.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 20, Name: "dynCall", Kind: "call_ref"},
	})

	out2 := impact.BuildDiff(idx, changes, 10, "", false, 0)
	out2.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "handler.go", Line: 20, Name: "dynCall", Kind: "call_ref"},
	})

	md1 := impact.FormatGitHubComment(out1, 0)
	md2 := impact.FormatGitHubComment(out2, 0)
	assert.Equal(t, md1, md2, "two runs on the same input must produce byte-identical output")
}

func TestFormatGitHubComment_CrossServiceTriggersAbsentWhenNone(t *testing.T) {
	// A single-service change produces no cross-service triggers section.
	idx := diffFixtureIndex()
	// Change main.go: only be:main is a target; its only ancestor is nothing.
	changes := []gitdiff.FileChange{
		{Path: "main.go", Spans: []gitdiff.Span{{Start: 4, End: 4}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false, 0)

	md := impact.FormatGitHubComment(out, 0)
	assert.NotContains(t, md, "### Cross-service triggers",
		"section must be absent when no cross-service triggers exist")
}

func TestFormatGitHubComment_FitsUnderGitHubLimit(t *testing.T) {
	idx := diffFixtureIndex()
	changes := []gitdiff.FileChange{
		{Path: "handler.go", Spans: []gitdiff.Span{{Start: 15, End: 16}}},
	}
	out := impact.BuildDiff(idx, changes, 10, "", false, 0)

	md := impact.FormatGitHubComment(out, 0) // default = GitHubCommentMaxBytes
	assert.LessOrEqual(t, len(md), impact.GitHubCommentMaxBytes+1000,
		"default render must fit under GitHub comment limit (footer may slightly exceed)")
}
