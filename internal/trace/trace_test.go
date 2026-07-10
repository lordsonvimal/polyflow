package trace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// buildIdx wires nodes and edges into an AdjacencyIndex.
func buildIdx(nodes []graph.Node, edges []graph.Edge) *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	for i := range nodes {
		idx.AddNode(&nodes[i])
	}
	for i := range edges {
		idx.AddEdge(&edges[i])
	}
	return idx
}

func linearGraph() *graph.AdjacencyIndex {
	return buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Service: "svc-1", Type: graph.NodeTypeFunction},
			{ID: "b", Label: "B", Service: "svc-1", Type: graph.NodeTypePublisher},
			{ID: "c", Label: "C", Service: "svc-2", Type: graph.NodeTypeSubscriber},
			{ID: "d", Label: "D", Service: "svc-2", Type: graph.NodeTypeFunction},
		},
		[]graph.Edge{
			{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls},
			{ID: "e2", From: "b", To: "c", Type: graph.EdgeTypePublishes, Confidence: graph.ConfidenceStatic},
			{ID: "e3", From: "c", To: "d", Type: graph.EdgeTypeCalls},
		},
	)
}

func TestRun_UnknownRoot(t *testing.T) {
	assert.Nil(t, Run(linearGraph(), "nope", "forward", 5))
}

func TestRun_ForwardChain(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 1)
	assert.Equal(t,
		"(svc-1) A -[calls]-> B -[publishes]-> ‖svc-2‖ C -[calls]-> D",
		r.Chains[0].Text)
	assert.Len(t, r.Nodes, 3, "flat BFS list excludes the root")
	assert.Equal(t, []string{"calls", "publishes"}, r.EdgeTypes)
	assert.Equal(t, []string{"svc-1", "svc-2"}, r.Services)
	assert.False(t, r.Truncated)

	// The cross-service hop is marked.
	hops := r.Chains[0].Hops
	require.Len(t, hops, 4)
	assert.False(t, hops[1].CrossService)
	assert.True(t, hops[2].CrossService)
}

func TestRun_BackwardChainReadsInFlowOrder(t *testing.T) {
	r := Run(linearGraph(), "d", "backward", 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 1)
	// Backward chains are reversed: they still read source → root.
	assert.Equal(t,
		"(svc-1) A -[calls]-> B -[publishes]-> ‖svc-2‖ C -[calls]-> D",
		r.Chains[0].Text)
}

func TestRun_BothDirections(t *testing.T) {
	r := Run(linearGraph(), "b", "both", 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 2)
	assert.Equal(t, "(svc-1) A -[calls]-> B", r.Chains[0].Text)
	assert.Equal(t, "(svc-1) B -[publishes]-> ‖svc-2‖ C -[calls]-> D", r.Chains[1].Text)
}

func TestRun_DepthLimitCutsChain(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 2)
	require.Len(t, r.Chains, 1)
	assert.Equal(t, "(svc-1) A -[calls]-> B -[publishes]-> ‖svc-2‖ C", r.Chains[0].Text)
}

func TestRun_CycleTerminates(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Service: "s"},
			{ID: "b", Label: "B", Service: "s"},
		},
		[]graph.Edge{
			{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls},
			{ID: "e2", From: "b", To: "a", Type: graph.EdgeTypeCalls},
		},
	)
	r := Run(idx, "a", "forward", 0)
	require.Len(t, r.Chains, 1, "cycle must not loop forever")
	assert.Equal(t, "(s) A -[calls]-> B", r.Chains[0].Text)
}

func TestRun_BranchingProducesOneChainPerLeaf(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Service: "s"},
			{ID: "b", Label: "B", Service: "s"},
			{ID: "c", Label: "C", Service: "s"},
		},
		[]graph.Edge{
			{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls},
			{ID: "e2", From: "a", To: "c", Type: graph.EdgeTypeSpawns},
		},
	)
	r := Run(idx, "a", "forward", 0)
	require.Len(t, r.Chains, 2)
	// Deterministic order: edges sorted by (type, neighbor).
	assert.Equal(t, "(s) A -[calls]-> B", r.Chains[0].Text)
	assert.Equal(t, "(s) A -[spawns]-> C", r.Chains[1].Text)
}

func TestRun_TruncationCap(t *testing.T) {
	// A root fanning out to MaxChains+20 leaves.
	nodes := []graph.Node{{ID: "root", Label: "R", Service: "s"}}
	var edges []graph.Edge
	for i := 0; i < MaxChains+20; i++ {
		id := "n" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		nodes = append(nodes, graph.Node{ID: id, Label: id, Service: "s"})
		edges = append(edges, graph.Edge{ID: "e" + id, From: "root", To: id, Type: graph.EdgeTypeCalls})
	}
	r := Run(buildIdx(nodes, edges), "root", "forward", 0)
	assert.Len(t, r.Chains, MaxChains)
	assert.True(t, r.Truncated)
}

func TestRun_PartialConfidenceMarked(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "q", Label: "db.Find", Service: "s", Type: graph.NodeTypeDatastore},
			{ID: "store", Label: "sqlite", Service: "s", Type: graph.NodeTypeDatastore},
		},
		[]graph.Edge{
			{ID: "e", From: "q", To: "store", Type: graph.EdgeTypeQueries, Confidence: graph.ConfidencePartial},
		},
	)
	r := Run(idx, "q", "forward", 0)
	require.Len(t, r.Chains, 1)
	assert.Equal(t, "(s) db.Find -[queries?]-> sqlite", r.Chains[0].Text,
		"partial/unknown confidence edges carry a trailing ?")
}

func TestRun_JSONCarriesVersionAndEdgeMeta(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "up", Label: "UploadReport", Service: "dsw-agent", Type: graph.NodeTypeFunction},
			{ID: "s3", Label: "PutObject", Service: "dsw-agent", Type: graph.NodeTypeExternalService,
				Meta: map[string]string{
					"package":          "github.com/aws/aws-sdk-go",
					"resolved_version": "1.55.8",
					"cloud_service":    "s3",
				}},
		},
		[]graph.Edge{
			{ID: "e", From: "up", To: "s3", Type: graph.EdgeTypeCloudCall,
				Meta: map[string]string{"via": "sdk"}},
		},
	)
	r := Run(idx, "up", "forward", 0)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	js := string(data)
	for _, want := range []string{
		`"package":"github.com/aws/aws-sdk-go"`,
		`"resolved_version":"1.55.8"`,
		`"cloud_service":"s3"`,
		`"edge_type":"cloud_call"`,
		`"edge_meta":{"via":"sdk"}`,
	} {
		assert.True(t, strings.Contains(js, want), "trace JSON missing %s\n%s", want, js)
	}
}

func TestAttachUnresolved_ScopedToTracedFiles(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Service: "svc-1", Type: graph.NodeTypeFunction, File: "a.go"},
			{ID: "b", Label: "B", Service: "svc-1", Type: graph.NodeTypeFunction, File: "b.go"},
		},
		[]graph.Edge{{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls}},
	)
	r := Run(idx, "a", "forward", 5)
	require.NotNil(t, r)

	r.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "svc-1", File: "b.go", Line: 4, Name: "mystery", Kind: "call_ref"},
		{Service: "svc-1", File: "elsewhere.go", Line: 9, Name: "other", Kind: "call_ref"},
	})

	require.Len(t, r.Unresolved, 1)
	assert.Equal(t, "mystery", r.Unresolved[0].Name)
	assert.Contains(t, r.UnresolvedNote, "verify this 1 unresolved reference manually")
}

func TestAttachUnresolved_CleanTraceHasEmptySectionAndNoNote(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 5)
	require.NotNil(t, r)

	r.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "svc-9", File: "unrelated.go", Line: 1, Name: "x", Kind: "import_ref"},
	})

	assert.Empty(t, r.UnresolvedNote)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"unresolved":[]`)
}
