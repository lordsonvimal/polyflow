package eval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

func validManifest() *eval.Manifest {
	return &eval.Manifest{
		Repo: eval.RepoRef{
			Name:      "testrepo",
			URL:       "https://github.com/example/testrepo",
			SHA:       "abc1234",
			Workspace: "workspace.yaml",
		},
		Cases: []eval.Case{
			{
				ID:               "case-one",
				Kind:             "node",
				Target:           "SomeFunc",
				ExpectedImpacted: []string{"foo.go", "bar.go"},
				MustNotMiss:      []string{"foo.go"},
			},
		},
	}
}

func TestValidateManifest_Valid(t *testing.T) {
	errs := eval.ValidateManifest(validManifest())
	assert.Empty(t, errs, "valid manifest should have no errors")
}

func TestValidateManifest_MissingRepoName(t *testing.T) {
	m := validManifest()
	m.Repo.Name = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "repo.name")
}

func TestValidateManifest_MissingSHA(t *testing.T) {
	m := validManifest()
	m.Repo.SHA = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "repo.sha")
}

func TestValidateManifest_MissingWorkspace(t *testing.T) {
	m := validManifest()
	m.Repo.Workspace = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "repo.workspace")
}

func TestValidateManifest_MissingURLAndPath(t *testing.T) {
	m := validManifest()
	m.Repo.URL = ""
	m.Repo.Path = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if e.CaseID == "" {
			found = true
		}
	}
	assert.True(t, found, "expected a repo-level error about missing url/path")
}

// Lint rule: every case must declare at least one must_not_miss file.
func TestValidateManifest_MissingMustNotMiss(t *testing.T) {
	m := validManifest()
	m.Cases[0].MustNotMiss = nil
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Equal(t, "case-one", errs[0].CaseID)
	assert.Contains(t, errs[0].Error(), "must_not_miss")
}

func TestValidateManifest_EmptyExpected(t *testing.T) {
	m := validManifest()
	m.Cases[0].ExpectedImpacted = nil
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "expected_impacted")
}

func TestValidateManifest_UnknownKind(t *testing.T) {
	m := validManifest()
	m.Cases[0].Kind = "unknown"
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "unknown kind")
}

func TestValidateManifest_DuplicateCaseID(t *testing.T) {
	m := validManifest()
	m.Cases = append(m.Cases, m.Cases[0]) // duplicate
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if e.CaseID == "case-one" && e.Message == "duplicate case id" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateManifest_DiffCaseMissingDiffFile(t *testing.T) {
	m := validManifest()
	m.Cases[0].Kind = "diff"
	m.Cases[0].DiffFile = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "diff_file")
}

func TestValidateManifest_NodeCaseMissingTarget(t *testing.T) {
	m := validManifest()
	m.Cases[0].Target = ""
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "target")
}

// A manifest using path: instead of url: is valid (local repo).
func TestValidateManifest_LocalPath(t *testing.T) {
	m := validManifest()
	m.Repo.URL = ""
	m.Repo.Path = "."
	errs := eval.ValidateManifest(m)
	assert.Empty(t, errs)
}

// ── Semantic case (kind=semantic, S.4) validation ─────────────────────────

func validSemanticCase() eval.Case {
	return eval.Case{
		ID:          "sem-one",
		Kind:        "semantic",
		Query:       "unresolved references",
		Section:     "nodes",
		ExpectAnyOf: []string{"UnresolvedNote"},
		MustNotMiss: []string{"UnresolvedNote"},
	}
}

func TestValidateManifest_SemanticCase_Valid(t *testing.T) {
	m := validManifest()
	m.Cases = []eval.Case{validSemanticCase()}
	errs := eval.ValidateManifest(m)
	assert.Empty(t, errs, "valid semantic case should have no errors")
}

func TestValidateManifest_SemanticCase_MissingQuery(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	c.Query = ""
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "query")
}

func TestValidateManifest_SemanticCase_BadSection(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	c.Section = "files"
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "section")
}

func TestValidateManifest_SemanticCase_EmptyExpectAnyOf(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	c.ExpectAnyOf = nil
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "expect_any_of")
}

// expected_impacted is not required for semantic cases.
func TestValidateManifest_SemanticCase_NoExpectedImpacted(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	c.ExpectedImpacted = nil
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	assert.Empty(t, errs, "semantic cases do not require expected_impacted")
}

// must_not_miss is still required for semantic cases.
func TestValidateManifest_SemanticCase_MissingMustNotMiss(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	c.MustNotMiss = nil
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "must_not_miss")
}

// "semantic" is now a known kind — should not trigger unknown-kind error.
func TestValidateManifest_SemanticKindRecognized(t *testing.T) {
	m := validManifest()
	c := validSemanticCase()
	m.Cases = []eval.Case{c}
	errs := eval.ValidateManifest(m)
	for _, e := range errs {
		assert.NotContains(t, e.Error(), "unknown kind")
	}
}

// All cases must have ≥1 must_not_miss — multiple violations reported at once.
func TestValidateManifest_MultipleMissingMustNotMiss(t *testing.T) {
	m := validManifest()
	m.Cases = append(m.Cases, eval.Case{
		ID:               "case-two",
		Kind:             "file",
		Target:           "some/file.go",
		ExpectedImpacted: []string{"other.go"},
		MustNotMiss:      nil, // missing
	})
	errs := eval.ValidateManifest(m)
	require.Len(t, errs, 1, "only case-two should fail")
	assert.Equal(t, "case-two", errs[0].CaseID)
}
