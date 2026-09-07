package linker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

// a minimal server-side trigger node so the gate fires.
func pusherHubNode(event string) graph.Node {
	return graph.Node{
		ID: "svc:lib/pusher_client.rb:publisher:pusher_trigger:28", Type: graph.NodeTypePublisher,
		Service: "svc", File: "lib/pusher_client.rb", Line: 28,
		Meta: map[string]string{"pattern": "pusher_trigger", "event": event, "key_dynamic": "true"},
	}
}

func jsSubscribers(nodes []graph.Node) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeSubscriber && n.Meta["pattern"] == "pusher_subscribe_js" {
			out = append(out, n)
		}
	}
	return out
}

func TestPusherJS_LiteralSubscribe(t *testing.T) {
	dir := t.TempDir()
	js := writeFile(t, dir, "orders.js", `
const p = new Pusher("key");
const channel = p.subscribe("app.orders");
channel.bind("update", cb);
`)
	nodes := []graph.Node{
		pusherHubNode("update"),
		{ID: "svc:" + js + ":file", Type: graph.NodeTypeFile, Service: "svc", File: js},
	}

	newNodes, newEdges, ledger := linker.EnrichPusherConsumersJS(nodes, map[string][]string{"svc": {js}})

	subs := jsSubscribers(newNodes)
	require.Len(t, subs, 1)
	assert.Equal(t, "app.orders", subs[0].Meta["channel"])
	assert.Equal(t, "update", subs[0].Meta["event"])
	assert.Empty(t, subs[0].Meta["key_dynamic"])
	assert.Empty(t, ledger)

	var bridged, contained bool
	for _, e := range newEdges {
		if e.Type == graph.EdgeTypePublishes && e.To == subs[0].ID {
			bridged = true
			assert.Equal(t, "pusher_channel_event", e.Meta["via"])
		}
		if e.Type == graph.EdgeTypeContains && e.To == subs[0].ID {
			contained = true
		}
	}
	assert.True(t, bridged, "event match bridges to the trigger publisher")
	assert.True(t, contained, "subscriber contained by its file node")
}

func TestPusherJS_PropConfigBridge(t *testing.T) {
	dir := t.TempDir()
	view := writeFile(t, dir, "index.html.erb",
		`<%= react_component("Foo", { pusherConfig: { channel: "envx.folder.1", event: "update" } }) %>`)
	js := writeFile(t, dir, "Foo.jsx", `
class Foo extends React.Component {
  setup() {
    const pusher = new window.Pusher(this.props.pusherConfig.key);
    this.chan = pusher.subscribe(this.props.pusherConfig.channel);
    this.chan.bind("update", this.onUpdate);
  }
}
`)
	nodes := []graph.Node{
		pusherHubNode("update"),
		{ID: "svc:" + js + ":file", Type: graph.NodeTypeFile, Service: "svc", File: js},
	}

	newNodes, _, ledger := linker.EnrichPusherConsumersJS(nodes, map[string][]string{"svc": {js, view}})

	subs := jsSubscribers(newNodes)
	require.Len(t, subs, 1)
	assert.Equal(t, "envx.folder.1", subs[0].Meta["channel"], "channel resolved from the ERB pusherConfig prop")
	assert.Equal(t, "Foo", subs[0].Meta["pusher_config"])
	assert.Empty(t, ledger, "a resolved channel is not a blind spot")
}

func TestPusherJS_DynamicChannel(t *testing.T) {
	dir := t.TempDir()
	js := writeFile(t, dir, "flag.js", `
const pusher = window.pusher;
let sub = pusher.subscribe(someChannelVar);
sub.bind("update", handleUpdate);
`)
	nodes := []graph.Node{
		pusherHubNode("update"),
		{ID: "svc:" + js + ":file", Type: graph.NodeTypeFile, Service: "svc", File: js},
	}

	newNodes, _, ledger := linker.EnrichPusherConsumersJS(nodes, map[string][]string{"svc": {js}})

	subs := jsSubscribers(newNodes)
	require.Len(t, subs, 1)
	assert.Equal(t, "true", subs[0].Meta["key_dynamic"])
	assert.Equal(t, "someChannelVar", subs[0].Meta["key_dynamic_raw"])
	require.Len(t, ledger, 1)
	assert.Equal(t, "pusher_channel_dynamic", ledger[0].Kind)
	assert.Equal(t, "someChannelVar", ledger[0].Name)
}

func TestPusherJS_NotAPusherInstanceNoOp(t *testing.T) {
	dir := t.TempDir()
	js := writeFile(t, dir, "store.js", `
const store = makeStore();
const sub = store.subscribe("x");
sub.bind("update", cb);
`)
	nodes := []graph.Node{pusherHubNode("update")}

	newNodes, newEdges, ledger := linker.EnrichPusherConsumersJS(nodes, map[string][]string{"svc": {js}})
	assert.Empty(t, jsSubscribers(newNodes))
	assert.Empty(t, newEdges)
	assert.Empty(t, ledger)
}

func TestPusherJS_NoHubNoOp(t *testing.T) {
	dir := t.TempDir()
	js := writeFile(t, dir, "orders.js", `const p = new Pusher("k"); p.subscribe("c").bind("update", cb);`)
	n, e, l := linker.EnrichPusherConsumersJS(nil, map[string][]string{"svc": {js}})
	assert.Empty(t, n)
	assert.Empty(t, e)
	assert.Empty(t, l)
}
