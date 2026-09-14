package pipeline_test

// FX.8 (2026-09-14): pusher_consumer — the ERB half of
// internal/linker/pusher_consumer.go's retired EnrichPusherConsumers,
// migrated onto patterns/ruby/pusher_consumer.yaml + rules/ruby/
// pusher_consumer.dl, driven by the pusher_wrapper_erb hub provider
// (internal/factpipe/hub_pusher_consumer.go — the Tier FX cross-framework
// fact-sharing mechanism, internal/factpipe/hub.go).
//
// Real files on disk (hub providers do their own os.ReadFile, like
// config_value / table_facts already do), mirroring pusher_helper_calls_test
// .go's convention: this package doesn't run the base (non-factpipe) parser,
// so the canonical PusherClient trigger publisher node and the ERB/JS file
// nodes the .dl joins against are hand-built here the same way that test
// hand-builds its wrapper node.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func pcWriteFile(t *testing.T, dir, rel, src string) string {
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

func pcActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg.Active([]deps.Dependency{{Name: "pusher", Version: "2.0.0"}})
}

const pcWrapperRb = `class PusherClient
  CHANNELS = { lro_update: "lro-update" }.freeze
  LRO_DETAILS = "lro_details"

  def initialize(object, channel); end

  def notify_lro(id)
    push({ id: id }, LRO_DETAILS)
  end

  def push(body, event_type)
    PusherClient.new_pusher_client.trigger(channel_name, event_type, body)
  end
end
`

func TestPusherConsumerRule_MintsSubscriberFromPusherConfigHelper(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", pcWrapperRb)
	erbFile := pcWriteFile(t, dir, "views/thing.html.erb", `<%= react_component("Thing", {
  pusherConfig: pusher_config(
    channel: PusherClient::CHANNELS[:lro_update],
    event:   PusherClient::LRO_DETAILS,
  ),
}) %>
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	fileNode := graph.Node{
		ID: "svc:" + erbFile + ":file", Type: graph.NodeTypeFile,
		File: erbFile, Service: "svc",
	}

	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper, fileNode},
		Files: []string{wrapperFile, erbFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sub *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeSubscriber {
			sub = &res.Nodes[i]
		}
	}
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}
	if sub.Meta["channel"] != "lro-update" {
		t.Errorf("channel = %q, want lro-update (meta: %+v)", sub.Meta["channel"], sub.Meta)
	}
	if sub.Meta["event"] != "lro_details" {
		t.Errorf("event = %q, want lro_details (meta: %+v)", sub.Meta["event"], sub.Meta)
	}
	if sub.Meta["pattern"] != "pusher_subscribe_erb" {
		t.Errorf("pattern = %q, want pusher_subscribe_erb", sub.Meta["pattern"])
	}
	if sub.Label != "lro-update lro_details" {
		t.Errorf("label = %q, want %q", sub.Label, "lro-update lro_details")
	}

	var gotContains bool
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeContains && e.From == fileNode.ID && e.To == sub.ID {
			gotContains = true
		}
	}
	if !gotContains {
		t.Errorf("missing contains edge %s -> %s; edges: %+v", fileNode.ID, sub.ID, res.Edges)
	}
}

func TestPusherConsumerRule_RenderSharedPusherChannelOnly(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", pcWrapperRb)
	erbFile := pcWriteFile(t, dir, "views/study.html.erb", `<%= render "shared/pusher", pusher_channel: "#{Rails.env}.folder-status.#{@study.id}" %>
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper},
		Files: []string{wrapperFile, erbFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sub *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeSubscriber {
			sub = &res.Nodes[i]
		}
	}
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}
	if sub.Meta["channel"] != "folder-status" {
		t.Errorf("channel = %q, want folder-status (meta: %+v)", sub.Meta["channel"], sub.Meta)
	}
	if _, hasEvent := sub.Meta["event"]; hasEvent {
		t.Errorf("event should be absent for the channel-only shared/pusher partial: %+v", sub.Meta)
	}
	if sub.Meta["pattern"] != "pusher_subscribe_erb_channel" {
		t.Errorf("pattern = %q, want pusher_subscribe_erb_channel", sub.Meta["pattern"])
	}
}

func TestPusherConsumerRule_BridgesToPusherConnectionSingleton(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", pcWrapperRb)
	erbFile := pcWriteFile(t, dir, "views/thing.html.erb", `<%= pusher_config(channel: PusherClient::CHANNELS[:lro_update]) %>
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	singleton := graph.Node{
		ID:   "svc:common/PusherConnection/PusherConnection.jsx:function:subscribe:12",
		Type: graph.NodeTypeFunction,
		File: "common/PusherConnection/PusherConnection.jsx", Service: "svc",
	}
	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper, singleton},
		Files: []string{wrapperFile, erbFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sub *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeSubscriber {
			sub = &res.Nodes[i]
		}
	}
	if sub == nil {
		t.Fatalf("no subscriber node minted; nodes: %+v", res.Nodes)
	}

	var bridged bool
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeCalls && e.From == sub.ID && e.To == singleton.ID {
			if e.Meta["via"] != "pusher_subscribe_singleton" {
				t.Errorf("unexpected meta: %+v", e.Meta)
			}
			bridged = true
		}
	}
	if !bridged {
		t.Errorf("missing bridge edge %s -> %s; edges: %+v", sub.ID, singleton.ID, res.Edges)
	}
}

func TestPusherConsumerRule_NoWrapperNoSubscribers(t *testing.T) {
	dir := t.TempDir()
	erbFile := pcWriteFile(t, dir, "views/thing.html.erb", `<%= pusher_config(channel: PusherClient::CHANNELS[:lro_update]) %>
`)
	snap := graph.Snapshot{Files: []string{erbFile}}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeSubscriber {
			t.Errorf("no pusher_trigger publisher exists; must not mint a subscriber: %+v", n)
		}
	}
}
