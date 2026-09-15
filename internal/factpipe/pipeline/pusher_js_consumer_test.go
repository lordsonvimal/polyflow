package pipeline_test

// FX.8 (2026-09-15): pusher_js_consumer — the JS-dataflow half of
// internal/linker/pusher_js_consumer.go's retired EnrichPusherConsumersJS
// (SPA.7), migrated onto patterns/javascript/pusher_js_consumer.yaml + rules/
// javascript/pusher_js_consumer.dl, driven by two hub providers:
// pusher_js_subscribe_sites (internal/factpipe/hub_pusher_js_consumer.go, the
// JS `new Pusher(...)` / `.subscribe()` / `.bind()` dataflow tracking) and
// pusher_wrapper_erb (internal/factpipe/hub_pusher_consumer.go, extended
// 2026-09-15 to also resolve `react_component(..., pusherConfig: {...})` ERB
// prop sites).
//
// Real files on disk (hub providers do their own os.ReadFile), mirroring
// pusher_consumer_test.go's convention: the canonical PusherClient trigger
// publisher node is hand-built here, the same way that test hand-builds it.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func pjWriteFile(t *testing.T, dir, rel, src string) string {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
	return abs
}

func pjActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg.Active([]deps.Dependency{{Name: "pusher-js", Version: "8.0.0"}})
}

func pjSubscriber(res pipeline.Result) *graph.Node {
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeSubscriber && res.Nodes[i].Meta["pattern"] == "pusher_subscribe_js" {
			return &res.Nodes[i]
		}
	}
	return nil
}

func pjPublisherNode(file string) graph.Node {
	return graph.Node{
		ID: "svc:" + file + ":publisher:pusher_trigger:28", Type: graph.NodeTypePublisher,
		File: file, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger", "event": "update"},
	}
}

func TestPusherJSConsumerRule_LiteralSubscribeBridges(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pjWriteFile(t, dir, "pusher_client.rb", "class PusherClient\nend\n")
	jsFile := pjWriteFile(t, dir, "orders.js", `
const p = new Pusher("key");
const channel = p.subscribe("app.orders");
channel.bind("update", cb);
`)
	fileNode := graph.Node{ID: "svc:" + jsFile + ":file", Type: graph.NodeTypeFile, File: jsFile, Service: "svc"}
	pub := pjPublisherNode(wrapperFile)

	snap := graph.Snapshot{
		Nodes: []graph.Node{pub, fileNode},
		Files: []string{wrapperFile, jsFile},
	}
	res, err := pipeline.Run(pjActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sub := pjSubscriber(res)
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}
	if sub.Meta["channel"] != "app.orders" {
		t.Errorf("channel = %q, want app.orders (meta: %+v)", sub.Meta["channel"], sub.Meta)
	}
	if sub.Meta["event"] != "update" {
		t.Errorf("event = %q, want update (meta: %+v)", sub.Meta["event"], sub.Meta)
	}
	if sub.Meta["key_dynamic"] != "" {
		t.Errorf("key_dynamic should be unset for a literal channel: %+v", sub.Meta)
	}

	var bridged, contained bool
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes && e.To == sub.ID {
			bridged = true
			if e.Meta["via"] != "pusher_channel_event" {
				t.Errorf("via = %q, want pusher_channel_event", e.Meta["via"])
			}
		}
		if e.Type == graph.EdgeTypeContains && e.From == fileNode.ID && e.To == sub.ID {
			contained = true
		}
	}
	if !bridged {
		t.Errorf("missing publishes bridge to %s; edges: %+v", sub.ID, res.Edges)
	}
	if !contained {
		t.Errorf("missing contains edge %s -> %s; edges: %+v", fileNode.ID, sub.ID, res.Edges)
	}
	for _, u := range res.Unresolved {
		if u.Kind == "pusher_channel_dynamic" {
			t.Errorf("a resolved channel must not be ledgered: %+v", u)
		}
	}
	for _, u := range res.Ledger {
		if u.Kind == "pusher_channel_dynamic" {
			t.Errorf("a resolved channel must not be ledgered: %+v", u)
		}
	}
}

func TestPusherJSConsumerRule_PropConfigResolvedFromERB(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pjWriteFile(t, dir, "pusher_client.rb", "class PusherClient\nend\n")
	erbFile := pjWriteFile(t, dir, "views/index.html.erb",
		`<%= react_component("Foo", { pusherConfig: { channel: "envx.folder.1", event: "update" } }) %>`)
	jsFile := pjWriteFile(t, dir, "Foo.jsx", `
class Foo extends React.Component {
  setup() {
    const pusher = new window.Pusher(this.props.pusherConfig.key);
    this.chan = pusher.subscribe(this.props.pusherConfig.channel);
    this.chan.bind("update", this.onUpdate);
  }
}
`)
	pub := pjPublisherNode(wrapperFile)

	snap := graph.Snapshot{
		Nodes: []graph.Node{pub},
		Files: []string{wrapperFile, erbFile, jsFile},
	}
	res, err := pipeline.Run(pjActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sub := pjSubscriber(res)
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}
	if sub.Meta["channel"] != "envx.folder.1" {
		t.Errorf("channel = %q, want envx.folder.1 (meta: %+v)", sub.Meta["channel"], sub.Meta)
	}
	if sub.Meta["pusher_config"] != "Foo" {
		t.Errorf("pusher_config = %q, want Foo (meta: %+v)", sub.Meta["pusher_config"], sub.Meta)
	}
	if sub.Meta["key_dynamic"] != "" {
		t.Errorf("an ERB-resolved channel must not stay dynamic: %+v", sub.Meta)
	}
	for _, u := range append(append([]graph.UnresolvedRef{}, res.Unresolved...), res.Ledger...) {
		if u.Kind == "pusher_channel_dynamic" {
			t.Errorf("an ERB-resolved channel is not a blind spot: %+v", u)
		}
	}
}

func TestPusherJSConsumerRule_DynamicChannelMintsAndLedgers(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pjWriteFile(t, dir, "pusher_client.rb", "class PusherClient\nend\n")
	jsFile := pjWriteFile(t, dir, "flag.js", `
const pusher = window.pusher;
let sub = pusher.subscribe(someChannelVar);
sub.bind("update", handleUpdate);
`)
	pub := pjPublisherNode(wrapperFile)

	snap := graph.Snapshot{
		Nodes: []graph.Node{pub},
		Files: []string{wrapperFile, jsFile},
	}
	res, err := pipeline.Run(pjActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sub := pjSubscriber(res)
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}
	if sub.Meta["key_dynamic"] != "true" {
		t.Errorf("key_dynamic = %q, want true (meta: %+v)", sub.Meta["key_dynamic"], sub.Meta)
	}
	if sub.Meta["key_dynamic_raw"] != "someChannelVar" {
		t.Errorf("key_dynamic_raw = %q, want someChannelVar (meta: %+v)", sub.Meta["key_dynamic_raw"], sub.Meta)
	}

	var ledgered bool
	for _, u := range append(append([]graph.UnresolvedRef{}, res.Unresolved...), res.Ledger...) {
		if u.Kind == "pusher_channel_dynamic" && u.Name == "someChannelVar" {
			ledgered = true
		}
	}
	if !ledgered {
		t.Errorf("missing pusher_channel_dynamic ledger row for someChannelVar; unresolved: %+v, ledger: %+v",
			res.Unresolved, res.Ledger)
	}
}

func TestPusherJSConsumerRule_NotAPusherInstanceNoOp(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pjWriteFile(t, dir, "pusher_client.rb", "class PusherClient\nend\n")
	jsFile := pjWriteFile(t, dir, "store.js", `
const store = makeStore();
const sub = store.subscribe("x");
sub.bind("update", cb);
`)
	pub := pjPublisherNode(wrapperFile)

	snap := graph.Snapshot{
		Nodes: []graph.Node{pub},
		Files: []string{wrapperFile, jsFile},
	}
	res, err := pipeline.Run(pjActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sub := pjSubscriber(res); sub != nil {
		t.Errorf("a non-Pusher .subscribe() must not mint a subscriber: %+v", sub)
	}
}

func TestPusherJSConsumerRule_NoPublisherNoOp(t *testing.T) {
	dir := t.TempDir()
	jsFile := pjWriteFile(t, dir, "orders.js", `const p = new Pusher("k"); p.subscribe("c").bind("update", cb);`)

	snap := graph.Snapshot{Files: []string{jsFile}}
	res, err := pipeline.Run(pjActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sub := pjSubscriber(res); sub != nil {
		t.Errorf("no pusher_trigger publisher exists; must not mint a subscriber: %+v", sub)
	}
}
