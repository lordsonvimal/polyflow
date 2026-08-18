package graph

// TraversalMode selects BFS or DFS.
type TraversalMode int

const (
	BFS TraversalMode = iota
	DFS
)

// TraversalResult holds a discovered node and the edge that led to it.
type TraversalResult struct {
	Node  *Node
	Via   *Edge
	Depth int
	// Structural is true when this node's path back to the traversal root
	// passes through at least one structural edge (contains, declares,
	// instantiates, uses_type — see structuralEdgeTypes). Such a node is
	// reachable through the code's shape rather than a verified call chain:
	// e.g. "main calls NewAgentService, which instantiates AgentService,
	// which contains RegisterOrUpdate" is a real, correctly-followed edge
	// chain, but main never calls RegisterOrUpdate. Callers is not filtered
	// by this (dropping the nodes costs recall — see BlastRadiusPolicy) but
	// output layers should label it so a reader isn't misled into treating
	// every listed node as a genuine call-site.
	Structural bool
}

// structuralEdgeTypes are edges that describe where a symbol LIVES or is
// TYPED, not a runtime call. A path that includes one of these is structural:
// it over-approximates reachability the way a struct's constructor "reaches"
// every method on that struct, whether or not the method is ever called.
var structuralEdgeTypes = map[EdgeType]bool{
	EdgeTypeContains:     true,
	EdgeTypeDeclares:     true,
	EdgeTypeInstantiates: true,
	EdgeTypeUsesType:     true,
}

// TraversalPolicy shapes a walk for blast-radius use. The zero value is the
// raw graph walk — every edge followed, every node reported — which is what
// Traverse has always done and what graph-shape consumers (mermaid, trace)
// still want.
//
// The distinction it draws is containment vs causation. `contains`
// (service→file→declaration, struct→method) and `declares` (scope→variable)
// say where a node LIVES, not what depends on it. Following them one hop is
// useful context; walking PAST them silently changes the question, because a
// file node then propagates along `imports` to every file that imports it and
// a struct node propagates along `uses_type` to every user of the type. On
// fleet-juniper that turns a function's p90 blast radius from 4 files
// into 46 and its worst case from 36 into 131 — all of it correct edges
// answering a question nobody asked.
type TraversalPolicy struct {
	// TerminalEdges are followed exactly one hop: the node they reach is
	// reported, but the walk does not continue out of it.
	TerminalEdges map[EdgeType]bool

	// DropLocals skips `variable` nodes whose scope is `captured` — the
	// closure-captured locals (`ch`, `ctx`, `errChan`) that make up ~13% of a
	// forward blast radius. Module, package, global and instance variables are
	// shared state and are always kept.
	DropLocals bool
}

// BlastRadiusPolicy is the default shape for `impact` answers: locals
// dropped, containment expanded.
//
// Containment is deliberately NOT terminal by default, though it is the
// bigger win on paper. Measured on fleet-juniper, making it terminal cuts
// a function's p90 blast radius from 46 files to 4 and its worst case from 131
// to 36 — but it also costs real recall, because where call resolution is weak
// the containment hop IS the resolution mechanism. In lobsters the only path
// from `Story#already_posted_recently?` to its verified caller is
// `method ←contains— Story ←instantiates— StoriesController#create`: a true
// positive reached by a mechanism that would equally reach every other
// constructor of Story. Suppressing it drops that repo's recall 0.944→0.833.
//
// Recall is the priority for agent context, so the tightening ships as
// ContainmentTerminal (CLI --stop-at-containers) and stays opt-in until the
// Ruby call-resolution gap that makes the hop load-bearing is closed.
func BlastRadiusPolicy() TraversalPolicy {
	return TraversalPolicy{DropLocals: true}
}

// ContainmentTerminal returns BlastRadiusPolicy with containment edges made
// terminal — the precision-first shape. See BlastRadiusPolicy for its cost.
func ContainmentTerminal() TraversalPolicy {
	p := BlastRadiusPolicy()
	p.TerminalEdges = map[EdgeType]bool{
		EdgeTypeContains: true,
		EdgeTypeDeclares: true,
	}
	return p
}

// isLocalVariable reports whether n is a closure-captured local. The scope
// meta is written by the variable extractors; anything else (module, package,
// global, instance) is shared state that a change can genuinely break.
func isLocalVariable(n *Node) bool {
	return n != nil && n.Type == NodeTypeVariable && n.Meta["scope"] == "captured"
}

// Traverse walks the graph from startID in the given direction using BFS or DFS.
// direction: "out" follows OutEdges, "in" follows InEdges.
// maxDepth <= 0 means unlimited.
func Traverse(idx *AdjacencyIndex, startID string, direction string, mode TraversalMode, maxDepth int) []TraversalResult {
	return TraverseWithPolicy(idx, startID, direction, mode, maxDepth, TraversalPolicy{})
}

// TraverseWithPolicy is Traverse with blast-radius shaping applied. See
// TraversalPolicy.
func TraverseWithPolicy(idx *AdjacencyIndex, startID string, direction string, mode TraversalMode, maxDepth int, policy TraversalPolicy) []TraversalResult {
	if _, ok := idx.Nodes[startID]; !ok {
		return nil
	}

	var results []TraversalResult
	visited := make(map[string]bool)
	visited[startID] = true

	type item struct {
		nodeID string
		via    *Edge
		depth  int
		// terminal marks a node reached by a TerminalEdges hop: reported, but
		// not expanded.
		terminal bool
		// structural marks a node whose path back to startID already crossed
		// a structuralEdgeTypes edge; it propagates to every descendant.
		structural bool
	}

	queue := []item{{nodeID: startID, depth: 0}}

	for len(queue) > 0 {
		var cur item
		if mode == BFS {
			cur, queue = queue[0], queue[1:]
		} else {
			cur, queue = queue[len(queue)-1], queue[:len(queue)-1]
		}

		if cur.depth > 0 {
			results = append(results, TraversalResult{
				Node:       idx.Nodes[cur.nodeID],
				Via:        cur.via,
				Depth:      cur.depth,
				Structural: cur.structural,
			})
		}

		if maxDepth > 0 && cur.depth >= maxDepth {
			continue
		}
		if cur.terminal {
			continue
		}

		var edges []*Edge
		if direction == "in" {
			edges = idx.InEdges[cur.nodeID]
		} else {
			edges = idx.OutEdges[cur.nodeID]
		}

		for _, e := range edges {
			next := e.To
			if direction == "in" {
				next = e.From
			}
			if visited[next] {
				continue
			}
			if policy.DropLocals && isLocalVariable(idx.Nodes[next]) {
				// Mark visited so a second edge into the same local does not
				// re-test it; the node is dropped, not deferred.
				visited[next] = true
				continue
			}
			visited[next] = true
			queue = append(queue, item{
				nodeID:     next,
				via:        e,
				depth:      cur.depth + 1,
				terminal:   policy.TerminalEdges[e.Type],
				structural: cur.structural || structuralEdgeTypes[e.Type],
			})
		}
	}

	return results
}

// Ancestors returns all nodes that can reach startID (upstream callers).
func Ancestors(idx *AdjacencyIndex, startID string, maxDepth int) []TraversalResult {
	return Traverse(idx, startID, "in", BFS, maxDepth)
}

// AncestorsWithPolicy is Ancestors with blast-radius shaping applied. Ancestors
// itself stays the raw walk: its other callers (mermaid, trace, context) draw
// the graph rather than answer "what breaks", and a node missing from a diagram
// is a worse failure than a local variable in it.
func AncestorsWithPolicy(idx *AdjacencyIndex, startID string, maxDepth int, policy TraversalPolicy) []TraversalResult {
	return TraverseWithPolicy(idx, startID, "in", BFS, maxDepth, policy)
}

// Descendants returns all nodes reachable from startID (downstream callees).
func Descendants(idx *AdjacencyIndex, startID string, maxDepth int) []TraversalResult {
	return Traverse(idx, startID, "out", BFS, maxDepth)
}
