package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MermaidFunction renders a per-function ("in-depth") Mermaid flowchart of
// the given subgraph. Nodes are labeled "label (type)"; edges carry their
// edge type; partial/unknown-confidence edges render dashed. Output is
// deterministic (nodes sorted by ID, edges by from/to/type).
func MermaidFunction(nodes []*graph.Node, edges []*graph.Edge) string {
	sorted := make([]*graph.Node, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	// Mermaid IDs must be simple tokens; map graph IDs to n0, n1, …
	idMap := make(map[string]string, len(sorted))
	var b strings.Builder
	b.WriteString("flowchart LR\n")

	// Group nodes into per-service subgraphs so service boundaries stay
	// visible in the exported diagram.
	byService := make(map[string][]*graph.Node)
	var services []string
	for _, n := range sorted {
		if _, ok := byService[n.Service]; !ok {
			services = append(services, n.Service)
		}
		byService[n.Service] = append(byService[n.Service], n)
	}
	sort.Strings(services)

	i := 0
	for _, svc := range services {
		fmt.Fprintf(&b, "  subgraph %s\n", mermaidToken(svc))
		for _, n := range byService[svc] {
			mid := "n" + strconv.Itoa(i)
			i++
			idMap[n.ID] = mid
			label := n.Label
			if label == "" {
				label = n.ID
			}
			if v := n.Meta["resolved_version"]; v != "" {
				label += " " + n.Meta["package"] + "@" + v
			}
			fmt.Fprintf(&b, "    %s[\"%s (%s)\"]\n", mid, mermaidEscape(label), n.Type)
		}
		b.WriteString("  end\n")
	}

	sortedEdges := make([]*graph.Edge, len(edges))
	copy(sortedEdges, edges)
	sort.Slice(sortedEdges, func(i, j int) bool {
		if sortedEdges[i].From != sortedEdges[j].From {
			return sortedEdges[i].From < sortedEdges[j].From
		}
		if sortedEdges[i].To != sortedEdges[j].To {
			return sortedEdges[i].To < sortedEdges[j].To
		}
		return sortedEdges[i].Type < sortedEdges[j].Type
	})
	for _, e := range sortedEdges {
		from, okF := idMap[e.From]
		to, okT := idMap[e.To]
		if !okF || !okT {
			continue
		}
		arrow := "-->"
		if e.Confidence == graph.ConfidencePartial || e.Confidence == graph.ConfidenceUnknown {
			arrow = "-.->"
		}
		fmt.Fprintf(&b, "  %s %s|%s| %s\n", from, arrow, mermaidEscape(string(e.Type)), to)
	}
	return b.String()
}

// MermaidService renders a service-level ("high-level") Mermaid flowchart:
// one node per service, cross-service edges aggregated per edge type with
// counts. Same-service edges are omitted.
func MermaidService(nodes []*graph.Node, edges []*graph.Edge) string {
	svcByNode := make(map[string]string, len(nodes))
	svcSet := make(map[string]bool)
	for _, n := range nodes {
		svcByNode[n.ID] = n.Service
		svcSet[n.Service] = true
	}
	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)

	// Aggregate: (fromSvc, toSvc, edgeType) → count.
	type key struct{ from, to, typ string }
	counts := make(map[key]int)
	for _, e := range edges {
		fromSvc, okF := svcByNode[e.From]
		toSvc, okT := svcByNode[e.To]
		if !okF || !okT || fromSvc == toSvc {
			continue
		}
		counts[key{fromSvc, toSvc, string(e.Type)}]++
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		if keys[i].to != keys[j].to {
			return keys[i].to < keys[j].to
		}
		return keys[i].typ < keys[j].typ
	})

	var b strings.Builder
	b.WriteString("flowchart LR\n")
	for _, s := range services {
		fmt.Fprintf(&b, "  %s[\"%s\"]\n", mermaidToken(s), mermaidEscape(s))
	}
	for _, k := range keys {
		label := k.typ
		if n := counts[k]; n > 1 {
			label = fmt.Sprintf("%s x%d", k.typ, n)
		}
		fmt.Fprintf(&b, "  %s -->|%s| %s\n", mermaidToken(k.from), mermaidEscape(label), mermaidToken(k.to))
	}
	return b.String()
}

// mermaidToken reduces a name to a safe Mermaid identifier token.
func mermaidToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// mermaidEscape sanitizes label text for a quoted Mermaid string.
func mermaidEscape(s string) string {
	s = strings.ReplaceAll(s, `"`, "#quot;")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "/")
	return s
}

// handleExportMermaid handles
// GET /api/export/mermaid?level=function|service&root=<id>&direction=&depth=
// Without root it exports the whole graph; with root it exports the same
// subgraph the trace endpoint would return.
func (s *Server) handleExportMermaid(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	if level == "" {
		level = "function"
	}
	if level != "function" && level != "service" {
		writeError(w, http.StatusBadRequest, "level must be 'function' or 'service'")
		return
	}

	s.idxMu.RLock()
	idx := s.idx
	s.idxMu.RUnlock()

	var nodes []*graph.Node
	var edges []*graph.Edge
	if root := r.URL.Query().Get("root"); root != "" {
		if _, ok := idx.Nodes[root]; !ok {
			writeError(w, http.StatusNotFound, "node not found")
			return
		}
		nodes, edges = traceSubgraph(idx, root, r.URL.Query().Get("direction"), queryDepth(r, 10, 50))
	} else {
		for _, n := range idx.Nodes {
			nodes = append(nodes, n)
		}
		for _, out := range idx.OutEdges {
			edges = append(edges, out...)
		}
	}

	var text string
	if level == "service" {
		text = MermaidService(nodes, edges)
	} else {
		text = MermaidFunction(nodes, edges)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

func queryDepth(r *http.Request, def, max int) int {
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	if depth <= 0 {
		depth = def
	}
	if depth > max {
		depth = max
	}
	return depth
}

// traceSubgraph collects the nodes and induced edges reachable from root in
// the given direction — the same subgraph handleTrace serves.
func traceSubgraph(idx *graph.AdjacencyIndex, root, direction string, depth int) ([]*graph.Node, []*graph.Edge) {
	nodeSet := map[string]bool{root: true}
	switch direction {
	case "forward":
		for _, r := range graph.Descendants(idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	case "backward":
		for _, r := range graph.Ancestors(idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	default: // "both"
		for _, r := range graph.Descendants(idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
		for _, r := range graph.Ancestors(idx, root, depth) {
			nodeSet[r.Node.ID] = true
		}
	}

	nodes := make([]*graph.Node, 0, len(nodeSet))
	for id := range nodeSet {
		if n, ok := idx.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	var edges []*graph.Edge
	for fromID := range nodeSet {
		for _, e := range idx.OutEdges[fromID] {
			if nodeSet[e.To] {
				edges = append(edges, e)
			}
		}
	}
	return nodes, edges
}
