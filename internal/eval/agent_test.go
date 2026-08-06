package eval_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

func TestScoreAgentCase_AllRequiredPresent(t *testing.T) {
	c := eval.AgentCase{
		ID:            "case1",
		RequiredFacts: []string{"activity/handlers/play.go", "ui/components/movenotationpanel.templ"},
	}
	answer := "This touches activity/handlers/play.go and ui/components/MoveNotationPanel.templ."
	res := eval.ScoreAgentCase(c, answer)
	assert.True(t, res.Correct)
	assert.Empty(t, res.MissingFacts)
	assert.Empty(t, res.ForbiddenHit)
}

func TestScoreAgentCase_MissingRequiredFact(t *testing.T) {
	c := eval.AgentCase{
		ID:            "case1",
		RequiredFacts: []string{"activity/handlers/play.go", "ui/components/movenotationpanel.templ"},
	}
	res := eval.ScoreAgentCase(c, "This only touches activity/handlers/play.go.")
	assert.False(t, res.Correct)
	assert.Equal(t, []string{"ui/components/movenotationpanel.templ"}, res.MissingFacts)
}

func TestScoreAgentCase_ForbiddenFactHit(t *testing.T) {
	c := eval.AgentCase{
		ID:             "case1",
		RequiredFacts:  []string{"activity/handlers/play.go"},
		ForbiddenFacts: []string{"no other files"},
	}
	res := eval.ScoreAgentCase(c, "activity/handlers/play.go is affected; no other files change.")
	assert.False(t, res.Correct)
	assert.Equal(t, []string{"no other files"}, res.ForbiddenHit)
}

func TestScoreAgentCase_BasenameSuffixMatchForward(t *testing.T) {
	// Fact is a full path; answer gives a shorter, less-qualified suffix of it.
	c := eval.AgentCase{ID: "c", RequiredFacts: []string{"activity/handlers/play.go"}}
	res := eval.ScoreAgentCase(c, "The relevant file is handlers/play.go.")
	assert.True(t, res.Correct)
}

func TestScoreAgentCase_BasenameSuffixDoesNotMatchReverse(t *testing.T) {
	// Fact is a short suffix; answer gives a longer, more-qualified path.
	// The reverse direction is deliberately excluded per the T.2 matching spec.
	c := eval.AgentCase{ID: "c", RequiredFacts: []string{"handlers/play.go"}}
	res := eval.ScoreAgentCase(c, "The relevant file is activity/handlers/play.go.")
	assert.False(t, res.Correct)
	assert.Equal(t, []string{"handlers/play.go"}, res.MissingFacts)
}

func TestScoreAgentCase_CaseInsensitiveAndBackslashNormalized(t *testing.T) {
	c := eval.AgentCase{ID: "c", RequiredFacts: []string{"activity/handlers/play.go"}}
	res := eval.ScoreAgentCase(c, `Touches ACTIVITY\HANDLERS\PLAY.GO`)
	assert.True(t, res.Correct)
}

func TestRunAgentCorpus_NoAgentCasesIsEmptyReport(t *testing.T) {
	dir := t.TempDir()
	manifest := "repo:\n  name: fixture\n  path: .\n  sha: deadbeef\n  workspace: workspace.yaml\ncases: []\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644))

	report, err := eval.RunAgentCorpus(context.Background(), eval.AgentRunOptions{CorpusDir: dir})
	require.NoError(t, err)
	assert.Equal(t, "fixture", report.Repo)
	assert.Empty(t, report.Results)
}

func TestRunAllAgent_UnavailableAgentCLISkips(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "fixture")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	manifest := "repo:\n  name: fixture\n  path: .\n  sha: deadbeef\n  workspace: workspace.yaml\ncases: []\nagent_cases:\n  - id: q1\n    question: \"What files?\"\n    required_facts:\n      - a.go\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "manifest.yaml"), []byte(manifest), 0o644))

	_, err := eval.RunAllAgent(context.Background(), root, eval.AgentRunOptions{
		AgentCmd: "definitely-not-a-real-agent-cli-binary --foo {mcp_config} {max_turns}",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, eval.ErrAgentCLIUnavailable))
}

func TestRunAllAgent_SkipsCorporaWithNoAgentCases(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "fixture")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	manifest := "repo:\n  name: fixture\n  path: .\n  sha: deadbeef\n  workspace: workspace.yaml\ncases: []\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "manifest.yaml"), []byte(manifest), 0o644))

	report, err := eval.RunAllAgent(context.Background(), root, eval.AgentRunOptions{})
	require.NoError(t, err)
	assert.Empty(t, report.Reports)
	assert.Empty(t, report.Skipped)
}

func TestRunAgentCorpus_UnavailableAgentCLISkips(t *testing.T) {
	dir := t.TempDir()
	manifest := "repo:\n  name: fixture\n  path: .\n  sha: deadbeef\n  workspace: workspace.yaml\ncases: []\nagent_cases:\n  - id: q1\n    question: \"What files?\"\n    required_facts:\n      - a.go\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644))

	_, err := eval.RunAgentCorpus(context.Background(), eval.AgentRunOptions{
		CorpusDir: dir,
		AgentCmd:  "definitely-not-a-real-agent-cli-binary --foo {mcp_config} {max_turns}",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, eval.ErrAgentCLIUnavailable))
}
