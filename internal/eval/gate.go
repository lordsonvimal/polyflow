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
	Reason         string  `json:"reason"` // "hard_fail" | "recall_drop" | "silent_miss_rise"
	BaselineRecall float64 `json:"baseline_recall,omitempty"`
	CurrentRecall  float64 `json:"current_recall,omitempty"`
	BaselineSilent int     `json:"baseline_silent,omitempty"`
	CurrentSilent  int     `json:"current_silent,omitempty"`
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
func CheckGate(current, baseline *MultiReport) *GateResult {
	// Index baseline by repo → caseID.
	type baselineKey struct{ repo, caseID string }
	baselineCases := make(map[baselineKey]CaseResult)
	baselineRepoRecall := make(map[string]float64)
	for _, rep := range baseline.Reports {
		baselineRepoRecall[rep.Repo] = rep.Recall
		for _, cr := range rep.Results {
			baselineCases[baselineKey{rep.Repo, cr.CaseID}] = cr
		}
	}

	var regressions []Regression
	for _, rep := range current.Reports {
		// Condition 1: new HardFail per case.
		for _, cr := range rep.Results {
			if !cr.HardFail {
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
