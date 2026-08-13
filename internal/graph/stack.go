package graph

import (
	"sort"
	"strings"
)

// DependencyInfo is one resolved package version surfaced by /api/stack.
type DependencyInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ecosystem string `json:"ecosystem"`
}

// ServiceStack is one service's tech-stack summary for /api/stack.
type ServiceStack struct {
	Name       string           `json:"name"`
	Language   string           `json:"language"`
	Frameworks []string         `json:"frameworks"`
	Deps       []DependencyInfo `json:"deps"`
	NodeCounts map[string]int   `json:"node_counts"`
	EdgeCounts map[string]int   `json:"edge_counts"`
	Files      int              `json:"files"`
}

// BuildStack aggregates one ServiceStack per service found in idx, joined
// with deps (pass every Dependency in the workspace; BuildStack groups by
// service itself). Language is the most common non-empty Node.Language among
// the service's nodes (alphabetical tie-break); frameworks are the distinct
// non-empty meta["framework"] values observed (real signal only — no
// service-wide framework classifier exists, so nothing is guessed here).
func BuildStack(idx *AdjacencyIndex, deps []*Dependency) []ServiceStack {
	type agg struct {
		languages  map[string]int
		frameworks map[string]bool
		nodeCounts map[string]int
		files      map[string]bool
	}
	byService := map[string]*agg{}
	ensure := func(svc string) *agg {
		a, ok := byService[svc]
		if !ok {
			a = &agg{
				languages:  map[string]int{},
				frameworks: map[string]bool{},
				nodeCounts: map[string]int{},
				files:      map[string]bool{},
			}
			byService[svc] = a
		}
		return a
	}

	for _, n := range idx.Nodes {
		a := ensure(n.Service)
		a.nodeCounts[string(n.Type)]++
		if n.Language != "" {
			a.languages[n.Language]++
		}
		if fw := n.Meta["framework"]; fw != "" {
			a.frameworks[fw] = true
		}
		if n.File != "" {
			a.files[n.File] = true
		}
	}

	edgeCounts := map[string]map[string]int{}
	for _, e := range idx.AllEdges() {
		from := idx.Nodes[e.From]
		if from == nil {
			continue
		}
		if edgeCounts[from.Service] == nil {
			edgeCounts[from.Service] = map[string]int{}
		}
		edgeCounts[from.Service][string(e.Type)]++
	}

	depsByService := map[string][]DependencyInfo{}
	for _, d := range deps {
		depsByService[d.Service] = append(depsByService[d.Service], DependencyInfo{
			Name: d.Name, Version: d.Version, Ecosystem: d.Ecosystem,
		})
	}
	for svc := range depsByService {
		list := depsByService[svc]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Ecosystem != list[j].Ecosystem {
				return list[i].Ecosystem < list[j].Ecosystem
			}
			return list[i].Name < list[j].Name
		})
		depsByService[svc] = list
	}

	var services []string
	for svc := range byService {
		services = append(services, svc)
	}
	for svc := range depsByService {
		if byService[svc] == nil {
			ensure(svc)
			services = append(services, svc)
		}
	}
	sort.Strings(services)

	out := make([]ServiceStack, 0, len(services))
	for _, svc := range services {
		a := byService[svc]
		out = append(out, ServiceStack{
			Name:       svc,
			Language:   majorityLanguage(a.languages),
			Frameworks: sortedSetKeys(a.frameworks),
			Deps:       nonNilDeps(depsByService[svc]),
			NodeCounts: a.nodeCounts,
			EdgeCounts: edgeCounts[svc],
			Files:      len(a.files),
		})
	}
	return out
}

func majorityLanguage(counts map[string]int) string {
	best, bestN := "", 0
	var langs []string
	for l := range counts {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		if counts[l] > bestN {
			best, bestN = l, counts[l]
		}
	}
	return best
}

func sortedSetKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNilDeps(d []DependencyInfo) []DependencyInfo {
	if d == nil {
		return []DependencyInfo{}
	}
	return d
}

// FilterUnresolvedRefs narrows refs by service/kind (exact match, "" = no
// filter) and q (case-insensitive substring over name and file), preserving
// the input order (callers pass an already-deterministically-sorted slice).
func FilterUnresolvedRefs(refs []UnresolvedRef, service, kind, q string) []UnresolvedRef {
	out := make([]UnresolvedRef, 0, len(refs))
	for _, r := range refs {
		if service != "" && r.Service != service {
			continue
		}
		if kind != "" && r.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name), strings.ToLower(q)) &&
			!strings.Contains(strings.ToLower(r.File), strings.ToLower(q)) {
			continue
		}
		out = append(out, r)
	}
	return out
}
