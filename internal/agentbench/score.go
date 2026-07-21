package agentbench

import "github.com/lordsonvimal/polyflow/internal/eval"

// ScoreTranscript extracts file paths from the transcript result text and
// scores them against the eval ground truth using the standard scorer.
// unresolvedFiles may be nil (not available for agent-bench runs).
func ScoreTranscript(caseID string, t Transcript, expected, mustNotMiss []string) eval.CaseResult {
	extracted := ExtractFiles(t.Result)
	return eval.Score(caseID, extracted, expected, mustNotMiss, nil)
}
