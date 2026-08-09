package agentbench

import "github.com/lordsonvimal/polyflow/internal/eval"

// ScoreTranscript extracts file paths from the transcript result text and
// scores them against the eval ground truth using the standard scorer.
// unresolvedFiles may be nil (not available for agent-bench runs).
func ScoreTranscript(caseID string, t Transcript, expected, mustNotMiss []string) eval.CaseResult {
	extracted := ExtractFiles(t.Result)
	return eval.Score(caseID, extracted, expected, mustNotMiss, nil)
}

// TranscriptPrecision is hits/mentioned for one arm's answer. Unlike the corpus
// precision suppressed by D.1, this one is defensible without an exhaustive
// truth set: both arms are scored against the same `expected` list, so it
// compares arms rather than claiming an absolute rate. Read it only that way.
func TranscriptPrecision(t Transcript, expected []string) float64 {
	return eval.Precision(ExtractFiles(t.Result), expected)
}
