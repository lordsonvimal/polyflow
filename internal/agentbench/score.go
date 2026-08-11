package agentbench

import "github.com/lordsonvimal/polyflow/internal/eval"

// ScoreTranscript extracts file paths from the transcript result text and
// scores them against the eval ground truth using the standard scorer.
// unresolvedFiles may be nil (not available for agent-bench runs).
//
// mustNotInclude is the precision half, and it is what makes a "does changing X
// break Y" case answerable with *no*. Without it a regression-safety task can
// only ever be a yes-case, and an agent that hedges "everything is connected"
// scores a perfect recall on every one of them.
func ScoreTranscript(caseID string, t Transcript, expected, mustNotMiss, mustNotInclude []string) eval.CaseResult {
	extracted := ExtractFiles(t.Result)
	cr := eval.Score(caseID, extracted, expected, mustNotMiss, nil)
	for _, forbidden := range mustNotInclude {
		for _, got := range extracted {
			if got == forbidden {
				cr.HardFail = true
				return cr
			}
		}
	}
	return cr
}

// TranscriptPrecision is hits/mentioned for one arm's answer. Unlike the corpus
// precision suppressed by D.1, this one is defensible without an exhaustive
// truth set: both arms are scored against the same `expected` list, so it
// compares arms rather than claiming an absolute rate. Read it only that way.
func TranscriptPrecision(t Transcript, expected []string) float64 {
	return eval.Precision(ExtractFiles(t.Result), expected)
}
