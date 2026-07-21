package trace

// Phase 10 proof: a snapshot of every edge type produced by the pattern
// fixtures (matcher passes + linker passes), and a completeness check that
// every one of those edge types survives into the trace JSON output.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// fixtureGraph indexes every positive pattern fixture the way production
// indexing does: all pattern files of a language active at once (one service
// per pattern file), then the shared linking passes over the union.
func fixtureGraph(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()

	langDirs, err := filepath.Glob(filepath.Join(patternsRoot, "*"))
	require.NoError(t, err)

	var allNodes []graph.Node
	var allEdges []graph.Edge
	svcFiles := map[string][]string{}

	for _, dir := range langDirs {
		yamls, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if len(yamls) == 0 {
			continue
		}
		reg := patterns.NewRegistry()
		var lang string
		for _, yp := range yamls {
			pf, err := patterns.LoadFile(yp)
			require.NoError(t, err)
			reg.RegisterFile(pf)
			lang = pf.Language
		}
		m := patterns.NewTreeSitterMatcher(reg)

		for _, yp := range yamls {
			service := strings.TrimSuffix(filepath.Base(yp), ".yaml")
			inputs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(yp, ".yaml")+"_test", "input.*"))
			for _, input := range inputs {
				src, err := os.ReadFile(input)
				require.NoError(t, err)
				grammar := grammarForExt(filepath.Ext(input))
				if grammar == "" {
					continue
				}
				results, err := m.MatchWithGrammar(lang, grammar, input, src)
				require.NoError(t, err)
				nodes, edges := m.MatchToNodes(service, results)
				allNodes = append(allNodes, nodes...)
				allEdges = append(allEdges, edges...)
				svcFiles[service] = append(svcFiles[service], input)
			}
		}
	}

	// Services with datastore call sites get a logical store node, as
	// deps.DatastoreNodes would derive from their resolved drivers.
	withStore := map[string]bool{}
	for i := range allNodes {
		n := &allNodes[i]
		if n.Type == graph.NodeTypeDatastore && n.Meta["kind"] == "call" && !withStore[n.Service] {
			withStore[n.Service] = true
			allNodes = append(allNodes, graph.Node{
				ID: n.Service + ":datastore:sqlite", Type: graph.NodeTypeDatastore,
				Label: "sqlite", Service: n.Service,
				Meta: map[string]string{"kind": "store", "engine": "sqlite"},
			})
		}
	}

	// The same linking passes the indexer runs.
	jsEdges, _, _, _ := linker.NewJSLinker().LinkJS(allNodes, allEdges, svcFiles)
	allEdges = append(allEdges, jsEdges...)
	allEdges = append(allEdges, linker.LinkRouteHandlers(allNodes)...)
	allEdges = append(allEdges, linker.LinkDatastores(allNodes)...)
	allEdges = append(allEdges, linker.LinkSSEClients(allNodes)...)

	// Broker hint pass, as a workspace links: entry would drive it — the
	// bunny fixture's exchange is variable-held, so only a hint can connect
	// it to the amqp091 consumer (the confirmed nextGen → dsw-agent shape).
	hintNodes, hintEdges := linker.LinkBrokerHints([]workspace.Link{
		{From: "bunny", To: "amqp091", Via: "rabbitmq", Exchange: "dsw.builds"},
	}, allNodes)
	allNodes = append(allNodes, hintNodes...)
	allEdges = append(allEdges, hintEdges...)

	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	hintedNodes := linker.ApplyHints(nil, allNodes, allEdges)
	eng := &contract.Engine{}
	contractResult := eng.Link(hintedNodes, contractRules, nil)
	allNodes = append(allNodes, contractResult.Nodes...)
	allEdges = append(allEdges, contractResult.Edges...)

	return allNodes, allEdges
}

func TestFixtureEdgeTypes_Snapshot(t *testing.T) {
	_, edges := fixtureGraph(t)

	set := map[string]bool{}
	for i := range edges {
		set[string(edges[i].Type)] = true
	}
	got := sortedKeys(set)

	goldenPath := filepath.Join("testdata", "edge_types.golden")
	goldenBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden file %s — fixture edge types are:\n%s",
			goldenPath, strings.Join(got, "\n"))
	}
	want := strings.Fields(strings.TrimSpace(string(goldenBytes)))
	sort.Strings(want)

	assert.Equal(t, want, got,
		"edge types produced by fixtures diverge from %s — update the golden deliberately if a new edge type was added", goldenPath)
}

// TestTraceJSON_IncludesEveryFixtureEdgeType proves the agent-facing JSON is
// complete: trace output over a graph containing one edge of every fixture
// edge type must surface all of them.
func TestTraceJSON_IncludesEveryFixtureEdgeType(t *testing.T) {
	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "edge_types.golden"))
	require.NoError(t, err)
	edgeTypes := strings.Fields(strings.TrimSpace(string(goldenBytes)))
	require.NotEmpty(t, edgeTypes)

	nodes := []graph.Node{{ID: "root", Label: "Root", Service: "s"}}
	var edges []graph.Edge
	for i, et := range edgeTypes {
		id := fmt.Sprintf("n%d", i)
		nodes = append(nodes, graph.Node{ID: id, Label: id, Service: "s"})
		edges = append(edges, graph.Edge{
			ID: "e" + id, From: "root", To: id, Type: graph.EdgeType(et),
		})
	}

	r := Run(buildIdx(nodes, edges), "root", "forward", 0, false, 0)
	require.NotNil(t, r)

	sorted := append([]string{}, edgeTypes...)
	sort.Strings(sorted)
	assert.Equal(t, sorted, r.EdgeTypes)

	data, err := json.Marshal(r)
	require.NoError(t, err)
	js := string(data)
	for _, et := range edgeTypes {
		assert.Contains(t, js, `"edge_type":"`+et+`"`, "trace JSON must carry edge type %s", et)
	}
}
