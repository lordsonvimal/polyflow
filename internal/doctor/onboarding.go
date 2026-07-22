// Package doctor provides reusable diagnostic helpers for polyflow workspaces.
// The onboarding check is designed to be pure and testable: callers resolve
// the workspace state before calling CheckOnboarding so no file I/O happens
// inside the function.
package doctor

import "strings"

// IssueKind classifies the severity of an onboarding issue.
type IssueKind string

const (
	IssueAction  IssueKind = "action"  // must resolve before polyflow is useful
	IssueWarning IssueKind = "warning" // degraded functionality
	IssueInfo    IssueKind = "info"    // optional enhancement
)

// OnboardingIssue is a single detected problem plus the command that fixes it.
type OnboardingIssue struct {
	Kind    IssueKind
	Message string
	Fix     string // one-liner command suggestion
}

// OnboardingParams is the resolved workspace state for onboarding checks.
// All fields are pre-resolved by the caller; CheckOnboarding does no I/O.
type OnboardingParams struct {
	// WorkspaceFound is true when workspace.yaml (or the equivalent config file)
	// exists in the working directory.
	WorkspaceFound bool

	// ConfiguredServices holds the service names declared in workspace.yaml.
	// Empty slice means workspace exists but has no services.
	ConfiguredServices []string

	// StoreErr is non-nil when the graph DB could not be opened (not indexed).
	StoreErr error

	// NodeCountByService maps each service name to its count of substantive
	// (non-file) nodes in the index.  A count of zero means patterns produced
	// no recognized symbols for that service.
	NodeCountByService map[string]int

	// HasSessions is true when at least one runtime capture session exists.
	HasSessions bool
}

// CheckOnboarding returns a list of onboarding issues ordered by severity.
// An empty slice means the workspace is fully configured and operational.
// Callers should display action-kind issues first as blockers.
func CheckOnboarding(p OnboardingParams) []OnboardingIssue {
	var issues []OnboardingIssue

	if !p.WorkspaceFound {
		return append(issues, OnboardingIssue{
			Kind:    IssueAction,
			Message: "Workspace not configured — workspace.yaml not found",
			Fix:     "polyflow init",
		})
	}

	if len(p.ConfiguredServices) == 0 {
		issues = append(issues, OnboardingIssue{
			Kind:    IssueAction,
			Message: "No services configured in workspace.yaml",
			Fix:     "polyflow init --interactive",
		})
		// No further checks possible without services.
		return issues
	}

	if p.StoreErr != nil {
		issues = append(issues, OnboardingIssue{
			Kind:    IssueAction,
			Message: "Services not indexed — no graph DB found",
			Fix:     "polyflow index",
		})
		// Remaining checks require the index.
		return issues
	}

	// Zero-pattern-match services: indexed but no recognizable symbols.
	// This is the loudest "any repo" warning — it means polyflow cannot
	// answer any query for these services.
	var zeroMatch []string
	for _, svc := range p.ConfiguredServices {
		if p.NodeCountByService[svc] == 0 {
			zeroMatch = append(zeroMatch, svc)
		}
	}
	if len(zeroMatch) > 0 {
		issues = append(issues, OnboardingIssue{
			Kind:    IssueWarning,
			Message: "Services with no recognized patterns: " + strings.Join(zeroMatch, ", "),
			Fix:     "polyflow patterns --list   # check supported languages/frameworks",
		})
	}

	if !p.HasSessions {
		issues = append(issues, OnboardingIssue{
			Kind:    IssueInfo,
			Message: "No runtime captures (optional — enables evidence fusion)",
			Fix:     "polyflow capture start <name>",
		})
	}

	return issues
}

// OnboardingOK returns true when CheckOnboarding would return no issues.
func OnboardingOK(p OnboardingParams) bool {
	return len(CheckOnboarding(p)) == 0
}
