package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AgentMultiReport holds scored AgentReports for every corpus with
// agent_cases found under a root, mirroring MultiReport's shape (T.1) so the
// same baseline/gate conventions apply to agent-correctness numbers.
type AgentMultiReport struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Reports     []AgentReport   `json:"repos"`
	Skipped     []SkippedCorpus `json:"skipped,omitempty"`
}

// AgentRegression records a single regression detected by CheckAgentGate.
type AgentRegression struct {
	Repo                string  `json:"repo"`
	CaseID              string  `json:"case_id"`
	Reason              string  `json:"reason"` // "now_incorrect" | "correctness_drop" | "missing_repo"
	BaselineCorrectness float64 `json:"baseline_correctness,omitempty"`
	CurrentCorrectness  float64 `json:"current_correctness,omitempty"`
}

// AgentGateResult holds the outcome of comparing an agent-correctness run
// against the baseline.
type AgentGateResult struct {
	Regressions []AgentRegression `json:"regressions"`
	OK          bool              `json:"ok"`
}

// CheckAgentGate compares current against baseline and returns all
// regressions (plan-14 T.3).
//
// Pinned failure conditions (mirroring CheckGate):
//  1. any case that was `correct` in the baseline and is incorrect now.
//     A case absent from the baseline (new case) does not trip this —
//     new cases enter the baseline failing, then ratchet, same as
//     CheckGate's pre-existing-HardFail precedent.
//  2. per-repo Correctness drops below the baseline's Correctness.
//  3. a repo present in the baseline but absent from the current run —
//     LocalOnly-skipped repos are exempt, same as CheckGate.
func CheckAgentGate(current, baseline *AgentMultiReport) *AgentGateResult {
	type baselineKey struct{ repo, caseID string }
	baselineCases := make(map[baselineKey]AgentCaseResult)
	baselineRepoCorrectness := make(map[string]float64)
	for _, rep := range baseline.Reports {
		baselineRepoCorrectness[rep.Repo] = rep.Correctness
		for _, cr := range rep.Results {
			baselineCases[baselineKey{rep.Repo, cr.ID}] = cr
		}
	}

	var regressions []AgentRegression
	for _, rep := range current.Reports {
		for _, cr := range rep.Results {
			key := baselineKey{rep.Repo, cr.ID}
			if base, found := baselineCases[key]; found && base.Correct && !cr.Correct {
				regressions = append(regressions, AgentRegression{
					Repo:   rep.Repo,
					CaseID: cr.ID,
					Reason: "now_incorrect",
				})
			}
		}

		if baseCorr, ok := baselineRepoCorrectness[rep.Repo]; ok {
			if rep.Correctness < baseCorr-1e-9 {
				regressions = append(regressions, AgentRegression{
					Repo:                rep.Repo,
					CaseID:              "*",
					Reason:              "correctness_drop",
					BaselineCorrectness: baseCorr,
					CurrentCorrectness:  rep.Correctness,
				})
			}
		}
	}

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
			regressions = append(regressions, AgentRegression{
				Repo:   rep.Repo,
				CaseID: "*",
				Reason: "missing_repo",
			})
		}
	}

	return &AgentGateResult{
		Regressions: regressions,
		OK:          len(regressions) == 0,
	}
}

// LoadAgentBaseline reads and parses an AgentMultiReport baseline JSON file.
func LoadAgentBaseline(path string) (*AgentMultiReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent baseline %s: %w", path, err)
	}
	var r AgentMultiReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse agent baseline %s: %w", path, err)
	}
	return &r, nil
}

// AgentTrustSummary is a compact per-repo agent-correctness reading, used by
// `polyflow doctor`'s Trust panel to render the row without re-running the
// corpus (T.3).
type AgentTrustSummary struct {
	Measured    bool
	Correctness float64
	Cases       int
	MeasuredAt  string // date portion of GeneratedAt, "" when unmeasured
}

// SummarizeAgentForDoctor finds the AgentReport for repo within baseline and
// returns its compact summary. Measured=false (zero value) when baseline is
// nil or has no report for repo — absence of measurement is reported, never
// implied away, matching TrustStamp's convention.
func SummarizeAgentForDoctor(baseline *AgentMultiReport, repo string) AgentTrustSummary {
	if baseline == nil {
		return AgentTrustSummary{}
	}
	for _, rep := range baseline.Reports {
		if rep.Repo == repo {
			return AgentTrustSummary{
				Measured:    true,
				Correctness: rep.Correctness,
				Cases:       len(rep.Results),
				MeasuredAt:  baseline.GeneratedAt.Format("2006-01-02"),
			}
		}
	}
	return AgentTrustSummary{}
}
