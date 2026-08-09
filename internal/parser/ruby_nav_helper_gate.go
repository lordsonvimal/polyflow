package parser

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// navHelperPatterns are the Rails nav patterns whose URL argument is a method
// call rather than a literal, and which therefore capture a *name* to resolve
// against the route table.
var navHelperPatterns = map[string]bool{
	"nav_link_rails_helper":      true,
	"nav_link_rails_form_helper": true,
}

// dropNonRouteHelperNavMatches discards nav matches whose captured helper is
// not a Rails route helper.
//
// Every route helper Rails generates ends in `_path` or `_url` — that is how
// the method is named, not a convention a developer may opt out of — so the
// test is an invariant of the framework rather than a heuristic about naming.
//
// The matches this drops are an artifact of the `_` wildcard in the nav
// queries, which lets @helper bind to any receiverless call in the argument
// list, not only the URL one. `link_to t("helpers.links.edit"),
// edit_organization_agent_path(...)` yields the correct match *and* a second
// one naming the i18n helper `t`. The fleet's leftovers after C.2 were 28 such
// nodes across `t`, `image_tag`, `back_link`, `duplicate_params` and bare
// locals named `url` / `path` / `v` — orphan http_client nodes describing
// requests that do not exist, in the same family as the 57 phantom JS clients
// C.1c removed.
//
// Names that do end in `_path`/`_url` but resolve to nothing (`redirect_path`,
// a local named `delete_path`) are kept and reach the ledger. An unresolved
// entry is an honest gap; a silently dropped one is a lie about coverage, and
// the suffix cannot tell the two apart.
//
// Filters MatchResults rather than finished nodes for the same reason
// dropNonRoutesFileRouteMatches does: the gate has to run before MatchToGraph's
// pass 1b, where a node at a file:line can suppress another at the same one.
func dropNonRouteHelperNavMatches(results []patterns.MatchResult) []patterns.MatchResult {
	out := results[:0]
	for _, r := range results {
		if navHelperPatterns[r.PatternName] && !isRailsRouteHelperName(r.Captures["helper"]) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// isRailsRouteHelperName reports whether a name is shaped like a Rails route
// helper: a non-empty stem followed by `_path` or `_url`. The bare words `path`
// and `url` are ordinary locals in the fleet's views and name no route, so the
// stem is required rather than optional.
func isRailsRouteHelperName(name string) bool {
	for _, suffix := range []string{"_path", "_url"} {
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			return true
		}
	}
	return false
}
