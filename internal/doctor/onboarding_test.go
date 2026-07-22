package doctor_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/doctor"
)

// golden helpers — each entry is a condition + expected output lines.

func TestCheckOnboarding_MissingWorkspace(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{WorkspaceFound: false})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	if issues[0].Kind != doctor.IssueAction {
		t.Errorf("want action, got %s", issues[0].Kind)
	}
	if !strings.Contains(issues[0].Message, "workspace.yaml") {
		t.Errorf("message should mention workspace.yaml: %q", issues[0].Message)
	}
	if !strings.Contains(issues[0].Fix, "polyflow init") {
		t.Errorf("fix should mention polyflow init: %q", issues[0].Fix)
	}
}

func TestCheckOnboarding_NoServices(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: nil,
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	if issues[0].Kind != doctor.IssueAction {
		t.Errorf("want action, got %s", issues[0].Kind)
	}
	if !strings.Contains(issues[0].Fix, "polyflow init") {
		t.Errorf("fix should mention polyflow init: %q", issues[0].Fix)
	}
}

func TestCheckOnboarding_NotIndexed(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"api", "web"},
		StoreErr:           errors.New("db not found"),
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	if issues[0].Kind != doctor.IssueAction {
		t.Errorf("want action, got %s", issues[0].Kind)
	}
	if !strings.Contains(issues[0].Fix, "polyflow index") {
		t.Errorf("fix should mention polyflow index: %q", issues[0].Fix)
	}
}

func TestCheckOnboarding_ZeroPatternMatchService(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"api", "unknown-lang-svc"},
		NodeCountByService: map[string]int{"api": 42, "unknown-lang-svc": 0},
		HasSessions:        true,
	})
	// Should have exactly the zero-match warning (no sessions issue since HasSessions=true).
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d: %v", len(issues), issues)
	}
	if issues[0].Kind != doctor.IssueWarning {
		t.Errorf("want warning, got %s", issues[0].Kind)
	}
	if !strings.Contains(issues[0].Message, "unknown-lang-svc") {
		t.Errorf("message should name the problematic service: %q", issues[0].Message)
	}
	if !strings.Contains(issues[0].Fix, "polyflow patterns") {
		t.Errorf("fix should mention polyflow patterns: %q", issues[0].Fix)
	}
}

func TestCheckOnboarding_NoSessions(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"api"},
		NodeCountByService: map[string]int{"api": 100},
		HasSessions:        false,
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
	if issues[0].Kind != doctor.IssueInfo {
		t.Errorf("want info, got %s", issues[0].Kind)
	}
	if !strings.Contains(issues[0].Fix, "polyflow capture") {
		t.Errorf("fix should mention polyflow capture: %q", issues[0].Fix)
	}
}

func TestCheckOnboarding_FullyOperational(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"api", "web"},
		NodeCountByService: map[string]int{"api": 150, "web": 80},
		HasSessions:        true,
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 issues for fully operational workspace, got %d: %v", len(issues), issues)
	}
}

func TestCheckOnboarding_MultipleZeroServices(t *testing.T) {
	issues := doctor.CheckOnboarding(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"go-svc", "php-svc", "erlang-svc"},
		NodeCountByService: map[string]int{"go-svc": 50, "php-svc": 0, "erlang-svc": 0},
		HasSessions:        true,
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 (combined) issue, got %d", len(issues))
	}
	msg := issues[0].Message
	if !strings.Contains(msg, "php-svc") || !strings.Contains(msg, "erlang-svc") {
		t.Errorf("message should name all zero-match services: %q", msg)
	}
	if strings.Contains(msg, "go-svc") {
		t.Errorf("message should NOT name the service with patterns: %q", msg)
	}
}

func TestCheckOnboarding_MissingWorkspaceReturnsSingleIssue(t *testing.T) {
	// When workspace is missing, remaining checks (no services, not indexed, etc.)
	// are not surfaced — the single action is sufficient.
	p := doctor.OnboardingParams{
		WorkspaceFound:     false,
		ConfiguredServices: []string{"api"},
		StoreErr:           errors.New("db not found"),
	}
	issues := doctor.CheckOnboarding(p)
	if len(issues) != 1 {
		t.Errorf("want exactly 1 issue when workspace missing, got %d", len(issues))
	}
}

func TestOnboardingOK(t *testing.T) {
	ok := doctor.OnboardingOK(doctor.OnboardingParams{
		WorkspaceFound:     true,
		ConfiguredServices: []string{"svc"},
		NodeCountByService: map[string]int{"svc": 10},
		HasSessions:        true,
	})
	if !ok {
		t.Error("expected OnboardingOK=true for operational workspace")
	}

	notOK := doctor.OnboardingOK(doctor.OnboardingParams{WorkspaceFound: false})
	if notOK {
		t.Error("expected OnboardingOK=false when workspace missing")
	}
}
