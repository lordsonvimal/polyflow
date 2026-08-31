package impact

import (
	"encoding/json"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// UnresolvedEntry is one unresolved reference within UnresolvedFileGroup's
// file. Service is set only when the response spans more than one service —
// see GroupUnresolvedByFile.
type UnresolvedEntry struct {
	Service string `json:"service,omitempty"`
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Targets string `json:"targets,omitempty"`
}

// UnresolvedFileGroup rolls up UnresolvedRef entries by file — the wire
// shape every impact result/summary type serializes "unresolved" as. Applies
// the same per-file grouping FileRollup already gives the resolved blast
// radius to the unresolved ledger, which was a flat list repeating its file
// path once per entry: found live on orion-atlas, a 128-entry unresolved
// list touching only 16 distinct files (one file, App.tsx, accounted for 47
// of the 128).
type UnresolvedFileGroup struct {
	File    string            `json:"file"`
	Entries []UnresolvedEntry `json:"entries"`
}

// GroupUnresolvedByFile groups refs by file, sorted by file then line for
// determinism (rule 2). singleService omits the now-redundant per-entry
// Service field — true whenever the caller already knows every ref shares
// one service (the ServicesAffected/Service field elsewhere in the same
// response already answers "which service", so repeating it per entry is
// pure duplication in the overwhelmingly common single-service case).
func GroupUnresolvedByFile(refs []graph.UnresolvedRef, singleService bool) []UnresolvedFileGroup {
	if len(refs) == 0 {
		return nil
	}
	byFile := make(map[string][]UnresolvedEntry, len(refs))
	seen := make(map[string]bool)
	var order []string
	for _, ref := range refs {
		if !seen[ref.File] {
			seen[ref.File] = true
			order = append(order, ref.File)
		}
		e := UnresolvedEntry{Line: ref.Line, Name: ref.Name, Kind: ref.Kind, Targets: ref.Targets}
		if !singleService {
			e.Service = ref.Service
		}
		byFile[ref.File] = append(byFile[ref.File], e)
	}
	sort.Strings(order)
	groups := make([]UnresolvedFileGroup, 0, len(order))
	for _, f := range order {
		entries := byFile[f]
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Line < entries[j].Line })
		groups = append(groups, UnresolvedFileGroup{File: f, Entries: entries})
	}
	return groups
}

// marshalWithGroupedUnresolved marshals base (an alias of the caller's type,
// so this does not recurse into its own MarshalJSON) and replaces the
// "unresolved" key with GroupUnresolvedByFile's grouped form. base's field
// list is never duplicated here — a struct alias marshal picks up every
// field automatically, so a field added to Result/Summary/FileResult/
// DiffResult/DiffSummary later needs no matching change in this file.
func marshalWithGroupedUnresolved(alias any, refs []graph.UnresolvedRef, servicesAffected []string) ([]byte, error) {
	base, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	grouped, err := json.Marshal(GroupUnresolvedByFile(refs, len(servicesAffected) <= 1))
	if err != nil {
		return nil, err
	}
	m["unresolved"] = grouped
	return json.Marshal(m)
}
