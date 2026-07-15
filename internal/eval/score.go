package eval

// CaseResult holds the scoring outcome for one eval case.
//
// Scoring rule (pinned): a miss that appears in the global unresolved ledger
// counts as HonestMiss — the graph knew about that file but could not resolve
// a reference to/from it. A miss with no such trace is SilentMiss — the
// failure mode the whole project exists to prevent.
type CaseResult struct {
	CaseID       string
	Recall       float64 // |returned ∩ expected| / |expected|
	Precision    float64 // |returned ∩ expected| / |returned|
	HonestMisses int     // expected files missed AND present in the unresolved ledger
	SilentMisses int     // expected files missed with no trace in any ledger
	HardFail     bool    // any must_not_miss file silently missed
}

// Report is the full corpus scoring report for one repository.
type Report struct {
	Repo      string
	Results   []CaseResult
	Recall    float64 // macro-average over cases
	Precision float64 // macro-average over cases
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
	precision := 0.0
	if len(retSet) > 0 {
		precision = float64(hitCount) / float64(len(retSet))
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
		Precision:    precision,
		HonestMisses: honestMisses,
		SilentMisses: silentMisses,
		HardFail:     hardFail,
	}
}

// AggregateReport computes corpus-level macro-averaged recall and precision
// from individual case results.
func AggregateReport(repo string, results []CaseResult) Report {
	if len(results) == 0 {
		return Report{Repo: repo}
	}
	var sumR, sumP float64
	for _, r := range results {
		sumR += r.Recall
		sumP += r.Precision
	}
	n := float64(len(results))
	return Report{
		Repo:      repo,
		Results:   results,
		Recall:    sumR / n,
		Precision: sumP / n,
	}
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
