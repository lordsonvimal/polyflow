package linker

import (
	"fmt"
	"path"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// containedTypes are the declaration node types that hang off the structural
// backbone: service→file→{function,method,struct,component}. Behavioral edges
// alone can't answer "what's in this file" or "what methods hang off this
// struct", so this containment layer is the biggest single win for the
// agent-context recall goal — and it lifts the otherwise-isolated structs and
// orphan functions out of the isolated set.
var containedTypes = map[graph.NodeType]bool{
	graph.NodeTypeFunction:  true,
	graph.NodeTypeMethod:    true,
	graph.NodeTypeStruct:    true,
	graph.NodeTypeComponent: true,
}

// LinkContainment synthesizes the service→file→declaration `contains` backbone
// (plus struct→method) from the file/receiver metadata already on the collected
// nodes. It returns the synthetic service and file nodes it minted alongside the
// edges, so the indexer can persist them before wiring the edges. Runs after all
// parser/semantic nodes are collected (containment spans every declaration a
// file produces, regardless of which pass emitted it).
func LinkContainment(nodes []graph.Node) ([]graph.Node, []graph.Edge) {
	var newNodes []graph.Node
	var edges []graph.Edge

	serviceID := map[string]string{}           // service -> service node ID
	fileID := map[string]string{}              // service\x00file -> file node ID
	structByKey := map[string]string{}         // service\x00dir\x00label -> struct node ID
	svcFileSeen := map[string]bool{}           // service\x00file dedup for service→file edges

	// First pass: index structs by package (directory) so struct→method resolves
	// even when the method lives in a different file of the same package.
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeStruct && n.File != "" {
			structByKey[structKey(n.Service, n.File, n.Label)] = n.ID
		}
	}

	ensureService := func(service string) string {
		if id, ok := serviceID[service]; ok {
			return id
		}
		id := "service:" + service
		serviceID[service] = id
		newNodes = append(newNodes, graph.Node{
			ID:    id,
			Type:  graph.NodeTypeService,
			Label: service,
			Meta:  map[string]string{"kind": "service"},
		})
		return id
	}

	ensureFile := func(service, file string) string {
		key := service + "\x00" + file
		if id, ok := fileID[key]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:%s", service, file, graph.NodeTypeFile)
		fileID[key] = id
		newNodes = append(newNodes, graph.Node{
			ID:       id,
			Type:     graph.NodeTypeFile,
			Label:    file,
			Service:  service,
			File:     file,
			Language: languageForFile(file),
			Meta:     map[string]string{"basename": path.Base(file)},
		})
		// Wire service→file once per file.
		svcID := ensureService(service)
		if !svcFileSeen[key] {
			svcFileSeen[key] = true
			edges = append(edges, containsEdge(svcID, id))
		}
		return id
	}

	for i := range nodes {
		n := &nodes[i]
		if !containedTypes[n.Type] || n.File == "" {
			continue
		}
		fID := ensureFile(n.Service, n.File)
		edges = append(edges, containsEdge(fID, n.ID))

		// struct→method: attach the method to its receiver's struct when the
		// receiver type resolves within the same package.
		if n.Type == graph.NodeTypeMethod {
			recv := strings.TrimPrefix(n.Meta["receiver"], "*")
			if recv != "" {
				if sID, ok := structByKey[structKey(n.Service, n.File, recv)]; ok {
					edges = append(edges, containsEdge(sID, n.ID))
				}
			}
		}
	}

	return newNodes, edges
}

// structKey keys a struct by service + package directory + label, so a method's
// receiver resolves to the struct in its own package (Go requires the receiver
// type be declared in the method's package) without colliding with a same-named
// struct elsewhere.
func structKey(service, file, label string) string {
	return service + "\x00" + path.Dir(file) + "\x00" + label
}

// containsEdge builds a deterministic `contains` edge; the ID encodes both
// endpoints so re-runs upsert rather than duplicate.
func containsEdge(from, to string) graph.Edge {
	return graph.Edge{
		ID:         fmt.Sprintf("contains:%s->%s", from, to),
		From:       from,
		To:         to,
		Type:       graph.EdgeTypeContains,
		Label:      string(graph.EdgeTypeContains),
		Confidence: graph.ConfidenceStatic,
	}
}

// languageForFile maps a file extension to the graph's language tag, so file
// nodes carry the same language label as the declarations they contain.
func languageForFile(file string) string {
	switch {
	case strings.HasSuffix(file, ".templ"):
		return "templ"
	case strings.HasSuffix(file, ".go"):
		return "go"
	case strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".tsx"):
		return "typescript"
	case strings.HasSuffix(file, ".js") || strings.HasSuffix(file, ".jsx") || strings.HasSuffix(file, ".mjs"):
		return "javascript"
	case strings.HasSuffix(file, ".rb"):
		return "ruby"
	}
	return ""
}
