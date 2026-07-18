package e2e_test

// Phase 12: end-to-end cross-stack chains through the real indexing pipeline
// (indexer.Run → SQLite → trace chain output). Three confirmed real flows:
//
//  1. templ data-on-click → Datastar action → Gin handler → hub.Broadcast()
//     → per-connection SSE subscriber (templ + Go).
//  2. Rails controller → bunny publish → RabbitMQ exchange (workspace hint)
//     → Go amqp091 consumer (Ruby + Go, cross-repo).
//  3. JS WebSocket typed message → Go gorilla read pump dispatch-by-type →
//     typed response write → client onmessage dispatch (JS + Go, both ways).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/trace"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// indexChains runs the full production pipeline over the chains workspace
// and returns the adjacency index.
func indexChains(t *testing.T) *graph.AdjacencyIndex {
	t.Helper()

	cfg := &workspace.WorkspaceConfig{
		Name:    "chains",
		Version: "1",
		Services: []workspace.Service{
			{Name: "ui", Path: "testdata/chains/ui", Language: "go"},
			{Name: "hub", Path: "testdata/chains/hub", Language: "go"},
			{Name: "rails", Path: "testdata/chains/rails", Language: "ruby"},
			{Name: "agent", Path: "testdata/chains/agent", Language: "go"},
			{Name: "tether-client", Path: "testdata/chains/tether-client", Language: "javascript"},
			{Name: "tether-server", Path: "testdata/chains/tether-server", Language: "go"},
		},
		Links: []workspace.Link{
			{From: "rails", To: "agent", Via: "rabbitmq", Exchange: "dsw.builds"},
		},
	}

	dbDir := t.TempDir()
	_, err := indexer.Run(context.Background(), indexer.Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
	})
	require.NoError(t, err)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	return idx
}

// findChainNode locates a node by service + predicate.
func findChainNode(idx *graph.AdjacencyIndex, service string, pred func(n *graph.Node) bool) *graph.Node {
	for _, n := range idx.Nodes {
		if n.Service == service && pred(n) {
			return n
		}
	}
	return nil
}

func chainWith(r *trace.Result, substrs ...string) (string, bool) {
	for _, c := range r.Chains {
		ok := true
		for _, s := range substrs {
			if !strings.Contains(c.Text, s) {
				ok = false
				break
			}
		}
		if ok {
			return c.Text, true
		}
	}
	return allChains(r), false
}

func allChains(r *trace.Result) string {
	var b strings.Builder
	for _, c := range r.Chains {
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// Chain 1: templ button → Datastar action → Gin handler → hub.Broadcast →
// SSE subscriber. ≥5 hops spanning the templ UI and the Go service.
func TestChain_TemplDatastarGinHubSSE(t *testing.T) {
	idx := indexChains(t)

	// Note: data-text bindings also produce pseudo-component nodes
	// ($currentTurn), so pin the root to the declared template component.
	root := findChainNode(idx, "ui", func(n *graph.Node) bool {
		return n.Type == graph.NodeTypeComponent && n.Label == "GamePage"
	})
	require.NotNil(t, root, "templ component node must exist in ui service")

	r := trace.Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)

	text, ok := chainWith(r,
		"-[datastar_action]->",
		"-[http_call]-> ‖hub‖",
		"-[calls]-> handleMove",
		"-[hub_broadcast]-> Subscribe",
	)
	require.True(t, ok, "expected templ→gin→hub→SSE chain, got:\n%s", text)

	// ≥4 hops: count nodes in the matched chain.
	for _, c := range r.Chains {
		if c.Text == text {
			assert.GreaterOrEqual(t, len(c.Hops), 5, "chain should span at least 5 nodes: %s", text)
		}
	}
	t.Logf("chain 1: %s", text)
}

// Chain 2: Rails controller action → bunny publish (exchange in a variable,
// connected by the workspace broker hint) → RabbitMQ channel → Go amqp091
// consumer, cross-language and cross-repo.
func TestChain_RailsBunnyRabbitGoConsumer(t *testing.T) {
	idx := indexChains(t)

	root := findChainNode(idx, "rails", func(n *graph.Node) bool {
		return n.Type == graph.NodeTypeMethod && n.Label == "create"
	})
	require.NotNil(t, root, "rails controller action node must exist")

	r := trace.Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)

	text, ok := chainWith(r,
		"-[publishes]-> dsw.builds",
		"-[subscribes]-> ‖agent‖",
	)
	require.True(t, ok, "expected rails→rabbitmq→agent chain, got:\n%s", text)
	assert.True(t, strings.HasPrefix(text, "(rails) create"), text)
	t.Logf("chain 2: %s", text)
}

// Chain 3: JS typed WebSocket message → Go gorilla dispatch-by-type, and the
// typed response write → client onmessage dispatch case (both directions of
// the tether shape).
func TestChain_WebSocketTypedRoundTrip(t *testing.T) {
	idx := indexChains(t)

	// Client → server: reportBattery sends {type:'battery'}, the Go read
	// pump's switch dispatches on it.
	root := findChainNode(idx, "tether-client", func(n *graph.Node) bool {
		return n.Type == graph.NodeTypeFunction && n.Label == "reportBattery"
	})
	require.NotNil(t, root, "reportBattery function node must exist")

	r := trace.Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)
	text, ok := chainWith(r, "-[ws_send]-> ‖tether-server‖")
	require.True(t, ok, "expected client→server typed ws chain, got:\n%s", text)
	t.Logf("chain 3 (client→server): %s", text)

	// Server → client: the Go handler's WriteJSON(Ack{Type:"battery_ack"})
	// reaches the client's onmessage 'battery_ack' case.
	root = findChainNode(idx, "tether-server", func(n *graph.Node) bool {
		return n.Type == graph.NodeTypeFunction && n.Label == "readPump"
	})
	require.NotNil(t, root, "readPump function node must exist")

	r = trace.Run(idx, root.ID, "forward", 0, false)
	require.NotNil(t, r)
	text, ok = chainWith(r, "-[ws_send]-> ‖tether-client‖")
	require.True(t, ok, "expected server→client typed ack chain, got:\n%s", text)
	t.Logf("chain 3 (server→client): %s", text)
}

// The chains workspace collectively spans ≥3 languages with cross-service
// links between them.
func TestChains_SpanThreeLanguages(t *testing.T) {
	idx := indexChains(t)

	langs := map[string]bool{}
	for _, n := range idx.Nodes {
		if n.Language != "" {
			langs[n.Language] = true
		}
	}
	for _, want := range []string{"go", "ruby", "javascript"} {
		assert.True(t, langs[want], "workspace must contain %s nodes (got %v)", want, langs)
	}
}
