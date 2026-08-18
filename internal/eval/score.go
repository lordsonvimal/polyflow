package eval

import "math"

// CaseResult holds the scoring outcome for one eval case.
//
// Scoring rule (pinned): a miss that appears in the global unresolved ledger
// counts as HonestMiss — the graph knew about that file but could not resolve
// a reference to/from it. A miss with no such trace is SilentMiss — the
// failure mode the whole project exists to prevent.
type CaseResult struct {
	CaseID string  `json:"case_id"`
	Kind   string  `json:"kind,omitempty"` // "semantic" (S.4) | "rank1" (C.6); empty for impact cases
	Recall float64 `json:"recall"`
	// Precision is a pointer because for most cases there is no such number
	// (D.1). Scoring hits/returned against an expected_impacted list that is a
	// hand-picked sample measures how short the sample is, not how precise the
	// tool is — one corpus reported "precision" against a 2-file sample of 193
	// returned files. Only an `exhaustive: true` case has a complete enough
	// truth set for the ratio to mean anything; everywhere else this stays nil
	// and is omitted from the JSON entirely, so it cannot be quoted by mistake.
	Precision    *float64 `json:"precision,omitempty"`
	Exhaustive   bool     `json:"exhaustive,omitempty"`
	HonestMisses int      `json:"honest_misses"`
	SilentMisses int      `json:"silent_misses"`
	HardFail     bool     `json:"hard_fail"`

	// ForbiddenHits (D.1) are the must_not_include entries the query actually
	// returned — named, not counted, because a precision failure is only
	// actionable if you know which phantom fired.
	ForbiddenHits []string `json:"forbidden_hits,omitempty"`

	// Rank-1 diagnostics (kind=rank1, C.6). Rank1 is what actually came back
	// first, so a failing case names its own usurper. The two scores are
	// recorded even when the case passes: search discrimination on this index is
	// flat enough that the correct hit can win by 0.002, which is a hit waiting
	// to be displaced by an unrelated indexing change, and only a recorded gap
	// makes that visible before it flips.
	Rank1      string  `json:"rank1,omitempty"`
	Rank1Score float64 `json:"rank1_score,omitempty"`
	Rank2      string  `json:"rank2,omitempty"`
	Rank2Score float64 `json:"rank2_score,omitempty"`
}

// ScoreGap is the rank-1 minus rank-2 score for a rank1 case — the margin by
// which the top hit holds its position. Zero when there was no second hit.
func (c CaseResult) ScoreGap() float64 {
	if c.Rank2 == "" {
		return 0
	}
	return c.Rank1Score - c.Rank2Score
}

// Report is the full corpus scoring report for one repository.
type Report struct {
	Repo           string       `json:"repo"`
	Results        []CaseResult `json:"results"`
	Recall         float64      `json:"recall"`                    // macro-average over all cases
	SemanticRecall float64      `json:"semantic_recall,omitempty"` // macro-average over kind=semantic cases (S.4)
	// Precision is the macro-average over `exhaustive: true` cases ONLY (D.1),
	// and is absent when the repo has none. It used to average every case,
	// which made it a measure of corpus authoring effort rather than of the
	// tool. ExhaustiveCases is published beside it so the denominator is never
	// invisible.
	Precision       *float64 `json:"precision,omitempty"`
	ExhaustiveCases int      `json:"exhaustive_cases,omitempty"`
	// ForbiddenHits is the total number of must_not_include violations across
	// the repo's cases — the corpus's only direct false-positive signal.
	ForbiddenHits int `json:"forbidden_hits,omitempty"`
	// Rank1Accuracy is the fraction of kind=rank1 cases whose expected entity
	// came back first (C.6). Separated from Recall for the same reason
	// SemanticRecall is: "did the blast radius reach the right files" and "did
	// the search put the right thing on top" are different failures.
	Rank1Accuracy float64 `json:"rank1_accuracy,omitempty"`
	// Rank1MinGap is the smallest rank1−rank2 score margin among the passing
	// rank1 cases — the thinnest ice the search surface is standing on.
	Rank1MinGap float64 `json:"rank1_min_gap,omitempty"`
	// SemanticWarnings carries the indexer's own "semantic call graph
	// unavailable, falling back to tree-sitter" notices (graph.db meta key
	// semantic_warnings) into the eval report. Without this, a service whose
	// go/packages (or equivalent) pass failed — e.g. because its module cache
	// was never warmed — silently degrades to tree-sitter-only edges and every
	// recall number affected reads as a resolver regression instead of what it
	// actually is: a graph that was never fully built.
	SemanticWarnings []string `json:"semantic_warnings,omitempty"`
}

// Score computes a CaseResult.
//
//   - returned: file paths the impact query produced
//   - expected: hand-verified ground-truth file paths
//   - mustNotMiss: subset of expected whose absence is a hard failure
//   - unresolvedFiles: set of file paths present in the global unresolved ledger
//     (files where the indexer recorded a reference it could not resolve)
func Score(caseID string, returned, expected, mustNotMiss []string, unresolvedFiles map[string]bool) CaseResult {
	retSet := toSet(returned)
	expSet := toSet(expected)

	hitCount := 0
	for f := range expSet {
		if retSet[f] {
			hitCount++
		}
	}

	recall := 0.0
	if len(expSet) > 0 {
		recall = float64(hitCount) / float64(len(expSet))
	}

	honestMisses, silentMisses := 0, 0
	for f := range expSet {
		if !retSet[f] {
			if unresolvedFiles[f] {
				honestMisses++
			} else {
				silentMisses++
			}
		}
	}

	mnmSet := toSet(mustNotMiss)
	hardFail := false
	for f := range mnmSet {
		if !retSet[f] && !unresolvedFiles[f] {
			hardFail = true
			break
		}
	}

	return CaseResult{
		CaseID:       caseID,
		Recall:       recall,
		HonestMisses: honestMisses,
		SilentMisses: silentMisses,
		HardFail:     hardFail,
	}
}

// Precision is hits/returned. Exported as a separate function precisely so that
// computing it is an explicit act at each call site: it is a valid number only
// where `expected` is the complete truth set (D.1), and callers that ask for it
// against a sample are making a claim they have to justify.
func Precision(returned, expected []string) float64 {
	retSet := toSet(returned)
	if len(retSet) == 0 {
		return 0
	}
	expSet := toSet(expected)
	hits := 0
	for f := range retSet {
		if expSet[f] {
			hits++
		}
	}
	return float64(hits) / float64(len(retSet))
}

// ApplyPrecision scores the D.1 precision half of a case onto an already-scored
// CaseResult: the must_not_include violations, and — only for an exhaustive
// case — the precision ratio itself.
//
// A forbidden hit is a hard failure on its own. It is not folded into the
// existing hardFail flag silently: an over-broad blast radius and a missed file
// are opposite defects, and an agent told "unnecessary work" needs a different
// fix from one told "you'll ship a regression".
func ApplyPrecision(cr CaseResult, returned, expected, mustNotInclude []string, exhaustive bool) CaseResult {
	retSet := toSet(returned)
	for _, f := range mustNotInclude {
		if retSet[f] {
			cr.ForbiddenHits = append(cr.ForbiddenHits, f)
		}
	}
	if len(cr.ForbiddenHits) > 0 {
		cr.HardFail = true
	}
	if exhaustive {
		cr.Exhaustive = true
		p := Precision(returned, expected)
		cr.Precision = &p
	}
	return cr
}

// AggregateReport computes corpus-level macro-averaged recall and precision
// from individual case results. SemanticRecall is separately computed over
// kind=semantic cases (S.4).
func AggregateReport(repo string, results []CaseResult) Report {
	if len(results) == 0 {
		return Report{Repo: repo}
	}
	var sumR, sumP float64
	var sumSR, sumRank1 float64
	var nSem, nRank1, nExh, nForbidden int
	minGap := math.Inf(1)
	for _, r := range results {
		sumR += r.Recall
		if r.Exhaustive && r.Precision != nil {
			sumP += *r.Precision
			nExh++
		}
		nForbidden += len(r.ForbiddenHits)
		switch r.Kind {
		case "semantic":
			sumSR += r.Recall
			nSem++
		case "rank1":
			sumRank1 += r.Recall
			nRank1++
			if !r.HardFail && r.Rank2 != "" && r.ScoreGap() < minGap {
				minGap = r.ScoreGap()
			}
		}
	}
	n := float64(len(results))
	rep := Report{
		Repo:            repo,
		Results:         results,
		Recall:          sumR / n,
		ExhaustiveCases: nExh,
		ForbiddenHits:   nForbidden,
	}
	if nExh > 0 {
		p := sumP / float64(nExh)
		rep.Precision = &p
	}
	if nSem > 0 {
		rep.SemanticRecall = sumSR / float64(nSem)
	}
	if nRank1 > 0 {
		rep.Rank1Accuracy = sumRank1 / float64(nRank1)
	}
	if !math.IsInf(minGap, 1) {
		rep.Rank1MinGap = minGap
	}
	return rep
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
