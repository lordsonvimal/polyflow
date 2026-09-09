package linker

import "github.com/lordsonvimal/polyflow/internal/graph"

// LinkRailsFiltersVia is linkRailsFilters with the Tier DL switch exposed to the
// external test package.
//
// The alternative is to have the test set PF_DATALOG, which would force every
// case in rails_filters_test.go to give up t.Parallel — an env var is process
// state and the suite runs concurrently. The matrix is the whole point of DL.1,
// so it must not be the thing that makes the tests slower to run.
func LinkRailsFiltersVia(nodes []graph.Node, serviceFiles map[string][]string, useDatalog bool) ([]graph.Edge, []graph.UnresolvedRef) {
	return linkRailsFilters(nodes, serviceFiles, useDatalog)
}
