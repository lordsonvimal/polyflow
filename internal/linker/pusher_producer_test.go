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

func TestEnrichPusherProducers_NoHubNoWork(t *testing.T) {
	dir := t.TempDir()
	caller := writeFile(t, dir, "x.rb", pusherCallerFixture)
	nodes := []graph.Node{{ID: "svc:x.rb:method:refresh:3", Type: graph.NodeTypeMethod, File: caller, Line: 3, EndLine: 5}}
	newNodes, newEdges := linker.EnrichPusherProducers(nodes, map[string][]string{"svc": {caller}})
	assert.Empty(t, newNodes)
	assert.Empty(t, newEdges)
}
