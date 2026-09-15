package pipeline_test

// FX.8.11 (2026-09-15): pusher_producer — internal/linker/pusher_producer.go's
// retired EnrichPusherProducers, migrated onto patterns/ruby/
// pusher_producer.yaml + rules/ruby/pusher_producer.dl, driven by the
// pusher_producer_sites hub provider (internal/factpipe/hub_pusher_producer.go).
// Real files on disk, same convention as pusher_consumer_test.go (reuses its
// pcWriteFile/pcActive helpers, same package) — this package doesn't run the
// base (non-factpipe) parser, so the canonical PusherClient trigger publisher
// node and the enclosing method nodes the .dl joins against are hand-built
// here, mirroring the retired internal/linker/pusher_producer_test.go's own
// fixtures verbatim.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

const ppWrapperRb = `# frozen_string_literal: true
class PusherClient
  CHANNELS = { lro_update: "lro_update" }.freeze
  FOLDER_REFRESH = "folder_refresh"
  LRO_DETAILS = "lro_details"

  def initialize(object, channel, explicit_channel: false)
    @channel_name = explicit_channel ? channel : "#{Rails.env}.#{channel}.#{object.id}"
  end

  def notify_folder_refresh(folder_id)
    push({ folder_id: folder_id }, FOLDER_REFRESH)
  end

  def notify_lro_details(ids)
    push(ids.to_json, LRO_DETAILS)
  end

  def push(body, event_type)
    PusherClient.new_pusher_client.trigger(channel_name, event_type, body)
  end
end
`

func TestPusherProducerRule_ResolvesChannelAndEvent(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", ppWrapperRb)
	callerFile := pcWriteFile(t, dir, "execution_history.rb", `# frozen_string_literal: true
class ExecutionHistory
  def refresh(study)
    PusherClient.new(study, "folder-status").notify_folder_refresh(study.root_folder_id)
  end

  def lro(lro_detail)
    pusher = PusherClient.new(self, PusherClient::CHANNELS[:lro_update], explicit_channel: true)
    pusher.notify_lro_details(lro_detail.ids)
  end

  def dynamic(obj, chan)
    PusherClient.new(obj, chan).notify_folder_refresh(obj.id)
  end
end
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	refresh := graph.Node{
		ID: "svc:execution_history.rb:method:refresh:3", Type: graph.NodeTypeMethod,
		File: callerFile, Service: "svc", Line: 3, EndLine: 5,
	}
	lro := graph.Node{
		ID: "svc:execution_history.rb:method:lro:7", Type: graph.NodeTypeMethod,
		File: callerFile, Service: "svc", Line: 7, EndLine: 10,
	}
	dynamic := graph.Node{
		ID: "svc:execution_history.rb:method:dynamic:12", Type: graph.NodeTypeMethod,
		File: callerFile, Service: "svc", Line: 12, EndLine: 14,
	}

	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper, refresh, lro, dynamic},
		Files: []string{wrapperFile, callerFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[string]string{} // channel -> event
	var pubCount int
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypePublisher {
			continue
		}
		pubCount++
		if n.Meta["pattern"] != "pusher_trigger_forward" {
			t.Errorf("pattern = %q, want pusher_trigger_forward", n.Meta["pattern"])
		}
		got[n.Meta["channel"]] = n.Meta["event"]
	}
	if pubCount != 2 {
		t.Fatalf("got %d publisher nodes, want 2 (dynamic `chan` arg must stay unresolved): %+v", pubCount, res.Nodes)
	}
	if got["folder-status"] != "folder_refresh" {
		t.Errorf("literal channel + notify_folder_refresh event: got %+v", got)
	}
	if got["lro_update"] != "lro_details" {
		t.Errorf("CHANNELS[:lro_update] via local var + notify_lro_details: got %+v", got)
	}
	if _, dynamicLeaked := got["chan"]; dynamicLeaked {
		t.Errorf("a bare-identifier channel argument must stay dynamic (no node): %+v", got)
	}

	var edgeFroms []string
	for _, e := range res.Edges {
		if e.Meta["via"] != "pusher_producer_forward" {
			continue
		}
		if e.Type != graph.EdgeTypeCalls {
			t.Errorf("want EdgeTypeCalls, got %q", e.Type)
		}
		edgeFroms = append(edgeFroms, e.From)
	}
	if !contains(edgeFroms, refresh.ID) {
		t.Errorf("missing edge from %q; edges: %+v", refresh.ID, res.Edges)
	}
	if !contains(edgeFroms, lro.ID) {
		t.Errorf("missing edge from %q; edges: %+v", lro.ID, res.Edges)
	}
}

func TestPusherProducerRule_IvarHeldInstance(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", ppWrapperRb)
	callerFile := pcWriteFile(t, dir, "folder_copier.rb", `# frozen_string_literal: true
class FolderCopier
  def initialize(destination)
    @pusher = PusherClient.new(destination, "folder-status")
  end

  attr_reader :pusher

  def run
    notify_progress
    @pusher.notify_folder_refresh(1)
  end

  def notify_progress
    pusher.notify_lro_details([1, 2])
  end
end
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	run := graph.Node{
		ID: "svc:folder_copier.rb:method:run:9", Type: graph.NodeTypeMethod,
		File: callerFile, Service: "svc", Line: 9, EndLine: 12,
	}
	notifyProgress := graph.Node{
		ID: "svc:folder_copier.rb:method:notify_progress:14", Type: graph.NodeTypeMethod,
		File: callerFile, Service: "svc", Line: 14, EndLine: 16,
	}

	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper, run, notifyProgress},
		Files: []string{wrapperFile, callerFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[string]string{}
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypePublisher {
			continue
		}
		if n.Meta["channel"] != "folder-status" {
			t.Errorf("channel = %q, want folder-status (the ivar's .new channel arg)", n.Meta["channel"])
		}
		got[n.Meta["event"]] = n.ID
	}
	if _, ok := got["folder_refresh"]; !ok {
		t.Errorf("@pusher.notify_folder_refresh not reached: %+v", got)
	}
	if _, ok := got["lro_details"]; !ok {
		t.Errorf("pusher.notify_lro_details (attr_reader) not reached: %+v", got)
	}
}

func TestPusherProducerRule_MixinHolder(t *testing.T) {
	dir := t.TempDir()
	wrapperFile := pcWriteFile(t, dir, "pusher_client.rb", ppWrapperRb)
	holderFile := pcWriteFile(t, dir, "task_importer.rb", `# frozen_string_literal: true
class TaskImporter
  include TaskImporterAnalyser

  def initialize(study)
    @pusher = PusherClient.new(study, "import-status")
  end

  attr_reader :pusher
end
`)
	modFile := pcWriteFile(t, dir, "task_importer_analyser.rb", `# frozen_string_literal: true
module TaskImporterAnalyser
  def notify_user
    @pusher.notify_folder_refresh(1)
  end

  def notify_details
    pusher.notify_lro_details([1, 2])
  end
end
`)

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: wrapperFile, Service: "svc",
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	notifyUser := graph.Node{
		ID: "svc:task_importer_analyser.rb:method:notify_user:3", Type: graph.NodeTypeMethod,
		File: modFile, Service: "svc", Line: 3, EndLine: 5,
	}
	notifyDetails := graph.Node{
		ID: "svc:task_importer_analyser.rb:method:notify_details:7", Type: graph.NodeTypeMethod,
		File: modFile, Service: "svc", Line: 7, EndLine: 9,
	}

	snap := graph.Snapshot{
		Nodes: []graph.Node{wrapper, notifyUser, notifyDetails},
		Files: []string{wrapperFile, holderFile, modFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[string]string{}
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypePublisher {
			continue
		}
		if n.Meta["channel"] != "import-status" {
			t.Errorf("channel = %q, want import-status (carried from the holder class)", n.Meta["channel"])
		}
		got[n.Meta["event"]] = n.ID
	}
	if _, ok := got["folder_refresh"]; !ok {
		t.Errorf("@pusher.notify_folder_refresh in the module body not reached: %+v", got)
	}
	if _, ok := got["lro_details"]; !ok {
		t.Errorf("pusher.notify_lro_details (attr_reader bare name) in the module body not reached: %+v", got)
	}

	var froms []string
	for _, e := range res.Edges {
		if e.Meta["via"] == "pusher_producer_forward" {
			froms = append(froms, e.From)
		}
	}
	if !contains(froms, notifyUser.ID) {
		t.Errorf("missing edge from %q; edges: %+v", notifyUser.ID, res.Edges)
	}
}

func TestPusherProducerRule_NoWrapperNoWork(t *testing.T) {
	dir := t.TempDir()
	callerFile := pcWriteFile(t, dir, "x.rb", `class ExecutionHistory
  def refresh(study)
    PusherClient.new(study, "folder-status").notify_folder_refresh(study.root_folder_id)
  end
end
`)
	snap := graph.Snapshot{
		Nodes: []graph.Node{{
			ID: "svc:x.rb:method:refresh:2", Type: graph.NodeTypeMethod,
			File: callerFile, Service: "svc", Line: 2, EndLine: 4,
		}},
		Files: []string{callerFile},
	}
	res, err := pipeline.Run(pcActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypePublisher {
			t.Errorf("no pusher_trigger wrapper exists; must not mint a publisher: %+v", n)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
