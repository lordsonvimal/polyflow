package graph

import (
	"fmt"
	"sort"
	"strings"
)

// entrypointKindByType maps declaration node types the flow engine treats as
// flow roots onto the UB.5 entrypoint kind vocabulary (NodeTypeRouteGroup is
// deliberately excluded — it is scaffolding, not a callable endpoint).
var entrypointKindByType = map[NodeType]string{
	NodeTypeHTTPHandler:     "http_handler",
	NodeTypeRoute:           "route",
	NodeTypeWorker:          "worker",
	NodeTypeSubscriber:      "subscriber",
	NodeTypeGRPCHandler:     "grpc_handler",
	NodeTypeGraphQLResolver: "graphql_resolver",
}

// ClassifyEntrypoint reports the UB.5 entrypoint kind for n, and whether it
// qualifies. A NodeTypeFunction additionally qualifies when meta.root_kind ==
// "entrypoint"; other root_kind values (e.g. "callback", "unreachable") are
// reported via the caller's skipped accounting rather than silently dropped
// (bug-class rule 12).
func ClassifyEntrypoint(n *Node) (kind string, skippedRootKind string, ok bool) {
	if k, mapped := entrypointKindByType[n.Type]; mapped {
		return k, "", true
	}
	if n.Type == NodeTypeFunction {
		if rk := n.Meta["root_kind"]; rk != "" {
			if rk == "entrypoint" {
				return "function", "", true
			}
			return "", rk, false
		}
	}
	return "", "", false
}

// EntrypointItem is one entry in the /api/flows/entrypoints catalog.
type EntrypointItem struct {
	NodeID  string `json:"node_id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// SkippedCount reports a population of nodes the entrypoint catalog
// deliberately excluded, so the denominator stays honest (rule 12).
type SkippedCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// EntrypointsResult is the response body for GET /api/flows/entrypoints.
type EntrypointsResult struct {
	Entrypoints []EntrypointItem `json:"entrypoints"`
	Skipped     []SkippedCount   `json:"skipped"`
}

// Entrypoints lists every entrypoint node (HTTP handlers/routes, subscribers,
// workers, gRPC/GraphQL handlers, plus functions with meta.root_kind ==
// "entrypoint"), scoped to service when non-empty and kind when non-empty.
func Entrypoints(idx *AdjacencyIndex, service, kind string) EntrypointsResult {
	skipped := map[string]int{}
	var items []EntrypointItem
	for _, n := range idx.Nodes {
		if service != "" && n.Service != service {
			continue
		}
		k, skipRK, ok := ClassifyEntrypoint(n)
		if !ok {
			if skipRK != "" {
				skipped[skipRK]++
			}
			continue
		}
		// Test-defined handlers/routes/subscribers (in-spec Sinatra apps,
		// fixture workers, mock gRPC servers) are not a production entry
		// surface. Exclude them, but bucket the count so the denominator
		// stays honest (rule 12).
		if IsTestFilePath(n.File) {
			skipped["test_file"]++
			continue
		}
		items = append(items, EntrypointItem{
			NodeID:  n.ID,
			Kind:    k,
			Label:   n.Label,
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			EndLine: n.EndLine,
			Channel: entrypointChannel(idx, n),
		})
	}
	if kind != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.Kind == kind {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Service != items[j].Service {
			return items[i].Service < items[j].Service
		}
		if items[i].File != items[j].File {
			return items[i].File < items[j].File
		}
		if items[i].Line != items[j].Line {
			return items[i].Line < items[j].Line
		}
		return items[i].NodeID < items[j].NodeID
	})
	if items == nil {
		items = []EntrypointItem{}
	}

	var skippedList []SkippedCount
	for t, c := range skipped {
		skippedList = append(skippedList, SkippedCount{Type: t, Count: c})
	}
	sort.Slice(skippedList, func(i, j int) bool { return skippedList[i].Type < skippedList[j].Type })
	if skippedList == nil {
		skippedList = []SkippedCount{}
	}

	return EntrypointsResult{Entrypoints: items, Skipped: skippedList}
}

// entrypointChannel derives the "method+path or queue/topic" summary for an
// entrypoint node, when derivable from node meta or an adjacent channel node.
// Returns "" when nothing is derivable — never guessed.
func entrypointChannel(idx *AdjacencyIndex, n *Node) string {
	method, path := n.Meta["method"], n.Meta["path"]
	if method != "" || path != "" {
		return strings.TrimSpace(method + " " + path)
	}
	for _, e := range idx.InEdges[n.ID] {
		if cn := idx.Nodes[e.From]; cn != nil && cn.Type == NodeTypeChannel {
			return channelLabel(cn)
		}
	}
	for _, e := range idx.OutEdges[n.ID] {
		if cn := idx.Nodes[e.To]; cn != nil && cn.Type == NodeTypeChannel {
			return channelLabel(cn)
		}
	}
	return ""
}

// channelLabel renders a channel node's identity for display: exchange +
// routing_key when present, else the node's own label.
func channelLabel(cn *Node) string {
	ex, rk := cn.Meta["exchange"], cn.Meta["routing_key"]
	if ex != "" || rk != "" {
		return strings.TrimSpace(ex + " " + rk)
	}
	return cn.Label
}

// FlowHop is one node in a UB.5 flow chain — the token-lean shape shared by
// /api/flows/through, /api/flows/paths, and /api/flows/refine.
type FlowHop struct {
	NodeID            string `json:"node_id"`
	Label             string `json:"label"`
	Service           string `json:"service"`
	EdgeType          string `json:"edge_type,omitempty"`
	EdgeLabel         string `json:"edge_label,omitempty"`
	CrossService      bool   `json:"cross_service,omitempty"`
	Confidence        string `json:"confidence,omitempty"`
	VerificationState string `json:"verification_state,omitempty"`
}

func flowHopFromNode(n *Node) FlowHop {
	return FlowHop{NodeID: n.ID, Label: n.Label, Service: n.Service}
}

// EntrypointRef is the trimmed entrypoint identity attached to a flows/through entry.
type EntrypointRef struct {
	NodeID  string `json:"node_id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

func EntrypointRefFromNode(n *Node, kind string) EntrypointRef {
	return EntrypointRef{NodeID: n.ID, Kind: kind, Label: n.Label, Service: n.Service, File: n.File, Line: n.Line}
}

// FlowEntry is one entrypoint-to-target chain in a FlowsThroughResult.
type FlowEntry struct {
	Entrypoint EntrypointRef `json:"entrypoint"`
	Chain      []FlowHop     `json:"chain"`
}

// FlowsThroughResult is the response body for GET /api/flows/through/{id}.
type FlowsThroughResult struct {
	Flows     []FlowEntry `json:"flows"`
	Truncated bool        `json:"truncated"`
	// DeadEnd is true when target has zero outgoing flow edges (contains/
	// declares excluded) — an empty Flows in that case means the node
	// genuinely has nothing downstream (e.g. a goroutine whose body only
	// calls stdlib/vendor code), not an unresolved link the linker missed.
	// Lets the UI tell "nothing to find" apart from "something's unresolved".
	DeadEnd bool `json:"dead_end"`
}

// flowEdgeTypes are edge types the UB.5 flow engine follows; EdgeTypeContains
// is structural (where a node lives, not what depends on it) and is excluded
// from every flow/path/seam query in this file.
func isFlowEdge(t EdgeType) bool {
	return t != EdgeTypeContains && t != EdgeTypeDeclares
}

// HasOutgoingFlowEdge reports whether id has any outgoing edge the flow
// engine follows (contains/declares excluded) — a cheap existence check for
// callers (e.g. the flows/through empty-state message) that only need to
// know whether a node is a genuine dead end, not the full edge list.
func HasOutgoingFlowEdge(idx *AdjacencyIndex, id string) bool {
	for _, e := range idx.OutEdges[id] {
		if isFlowEdge(e.Type) {
			return true
		}
	}
	return false
}

// sortedFlowEdges returns id's outgoing flow edges (contains/declares
// excluded) ordered by (type, to) for deterministic traversal.
func sortedFlowEdges(idx *AdjacencyIndex, id string) []*Edge {
	var out []*Edge
	for _, e := range idx.OutEdges[id] {
		if isFlowEdge(e.Type) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].To < out[j].To
	})
	return out
}

// pathToFlowHops converts a node-id path plus its connecting edges into a
// FlowHop chain. len(edges) == len(nodes)-1.
func pathToFlowHops(idx *AdjacencyIndex, nodes []string, edges []*Edge) []FlowHop {
	hops := make([]FlowHop, len(nodes))
	for i, id := range nodes {
		n := idx.Nodes[id]
		hops[i] = flowHopFromNode(n)
		if i > 0 {
			e := edges[i-1]
			hops[i].EdgeType = string(e.Type)
			hops[i].EdgeLabel = e.Label
			hops[i].Confidence = e.Confidence
			hops[i].VerificationState = e.VerificationState
			from, to := idx.Nodes[e.From], idx.Nodes[e.To]
			if from != nil && to != nil && from.Service != to.Service {
				hops[i].CrossService = true
			}
		}
	}
	return hops
}

// FlowPath is one path found by KShortestFlowPaths.
type FlowPath struct {
	Chain []FlowHop `json:"chain"`
}

// FlowPathsResult is the response body for GET /api/flows/paths.
type FlowPathsResult struct {
	Paths     []FlowPath `json:"paths"`
	Reachable bool       `json:"reachable"`
}

// maxPathsExplored bounds KShortestFlowPaths' BFS-with-path-copy search so a
// dense graph cannot make one query run unbounded: paths are explored
// shortest-first (FIFO), so the cap only ever discards paths at least as long
// as whatever was already found, never a shorter one.
const maxPathsExplored = 20000

// KShortestFlowPaths finds up to k shortest simple paths from fromID to toID
// over directed flow edges (contains/declares excluded), via BFS with path
// copies. Paths are ranked by (length, lexical edge-id sequence) for
// determinism (rule 2). Returns Reachable=false (not an error) when no path
// exists.
func KShortestFlowPaths(idx *AdjacencyIndex, fromID, toID string, k, maxDepth int) (*FlowPathsResult, error) {
	if _, ok := idx.Nodes[fromID]; !ok {
		return nil, fmt.Errorf("node not found: %s", fromID)
	}
	if _, ok := idx.Nodes[toID]; !ok {
		return nil, fmt.Errorf("node not found: %s", toID)
	}
	if k <= 0 {
		k = 5
	}
	if k > 20 {
		k = 20
	}
	if maxDepth <= 0 {
		maxDepth = 10
	}
	if maxDepth > 50 {
		maxDepth = 50
	}

	type pathState struct {
		nodes  []string
		edges  []*Edge
		onPath map[string]bool
	}

	if fromID == toID {
		return &FlowPathsResult{Paths: []FlowPath{}, Reachable: false}, nil
	}

	queue := []pathState{{nodes: []string{fromID}, onPath: map[string]bool{fromID: true}}}
	var found []pathState
	explored := 0

	for len(queue) > 0 && explored < maxPathsExplored {
		cur := queue[0]
		queue = queue[1:]
		explored++
		if len(cur.nodes)-1 >= maxDepth {
			continue
		}
		curID := cur.nodes[len(cur.nodes)-1]
		for _, e := range sortedFlowEdges(idx, curID) {
			if cur.onPath[e.To] {
				continue
			}
			if _, ok := idx.Nodes[e.To]; !ok {
				continue
			}
			nextNodes := append(append([]string{}, cur.nodes...), e.To)
			nextEdges := append(append([]*Edge{}, cur.edges...), e)
			nextOnPath := make(map[string]bool, len(cur.onPath)+1)
			for id := range cur.onPath {
				nextOnPath[id] = true
			}
			nextOnPath[e.To] = true
			ns := pathState{nodes: nextNodes, edges: nextEdges, onPath: nextOnPath}
			if e.To == toID {
				found = append(found, ns)
				continue
			}
			queue = append(queue, ns)
		}
	}

	sort.Slice(found, func(i, j int) bool {
		if len(found[i].edges) != len(found[j].edges) {
			return len(found[i].edges) < len(found[j].edges)
		}
		return edgeIDSeq(found[i].edges) < edgeIDSeq(found[j].edges)
	})
	if len(found) > k {
		found = found[:k]
	}

	paths := make([]FlowPath, len(found))
	for i, p := range found {
		paths[i] = FlowPath{Chain: pathToFlowHops(idx, p.nodes, p.edges)}
	}
	return &FlowPathsResult{Paths: paths, Reachable: len(paths) > 0}, nil
}

func edgeIDSeq(edges []*Edge) string {
	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.ID
	}
	return strings.Join(ids, "\x00")
}

// CandidateNode is one immediate flow-edge neighbor surfaced by
// /api/flows/refine as a next-waypoint suggestion.
type CandidateNode struct {
	NodeID      string `json:"node_id"`
	Label       string `json:"label"`
	Service     string `json:"service"`
	Type        string `json:"type"`
	ViaEdgeType string `json:"via_edge_type"`
}

// Candidates groups next-waypoint suggestions by direction.
type Candidates struct {
	Upstream   []CandidateNode `json:"upstream"`
	Downstream []CandidateNode `json:"downstream"`
}

// RefineResult is the response body for GET /api/flows/refine.
type RefineResult struct {
	Chain      []FlowHop  `json:"chain"`
	Candidates Candidates `json:"candidates"`
}

// RefineWaypoints validates that consecutive waypoints are connected (each
// pair via KShortestFlowPaths with k=1) and stitches the resulting chain,
// plus the immediate flow-edge neighbors of the endpoints as candidate next
// waypoints. direction == "backward" walks the given waypoint list in
// reverse before stitching. Returns an error naming the first disconnected
// pair, never a partial/best-effort chain.
func RefineWaypoints(idx *AdjacencyIndex, waypointIDs []string, direction string) (*RefineResult, error) {
	if len(waypointIDs) == 0 {
		return nil, fmt.Errorf("no waypoints given")
	}
	for _, id := range waypointIDs {
		if _, ok := idx.Nodes[id]; !ok {
			return nil, fmt.Errorf("node not found: %s", id)
		}
	}

	ordered := waypointIDs
	if direction == "backward" {
		ordered = make([]string, len(waypointIDs))
		for i, id := range waypointIDs {
			ordered[len(waypointIDs)-1-i] = id
		}
	}

	var chain []FlowHop
	if len(ordered) == 1 {
		chain = []FlowHop{flowHopFromNode(idx.Nodes[ordered[0]])}
	} else {
		for i := 0; i < len(ordered)-1; i++ {
			from, to := ordered[i], ordered[i+1]
			res, err := KShortestFlowPaths(idx, from, to, 1, 50)
			if err != nil {
				return nil, err
			}
			if !res.Reachable {
				return nil, fmt.Errorf("waypoints not connected: %s -> %s", from, to)
			}
			seg := res.Paths[0].Chain
			if i == 0 {
				chain = append(chain, seg...)
			} else {
				chain = append(chain, seg[1:]...)
			}
		}
	}

	return &RefineResult{
		Chain: chain,
		Candidates: Candidates{
			Upstream:   neighborCandidates(idx, ordered[0], "in"),
			Downstream: neighborCandidates(idx, ordered[len(ordered)-1], "out"),
		},
	}, nil
}

// neighborCandidates lists the immediate flow-edge neighbors of nodeID in the
// given direction ("in" or "out"), sorted by (edge type, neighbor id).
func neighborCandidates(idx *AdjacencyIndex, nodeID, direction string) []CandidateNode {
	var edges []*Edge
	if direction == "in" {
		edges = idx.InEdges[nodeID]
	} else {
		edges = idx.OutEdges[nodeID]
	}
	var flowEdges []*Edge
	for _, e := range edges {
		if isFlowEdge(e.Type) {
			flowEdges = append(flowEdges, e)
		}
	}
	sort.Slice(flowEdges, func(i, j int) bool {
		if flowEdges[i].Type != flowEdges[j].Type {
			return flowEdges[i].Type < flowEdges[j].Type
		}
		ni, nj := flowEdges[i].To, flowEdges[j].To
		if direction == "in" {
			ni, nj = flowEdges[i].From, flowEdges[j].From
		}
		return ni < nj
	})

	out := make([]CandidateNode, 0, len(flowEdges))
	for _, e := range flowEdges {
		nid := e.To
		if direction == "in" {
			nid = e.From
		}
		n := idx.Nodes[nid]
		if n == nil {
			continue
		}
		out = append(out, CandidateNode{
			NodeID: n.ID, Label: n.Label, Service: n.Service, Type: string(n.Type),
			ViaEdgeType: string(e.Type),
		})
	}
	return out
}
