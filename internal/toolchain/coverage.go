package toolchain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ProfileStamp is the per-tool selection stamp persisted in graph meta
// "toolchain_profiles" (service → tool → stamp) by the indexer.
type ProfileStamp struct {
	Profile  string `json:"profile"`
	Version  string `json:"version"`
	Inferred bool   `json:"inferred,omitempty"`
}

// RenderVersionCoverage renders the V.4 doctor tool×version coverage table
// from the "toolchain_profiles" and "toolchain_coverage" graph meta values.
// Rows are sorted by (service, tool); output is byte-identical across runs
// for the same inputs. A graph indexed before V.2 has no profile stamps —
// that is reported as unstamped, never as an empty table.
func RenderVersionCoverage(profilesJSON, notesJSON string) string {
	const prefix = "  Versioning coverage: "
	const indent = "                       "

	if strings.TrimSpace(profilesJSON) == "" {
		return prefix + "unstamped (graph predates V.2 — run 'polyflow index --full')\n"
	}
	var profiles map[string]map[string]ProfileStamp
	if err := json.Unmarshal([]byte(profilesJSON), &profiles); err != nil {
		return prefix + fmt.Sprintf("error parsing toolchain_profiles: %v\n", err)
	}
	if len(profiles) == 0 {
		return prefix + "unstamped (graph predates V.2 — run 'polyflow index --full')\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s%-12s  %-12s  %-12s  %-18s  %s\n", prefix, "service", "tool", "version", "profile", "inferred")
	services := make([]string, 0, len(profiles))
	for svc := range profiles {
		services = append(services, svc)
	}
	sort.Strings(services)
	for _, svc := range services {
		tools := make([]string, 0, len(profiles[svc]))
		for tool := range profiles[svc] {
			tools = append(tools, tool)
		}
		sort.Strings(tools)
		for _, tool := range tools {
			stamp := profiles[svc][tool]
			inferred := "-"
			if stamp.Inferred {
				inferred = "yes"
			}
			fmt.Fprintf(&b, "%s%-12s  %-12s  %-12s  %-18s  %s\n", indent, svc, tool, stamp.Version, stamp.Profile, inferred)
		}
	}

	var notes []CoverageNote
	if strings.TrimSpace(notesJSON) != "" {
		if err := json.Unmarshal([]byte(notesJSON), &notes); err != nil {
			fmt.Fprintf(&b, "%serror parsing toolchain_coverage: %v\n", indent, err)
			return b.String()
		}
	}
	if len(notes) > 0 {
		sort.Slice(notes, func(i, j int) bool {
			a, c := notes[i], notes[j]
			if a.Service != c.Service {
				return a.Service < c.Service
			}
			if a.Tool != c.Tool {
				return a.Tool < c.Tool
			}
			if a.RequestedVersion != c.RequestedVersion {
				return a.RequestedVersion < c.RequestedVersion
			}
			return a.Note < c.Note
		})
		fmt.Fprintf(&b, "%s%d fallback selection(s) — nearest-newest or in-process:\n", indent, len(notes))
		for _, n := range notes {
			line := fmt.Sprintf("%s  %s/%s requested=%s used=%s", indent, n.Service, n.Tool, n.RequestedVersion, n.UsedProfile)
			if n.Note != "" {
				line += "  (" + n.Note + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
