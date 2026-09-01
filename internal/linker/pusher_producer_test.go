package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

// A trimmed PusherClient wrapper + three call sites: chained literal channel,
// chained `Class::CONST` channel, and a local-var `CHANNELS[:x]` channel.
const pusherWrapperFixture = `# frozen_string_literal: true
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

const pusherCallerFixture = `# frozen_string_literal: true
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
`

// An ivar-held instance assigned in one method, invoked from others via an
// attr_reader (bare name) and directly (@ivar).
const pusherIvarFixture = `# frozen_string_literal: true
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
`

// PU.2d — the holder class sets @pusher and `include`s a module; the actual
// notify calls live in that module (a separate file).
const pusherHolderFixture = `# frozen_string_literal: true
class TaskImporter
  include TaskImporterAnalyser

  def initialize(study)
    @pusher = PusherClient.new(study, "import-status")
  end

  attr_reader :pusher
end
`

const pusherMixinModuleFixture = `# frozen_string_literal: true
module TaskImporterAnalyser
  def notify_user
    @pusher.notify_folder_refresh(1)
  end

  def notify_details
    pusher.notify_lro_details([1, 2])
  end
end
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestEnrichPusherProducers_ResolvesChannelAndEvent(t *testing.T) {
	dir := t.TempDir()
	wrapper := writeFile(t, dir, "pusher_client.rb", pusherWrapperFixture)
	caller := writeFile(t, dir, "execution_history.rb", pusherCallerFixture)

	// The wrapper's pattern node (as PU.1 would emit it) plus the enclosing
	// methods of the caller, so the calls edge has somewhere to anchor.
	nodes := []graph.Node{
		{ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
			File: wrapper, Line: 20, Meta: map[string]string{"pattern": "pusher_trigger", "key_dynamic": "true"}},
		{ID: "svc:execution_history.rb:method:refresh:3", Type: graph.NodeTypeMethod,
			File: caller, Line: 3, EndLine: 5},
		{ID: "svc:execution_history.rb:method:lro:7", Type: graph.NodeTypeMethod,
			File: caller, Line: 7, EndLine: 10},
		{ID: "svc:execution_history.rb:method:dynamic:12", Type: graph.NodeTypeMethod,
			File: caller, Line: 12, EndLine: 14},
	}
	serviceFiles := map[string][]string{"svc": {wrapper, caller}}

	newNodes, newEdges := linker.EnrichPusherProducers(nodes, serviceFiles)

	got := map[string]string{} // channel -> event
	for _, n := range newNodes {
		assert.Equal(t, graph.NodeTypePublisher, n.Type)
		assert.Equal(t, "pusher_trigger_forward", n.Meta["pattern"])
		assert.Empty(t, n.Meta["key_dynamic"])
		got[n.Meta["channel"]] = n.Meta["event"]
	}

	assert.Equal(t, "folder_refresh", got["folder-status"], "literal channel + notify_folder_refresh event")
	assert.Equal(t, "lro_details", got["lro_update"], "CHANNELS[:lro_update] via local var + notify_lro_details")
	assert.NotContains(t, got, "chan", "a bare-identifier channel argument must stay dynamic (no node)")
	require.Len(t, newNodes, 2)

	var edgeTargets []string
	for _, e := range newEdges {
		assert.Equal(t, graph.EdgeTypeCalls, e.Type)
		edgeTargets = append(edgeTargets, e.From)
	}
	assert.Contains(t, edgeTargets, "svc:execution_history.rb:method:refresh:3")
	assert.Contains(t, edgeTargets, "svc:execution_history.rb:method:lro:7")
}

func TestEnrichPusherProducers_IvarHeldInstance(t *testing.T) {
	dir := t.TempDir()
	wrapper := writeFile(t, dir, "pusher_client.rb", pusherWrapperFixture)
	caller := writeFile(t, dir, "folder_copier.rb", pusherIvarFixture)

	nodes := []graph.Node{
		{ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
			File: wrapper, Line: 20, Meta: map[string]string{"pattern": "pusher_trigger", "key_dynamic": "true"}},
		{ID: "svc:folder_copier.rb:method:run:9", Type: graph.NodeTypeMethod, File: caller, Line: 9, EndLine: 12},
		{ID: "svc:folder_copier.rb:method:notify_progress:14", Type: graph.NodeTypeMethod, File: caller, Line: 14, EndLine: 16},
	}

	newNodes, _ := linker.EnrichPusherProducers(nodes, map[string][]string{"svc": {wrapper, caller}})

	got := map[string]string{}
	for _, n := range newNodes {
		assert.Equal(t, "folder-status", n.Meta["channel"], "the ivar's .new channel arg")
		got[n.Meta["event"]] = n.ID
	}
	assert.Contains(t, got, "folder_refresh", "@pusher.notify_folder_refresh reached")
	assert.Contains(t, got, "lro_details", "pusher.notify_lro_details (attr_reader) reached")
}

func TestEnrichPusherProducers_MixinHolder(t *testing.T) {
	dir := t.TempDir()
	wrapper := writeFile(t, dir, "pusher_client.rb", pusherWrapperFixture)
	holder := writeFile(t, dir, "task_importer.rb", pusherHolderFixture)
	mod := writeFile(t, dir, "task_importer_analyser.rb", pusherMixinModuleFixture)

	nodes := []graph.Node{
		{ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
			File: wrapper, Line: 20, Meta: map[string]string{"pattern": "pusher_trigger", "key_dynamic": "true"}},
		{ID: "svc:task_importer_analyser.rb:method:notify_user:3", Type: graph.NodeTypeMethod, File: mod, Line: 3, EndLine: 5},
		{ID: "svc:task_importer_analyser.rb:method:notify_details:7", Type: graph.NodeTypeMethod, File: mod, Line: 7, EndLine: 9},
	}

	newNodes, newEdges := linker.EnrichPusherProducers(nodes, map[string][]string{"svc": {wrapper, holder, mod}})

	got := map[string]string{}
	for _, n := range newNodes {
		assert.Equal(t, "import-status", n.Meta["channel"], "channel carried from the holder class")
		assert.Equal(t, "pusher_trigger_forward", n.Meta["pattern"])
		got[n.Meta["event"]] = n.ID
	}
	assert.Contains(t, got, "folder_refresh", "@pusher.notify_folder_refresh in the module body")
	assert.Contains(t, got, "lro_details", "pusher.notify_lro_details (attr_reader bare name) in the module body")

	var froms []string
	for _, e := range newEdges {
		froms = append(froms, e.From)
	}
	assert.Contains(t, froms, "svc:task_importer_analyser.rb:method:notify_user:3")
}

func TestEnrichPusherProducers_NoHubNoWork(t *testing.T) {
	dir := t.TempDir()
	caller := writeFile(t, dir, "x.rb", pusherCallerFixture)
	nodes := []graph.Node{{ID: "svc:x.rb:method:refresh:3", Type: graph.NodeTypeMethod, File: caller, Line: 3, EndLine: 5}}
	newNodes, newEdges := linker.EnrichPusherProducers(nodes, map[string][]string{"svc": {caller}})
	assert.Empty(t, newNodes)
	assert.Empty(t, newEdges)
}
