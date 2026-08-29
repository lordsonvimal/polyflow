package pluginloader

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RenderCoverage renders the Phase 2 doctor plugin-version-coverage report
// from the "plugin_coverage" graph meta value the indexer writes (Reason
// strings from CoverageNote, one per (component, service) pair skipped for
// an out-of-range resolved framework version) — mirrors
// toolchain.RenderVersionCoverage's role for tool/version fallbacks.
func RenderCoverage(notesJSON string) string {
	const prefix = "  Plugin coverage:      "
	const indent = "                       "

	if strings.TrimSpace(notesJSON) == "" {
		return prefix + "unstamped (graph predates Phase 2 — run 'polyflow index --full')\n"
	}
	var notes []CoverageNote
	if err := json.Unmarshal([]byte(notesJSON), &notes); err != nil {
		return prefix + fmt.Sprintf("error parsing plugin_coverage: %v\n", err)
	}
	if len(notes) == 0 {
		return prefix + "no out-of-range plugin components\n"
	}

	sort.Slice(notes, func(i, j int) bool {
		a, b := notes[i], notes[j]
		if a.Plugin != b.Plugin {
			return a.Plugin < b.Plugin
		}
		if a.Component != b.Component {
			return a.Component < b.Component
		}
		return a.Reason < b.Reason
	})

	var b strings.Builder
	fmt.Fprintf(&b, "%s%d out-of-range component/service pair(s):\n", prefix, len(notes))
	for _, n := range notes {
		fmt.Fprintf(&b, "%s%s/%s: %s\n", indent, n.Plugin, n.Component, n.Reason)
	}
	return b.String()
}
