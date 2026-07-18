package trace

// Phase 10 proof: chain output asserted against the real fixture chains —
// the RabbitMQ cross-repo chain (Ruby bunny publisher → Go amqp091 consumer),
// the SSE broadcast-hub chain, and the WebSocket typed-dispatch chain.

import (
	"os"
	"path/filepath"
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

const patternsRoot = "../../patterns"

func grammarForExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx", ".jsx":
		return "tsx"
	case ".rb":
		return "ruby"
	case ".html":
		return "html"
	default:
		return ""
	}
}

// matchFixtureFiles runs the given pattern YAML files against one fixture
// source file and converts the matches to graph nodes/edges for the service.
func matchFixtureFiles(t *testing.T, service, fixturePath string, yamlPaths ...string) ([]graph.Node, []graph.Edge) {
	t.Helper()
	reg := patterns.NewRegistry()
	var lang string
	for _, yp := range yamlPaths {
		pf, err := patterns.LoadFile(yp)
		require.NoError(t, err)
		reg.RegisterFile(pf)
		lang = pf.Language
	}
	src, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	grammar := grammarForExt(filepath.Ext(fixturePath))
	require.NotEmpty(t, grammar)

	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.MatchWithGrammar(lang, grammar, fixturePath, src)
	require.NoError(t, err)
	return m.MatchToNodes(service, results)
}

func findNode(nodes []graph.Node, pred func(n *graph.Node) bool) *graph.Node {
	for i := range nodes {
		if pred(&nodes[i]) {
			return &nodes[i]
		}
	}
	return nil
}

// TestChains_RabbitMQCrossRepo traces the confirmed real chain: nextGen (Rails)
// publishes through bunny with the exchange held in a variable, dsw-agent (Go)
// consumes via amqp091; a workspace broker hint connects them.
func TestChains_RabbitMQCrossRepo(t *testing.T) {
	pubNodes, pubEdges := matchFixtureFiles(t, "nextgen",
		filepath.Join(patternsRoot, "ruby/bunny_test/input.rb"),
		filepath.Join(patternsRoot, "ruby/bunny.yaml"))
	subNodes, subEdges := matchFixtureFiles(t, "dsw-agent",
		filepath.Join(patternsRoot, "go/amqp091_test/input.go"),
		filepath.Join(patternsRoot, "go/amqp091.yaml"))

	allNodes := append(append([]graph.Node{}, pubNodes...), subNodes...)
	allEdges := append(append([]graph.Edge{}, pubEdges...), subEdges...)

	hintNodes, hintEdges := linker.LinkBrokerHints([]workspace.Link{
		{From: "nextgen", To: "dsw-agent", Via: "rabbitmq", Exchange: "dsw.builds"},
	}, allNodes)
	allNodes = append(allNodes, hintNodes...)
	allEdges = append(allEdges, hintEdges...)

	idx := buildIdx(allNodes, allEdges)

	root := findNode(pubNodes, func(n *graph.Node) bool {
		return n.Meta["pattern"] == "bunny_exchange_publish"
	})
	require.NotNil(t, root, "bunny exchange.publish call site must be matched")

	r := Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)
	require.NotEmpty(t, r.Chains)

	var hit string
	for _, c := range r.Chains {
		if strings.Contains(c.Text, "-[publishes]-> dsw.builds") &&
			strings.Contains(c.Text, "-[subscribes]-> ‖dsw-agent‖") {
			hit = c.Text
		}
	}
	require.NotEmpty(t, hit,
		"expected a chain nextgen publisher → dsw.builds channel → ‖dsw-agent‖ consumer, got:\n%s",
		chainTexts(r))
	assert.True(t, strings.HasPrefix(hit, "(nextgen) "), "chain starts at the publishing service: %s", hit)
	assert.Contains(t, r.Services, "dsw-agent")
	assert.Contains(t, r.EdgeTypes, "publishes")
	assert.Contains(t, r.EdgeTypes, "subscribes")
}

// TestChains_SSEHubFanout traces the chessleap hub shape: handleMove calls
// hub.Broadcast, whose fan-out reaches the per-connection hub.Subscribe call.
func TestChains_SSEHubFanout(t *testing.T) {
	nodes, edges := matchFixtureFiles(t, "chessleap",
		filepath.Join(patternsRoot, "go/sse_hub_test/input.go"),
		filepath.Join(patternsRoot, "go/sse_hub.yaml"),
		filepath.Join(patternsRoot, "go/functions.yaml"))
	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	contractResult := eng.Link(nodes, contractRules, nil)
	edges = append(edges, contractResult.Edges...)

	idx := buildIdx(nodes, edges)

	root := findNode(nodes, func(n *graph.Node) bool {
		return n.Label == "handleMove" && n.Type == graph.NodeTypeFunction
	})
	require.NotNil(t, root)

	r := Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)

	var hit string
	for _, c := range r.Chains {
		if strings.Contains(c.Text, "-[hub_broadcast]->") {
			hit = c.Text
		}
	}
	require.NotEmpty(t, hit, "expected a chain through the hub fan-out, got:\n%s", chainTexts(r))
	assert.True(t, strings.HasPrefix(hit, "(chessleap) handleMove -[calls]->"), hit)
	assert.True(t, strings.HasSuffix(hit, "-[hub_broadcast]-> Subscribe"), hit)
}

// TestChains_WebSocketTypedDispatch traces the tether shape: reportBattery
// sends {type: "battery"}, which the dispatch switch case for 'battery'
// handles.
func TestChains_WebSocketTypedDispatch(t *testing.T) {
	nodes, edges := matchFixtureFiles(t, "tether",
		filepath.Join(patternsRoot, "javascript/websocket_test/input.js"),
		filepath.Join(patternsRoot, "javascript/websocket.yaml"),
		filepath.Join(patternsRoot, "javascript/functions.yaml"))
	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	contractResult := eng.Link(nodes, contractRules, nil)
	edges = append(edges, contractResult.Edges...)

	idx := buildIdx(nodes, edges)

	root := findNode(nodes, func(n *graph.Node) bool {
		return n.Label == "reportBattery" && n.Type == graph.NodeTypeFunction
	})
	require.NotNil(t, root)

	r := Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)

	var hitChain *Chain
	for i, c := range r.Chains {
		if strings.Contains(c.Text, "-[ws_send]->") {
			hitChain = &r.Chains[i]
		}
	}
	require.NotNil(t, hitChain, "expected a chain through the typed ws_send link, got:\n%s", chainTexts(r))
	assert.True(t, strings.HasPrefix(hitChain.Text, "(tether) reportBattery -[calls]->"), hitChain.Text)

	// The message type is carried as the edge label on the ws_send hop.
	last := hitChain.Hops[len(hitChain.Hops)-1]
	assert.Equal(t, "ws_send", last.EdgeType)
	assert.Equal(t, "battery", last.EdgeLabel)
}

func chainTexts(r *Result) string {
	var b strings.Builder
	for _, c := range r.Chains {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}
