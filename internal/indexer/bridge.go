package indexer

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// BridgeResult is the output of BuildBridge: the small set of cross-service
// edges a fleet sync (Tier GR, GR.2) persists to bridge.db, plus a
// lightweight endpoint node stub for each edge — just enough to satisfy
// bridge.db's own nodes/edges foreign key, not a copy of any member's full
// node.
type BridgeResult struct {
	Nodes []graph.Node
	Edges []graph.Edge
}

// BuildBridge reruns exactly the scopeCrossService linking passes
// (link_passes.go) over the union of every fleet member's own
// already-indexed node/edge set — the same call shape Relink's loop already
// makes for a scoped relink, just sourced from N independent members
// instead of one merged store — and returns only what those passes newly
// produce: the resulting cross-service edges, plus an owner_service-tagged
// stub node for each edge endpoint. A stub's real content lives in its
// owner's own local graph.db; GR.3's query-time resolver reads
// meta["owner_service"] off it to know which store to open.
//
// allNodes/allEdges must be the plain union of every fleet member's
// graph.BuildIndex result (node IDs are disjoint across services by
// construction — FR.0's (service, file, line, name, ...) keying — so a
// union is safe without a merge step). contractsDir may add
// workspace-custom contract rules on top of the embedded defaults (G.5),
// same meaning as Options.ContractsDir.
func BuildBridge(ctx context.Context, links []workspace.Link, contractsDir string, allNodes []graph.Node, allEdges []graph.Edge) (*BridgeResult, error) {
	workFile, err := os.CreateTemp("", "polyflow-fleetsync-bridge-*.db")
	if err != nil {
		return nil, fmt.Errorf("create scratch bridge work db: %w", err)
	}
	workPath := workFile.Name()
	workFile.Close()
	defer os.Remove(workPath)

	workStore, err := graph.NewBuildStore(workPath)
	if err != nil {
		return nil, fmt.Errorf("open scratch bridge work db: %w", err)
	}
	defer workStore.Close()

	// Every member's already-persisted rows must exist in the work store
	// too, or a cross-service pass's edge insert (edges REFERENCES
	// nodes(id)) fails the moment it touches one of them.
	seedBW := graph.NewFreshBatchWriter(workStore)
	for i := range allNodes {
		if err := seedBW.AddNode(ctx, &allNodes[i]); err != nil {
			return nil, fmt.Errorf("seed bridge work db: %w", err)
		}
	}
	if err := seedBW.Flush(ctx); err != nil {
		return nil, fmt.Errorf("seed bridge work db: %w", err)
	}

	origEdgeCount := len(allEdges)

	st := &linkPipelineState{
		ctx:      ctx,
		store:    workStore,
		bw:       graph.NewBatchWriter(workStore),
		cfg:      &workspace.WorkspaceConfig{Links: links},
		opts:     Options{ContractsDir: contractsDir},
		stats:    &Stats{},
		allNodes: allNodes,
		allEdges: allEdges,
	}

	for _, pass := range buildLinkPasses(st) {
		if pass.scope != scopeCrossService {
			continue
		}
		if err := pass.exec(); err != nil {
			return nil, fmt.Errorf("bridge build pass %s: %w", pass.name, err)
		}
	}

	nodeByID := make(map[string]graph.Node, len(st.allNodes))
	for _, n := range st.allNodes {
		nodeByID[n.ID] = n
	}

	// The scopeCrossService passes are the ones whose *correctness* depends
	// on multi-service visibility, not ones that exclusively emit
	// cross-service edges — several (e.g. the contract engine's route
	// matching) also derive same-service edges (a same-repo frontend
	// navigating its own backend) as a side effect. Those are already in
	// that member's own local graph.db; keep only edges that actually cross
	// a service boundary, so a bridge stays the few-hundred-row artifact
	// the plan doc promises instead of ballooning back toward a full merge.
	var newEdges []graph.Edge
	for _, e := range st.allEdges[origEdgeCount:] {
		from, to := nodeByID[e.From], nodeByID[e.To]
		if from.Service != to.Service {
			newEdges = append(newEdges, e)
		}
	}

	referenced := make(map[string]bool)
	for _, e := range newEdges {
		referenced[e.From] = true
		referenced[e.To] = true
	}
	refIDs := make([]string, 0, len(referenced))
	for id := range referenced {
		refIDs = append(refIDs, id)
	}
	sort.Strings(refIDs)

	bridgeNodes := make([]graph.Node, 0, len(refIDs))
	for _, id := range refIDs {
		n, ok := nodeByID[id]
		if !ok {
			continue
		}
		cp := n
		nm := make(map[string]string, len(n.Meta)+1)
		for k, v := range n.Meta {
			nm[k] = v
		}
		nm["owner_service"] = n.Service
		cp.Meta = nm
		bridgeNodes = append(bridgeNodes, cp)
	}

	sort.Slice(newEdges, func(i, j int) bool { return newEdges[i].ID < newEdges[j].ID })

	return &BridgeResult{Nodes: bridgeNodes, Edges: newEdges}, nil
}
