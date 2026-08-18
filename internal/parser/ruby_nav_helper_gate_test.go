package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
)

func navMatch(pattern, helper string) patterns.MatchResult {
	return patterns.MatchResult{
		PatternName: pattern,
		Captures:    map[string]string{"helper": helper},
	}
}

// TestNavHelperGate_KeepsRouteHelpers. Every helper Rails generates ends in
// `_path` or `_url`, so the suffix is a framework invariant rather than a
// naming convention that a view may opt out of.
func TestNavHelperGate_KeepsRouteHelpers(t *testing.T) {
	t.Parallel()
	in := []patterns.MatchResult{
		navMatch("nav_link_rails_helper", "study_deliverable_path"),
		navMatch("nav_link_rails_helper", "edit_user_url"),
		navMatch("nav_link_rails_form_helper", "unblind_study_path"),
	}
	assert.Len(t, dropNonRouteHelperNavMatches(in), 3)
}

// TestNavHelperGate_DropsNonRouteCalls is the artifact this exists for: the
// nav queries' `_` wildcard lets @helper bind to any receiverless call in the
// argument list, so `link_to t(".edit"), edit_agent_path(a)` produced a second,
// phantom http_client naming the i18n helper.
func TestNavHelperGate_DropsNonRouteCalls(t *testing.T) {
	t.Parallel()
	for _, helper := range []string{
		"t", "image_tag", "back_link", "duplicate_params",
		"parent_folder_link", "acl_status_params", "sce_batch_job",
	} {
		in := []patterns.MatchResult{navMatch("nav_link_rails_helper", helper)}
		assert.Empty(t, dropNonRouteHelperNavMatches(in), "helper %q should be dropped", helper)
	}
}

// TestNavHelperGate_DropsBareUrlAndPath. `url` and `path` are ordinary locals
// in the fleet's helpers; a stem is required, not optional.
func TestNavHelperGate_DropsBareUrlAndPath(t *testing.T) {
	t.Parallel()
	in := []patterns.MatchResult{
		navMatch("nav_link_rails_helper", "url"),
		navMatch("nav_link_rails_helper", "path"),
		navMatch("nav_link_rails_helper", "_path"),
		navMatch("nav_link_rails_helper", ""),
	}
	assert.Empty(t, dropNonRouteHelperNavMatches(in))
}

// TestNavHelperGate_KeepsUnresolvableRouteShapedNames. A `_path` name that
// matches no route is an honest gap the ledger should record. Dropping it would
// understate the miss, and the suffix cannot distinguish a real helper this
// pass failed to resolve from a local that merely looks like one.
func TestNavHelperGate_KeepsUnresolvableRouteShapedNames(t *testing.T) {
	t.Parallel()
	in := []patterns.MatchResult{
		navMatch("nav_link_rails_helper", "redirect_path"),
		navMatch("nav_link_rails_helper", "delete_path"),
	}
	assert.Len(t, dropNonRouteHelperNavMatches(in), 2)
}

// TestNavHelperGate_LeavesOtherPatternsAlone. Literal-path nav matches carry no
// helper capture at all and must survive.
func TestNavHelperGate_LeavesOtherPatternsAlone(t *testing.T) {
	t.Parallel()
	in := []patterns.MatchResult{
		{PatternName: "nav_link_rails_literal", Captures: map[string]string{"path": "/app/folders"}},
		{PatternName: "rest_client_request", Captures: map[string]string{"url": "/x"}},
	}
	assert.Len(t, dropNonRouteHelperNavMatches(in), 2)
}
