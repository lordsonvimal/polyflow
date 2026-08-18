package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Regression records a single regression detected by CheckGate.
type Regression struct {
	Repo           string  `json:"repo"`
	CaseID         string  `json:"case_id"`
	Reason         string  `json:"reason"` // "hard_fail" | "recall_drop" | "silent_miss_rise" | "missing_repo" | "corpus_error" | "forbidden_hit" | "precision_drop"
	BaselineRecall float64 `json:"baseline_recall,omitempty"`
	CurrentRecall  float64 `json:"current_recall,omitempty"`
	BaselineSilent int     `json:"baseline_silent,omitempty"`
	CurrentSilent  int     `json:"current_silent,omitempty"`
	// BaselinePrecision/CurrentPrecision carry condition 7's numbers. Pointers
	// for the same reason CaseResult.Precision is one: a case that is not
	// exhaustive has no precision, and 0.0 is a very different claim from
	// "not measured".
	BaselinePrecision *float64 `json:"baseline_precision,omitempty"`
	CurrentPrecision  *float64 `json:"current_precision,omitempty"`
	// ForbiddenHits names the must_not_include files that are newly returned
	// (D.1) — the point of the condition is which phantom appeared, not that
	// one did.
	ForbiddenHits []string `json:"forbidden_hits,omitempty"`
	// SemanticWarnings names the indexer's own "semantic call graph
	// unavailable" notices that are new versus baseline (condition 8).
	SemanticWarnings []string `json:"semantic_warnings,omitempty"`
}

// GateResult holds the outcome of comparing a run against the baseline.
type GateResult struct {
	// Regressions lists every detected regression, with repo and case IDs.
	Regressions []Regression `json:"regressions"`
	// OK is true when no regression was detected.
	OK bool `json:"ok"`
}

// CheckGate compares current against baseline and returns all regressions.
//
// Pinned failure conditions:
//  1. any HardFail that is NEW — case was not HardFail in baseline but is now.
//     Cases that were already HardFail in the baseline are pre-existing failures,
//     not regressions; they do not trip the gate.
//  2. per-repo aggregate recall drops below baseline recall.
//  3. per-case silent-miss count rises above baseline.
//  4. a repo present in the baseline is absent from the current run — a repo
//     that fails to clone or crashes during indexing must read as a failure,
//     not as a silent pass (its cases were never compared).
//  5. a corpus that was present but failed to run.
//  6. a case returns a must_not_include file it did not return in the baseline
//     (D.1) — the false-positive condition, and the only one that can fail a
//     case for being too broad rather than too narrow.
//  7. an `exhaustive` case's precision falls below its baseline (D.4). Without
//     this, condition 6 is the only precision gate there is, and it can only
//     catch a phantom somebody predicted by name. An exhaustive case declares
//     its expected_impacted to be the COMPLETE truth set, so any file it
//     returns beyond that set is by definition a false positive — this
//     condition is what turns that declaration into an assertion, and it
//     catches the phantom nobody thought to forbid.
//  8. a NEW semantic-analysis fallback warning (indexer meta key
//     semantic_warnings). When a service's semantic pass (e.g. go/packages)
//     fails, the indexer silently degrades to tree-sitter-only edges and
//     every recall number for that repo looks like an ordinary resolver
//     regression — conditions 2/3 alone would just report a recall drop with
//     no indication the graph itself was never fully built. A warning already
//     present in the baseline is not re-flagged here, matching condition 6's
//     "new, not existing" rule.
func CheckGate(current, baseline *MultiReport) *GateResult {
	// Index baseline by repo → caseID.
	type baselineKey struct{ repo, caseID string }
	baselineCases := make(map[baselineKey]CaseResult)
	baselineRepoRecall := make(map[string]float64)
	baselineWarnings := make(map[string]map[string]bool)
	for _, rep := range baseline.Reports {
		baselineRepoRecall[rep.Repo] = rep.Recall
		for _, cr := range rep.Results {
			baselineCases[baselineKey{rep.Repo, cr.CaseID}] = cr
		}
		if len(rep.SemanticWarnings) > 0 {
			set := make(map[string]bool, len(rep.SemanticWarnings))
			for _, w := range rep.SemanticWarnings {
				set[w] = true
			}
			baselineWarnings[rep.Repo] = set
		}
	}

	var regressions []Regression
	for _, rep := range current.Reports {
		// Condition 8: a NEW semantic-analysis fallback warning.
		if len(rep.SemanticWarnings) > 0 {
			known := baselineWarnings[rep.Repo]
			var fresh []string
			for _, w := range rep.SemanticWarnings {
				if !known[w] {
					fresh = append(fresh, w)
				}
			}
			if len(fresh) > 0 {
				regressions = append(regressions, Regression{
					Repo:             rep.Repo,
					CaseID:           "*",
					Reason:           "semantic_fallback",
					SemanticWarnings: fresh,
				})
			}
		}
		// Condition 6: a NEW must_not_include violation (D.1).
		//
		// Checked as a set difference rather than "any hit at all", for the same
		// reason condition 1 exempts a pre-existing hard fail: a case authored
		// against a known-live false positive is a recorded defect, and a gate
		// that a recorded defect blocks forever is a gate people delete. What
		// must never pass silently is a phantom that was not there before —
		// including one appearing on a case that is already red for a recall
		// reason, which condition 1 skips over.
		//
		// Evaluated before condition 1 because a forbidden hit sets HardFail
		// too, and reporting the same defect twice under two names inflates the
		// regression count that `polyflow doctor` shows.
		freshForbidden := make(map[string]bool)
		for _, cr := range rep.Results {
			if len(cr.ForbiddenHits) == 0 {
				continue
			}
			base := baselineCases[baselineKey{rep.Repo, cr.CaseID}]
			known := toSet(base.ForbiddenHits)
			var fresh []string
			for _, f := range cr.ForbiddenHits {
				if !known[f] {
					fresh = append(fresh, f)
				}
			}
			if len(fresh) > 0 {
				freshForbidden[cr.CaseID] = true
				regressions = append(regressions, Regression{
					Repo:          rep.Repo,
					CaseID:        cr.CaseID,
					Reason:        "forbidden_hit",
					ForbiddenHits: fresh,
				})
			}
		}

		// Condition 7: an exhaustive case's precision fell (D.4).
		//
		// Skipped when condition 6 already fired on the case: a forbidden hit
		// is a returned file that expected_impacted cannot contain (the
		// manifest lint forbids the overlap), so it necessarily drags precision
		// down too. Reporting both would name one defect twice, the same
		// reasoning that puts condition 6 ahead of condition 1.
		//
		// A case with no baseline entry is skipped rather than treated as a
		// drop — newly authored exhaustive cases establish a baseline, they do
		// not regress against one.
		for _, cr := range rep.Results {
			if !cr.Exhaustive || cr.Precision == nil || freshForbidden[cr.CaseID] {
				continue
			}
			base, found := baselineCases[baselineKey{rep.Repo, cr.CaseID}]
			if !found || base.Precision == nil {
				continue
			}
			if *cr.Precision < *base.Precision-1e-9 {
				basePrec, curPrec := *base.Precision, *cr.Precision
				regressions = append(regressions, Regression{
					Repo:              rep.Repo,
					CaseID:            cr.CaseID,
					Reason:            "precision_drop",
					BaselinePrecision: &basePrec,
					CurrentPrecision:  &curPrec,
				})
			}
		}

		// Condition 1: new HardFail per case.
		for _, cr := range rep.Results {
			if !cr.HardFail || freshForbidden[cr.CaseID] {
				continue
			}
			key := baselineKey{rep.Repo, cr.CaseID}
			if base, found := baselineCases[key]; found && base.HardFail {
				// Pre-existing HardFail — not a new regression.
				continue
			}
			regressions = append(regressions, Regression{
				Repo:   rep.Repo,
				CaseID: cr.CaseID,
				Reason: "hard_fail",
			})
		}

		// Condition 2: per-repo recall drop.
		if baseRec, ok := baselineRepoRecall[rep.Repo]; ok {
			if rep.Recall < baseRec-1e-9 {
				regressions = append(regressions, Regression{
					Repo:           rep.Repo,
					CaseID:         "*",
					Reason:         "recall_drop",
					BaselineRecall: baseRec,
					CurrentRecall:  rep.Recall,
				})
			}
		}

		// Condition 3: per-case silent-miss count rise.
		for _, cr := range rep.Results {
			key := baselineKey{rep.Repo, cr.CaseID}
			if base, found := baselineCases[key]; found {
				if cr.SilentMisses > base.SilentMisses {
					regressions = append(regressions, Regression{
						Repo:           rep.Repo,
						CaseID:         cr.CaseID,
						Reason:         "silent_miss_rise",
						BaselineSilent: base.SilentMisses,
						CurrentSilent:  cr.SilentMisses,
					})
				}
			}
		}
	}

	// Condition 4: baseline repo missing from the current run. Path-based
	// (local-only) repos that were explicitly skipped are exempt — CI cannot
	// clone a private local repo, so its absence is an expected skip. URL
	// repos are never exempt: a failed clone/index must fail the gate.
	currentRepos := make(map[string]bool, len(current.Reports))
	for _, rep := range current.Reports {
		currentRepos[rep.Repo] = true
	}
	skippedLocalOnly := make(map[string]bool, len(current.Skipped))
	for _, s := range current.Skipped {
		if s.LocalOnly {
			skippedLocalOnly[s.Name] = true
		}
	}
	for _, rep := range baseline.Reports {
		if !currentRepos[rep.Repo] && !skippedLocalOnly[rep.Repo] {
			regressions = append(regressions, Regression{
				Repo:   rep.Repo,
				CaseID: "*",
				Reason: "missing_repo",
			})
		}
	}

	// Condition 5: a corpus that was present but failed to run. Never exempt —
	// local-only status excuses an absent repo, not a broken one. Without this
	// a corpus can stop measuring anything while the gate still exits 0.
	for _, b := range current.Broken {
		regressions = append(regressions, Regression{
			Repo:   b.Name,
			CaseID: "*",
			Reason: "corpus_error",
		})
	}

	return &GateResult{
		Regressions: regressions,
		OK:          len(regressions) == 0,
	}
}

// LoadBaseline reads and parses a MultiReport baseline JSON file.
func LoadBaseline(path string) (*MultiReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read baseline %s: %w", path, err)
	}
	var r MultiReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	return &r, nil
}

// EvalSummary is a compact representation of the last eval run, used by
// polyflow doctor to render the eval row without re-running the corpus.
type EvalSummary struct {
	GeneratedAt string  `json:"generated_at"`
	Repos       int     `json:"repos"`
	TotalCases  int     `json:"total_cases"`
	AvgRecall   float64 `json:"avg_recall"`
	HardFails   int     `json:"hard_fails"`
	SilentMiss  int     `json:"silent_miss"`
	HonestMiss  int     `json:"honest_miss"`
	Regressions int     `json:"regressions,omitempty"`
}

// SummarizeForDoctor builds an EvalSummary from a MultiReport, optionally
// comparing against a baseline to count regressions.
func SummarizeForDoctor(current *MultiReport, baseline *MultiReport) EvalSummary {
	sum := EvalSummary{GeneratedAt: current.GeneratedAt.Format("2006-01-02")}
	var totalRecall float64
	for _, rep := range current.Reports {
		sum.Repos++
		totalRecall += rep.Recall
		for _, cr := range rep.Results {
			sum.TotalCases++
			sum.SilentMiss += cr.SilentMisses
			sum.HonestMiss += cr.HonestMisses
			if cr.HardFail {
				sum.HardFails++
			}
		}
	}
	if sum.Repos > 0 {
		sum.AvgRecall = totalRecall / float64(sum.Repos)
	}
	if baseline != nil {
		gate := CheckGate(current, baseline)
		sum.Regressions = len(gate.Regressions)
	}
	return sum
}
