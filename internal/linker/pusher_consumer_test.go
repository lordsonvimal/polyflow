package linker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

const pusherERBFixture = `<h1>Batch</h1>
<%= react_component("SceBatchItemsContainer", {
  pusherConfig: pusher_config(
    channel: PusherClient::CHANNELS[:lro_update],
    event: PusherClient::LRO_DETAILS
  ),
  folderConfig: pusher_config(
    channel: "#{Rails.env}.folder-status.#{@study.id}",
    event: PusherClient::FOLDER_REFRESH
  ),
  dynamicConfig: pusher_config(
    channel: some_helper(@x),
    event: PusherClient::FOLDER_REFRESH
  )
}) %>
<%= render "shared/pusher", pusher_channel: "#{Rails.env}.job-logs.#{@item.id}" %>
`

func TestEnrichPusherConsumers_ResolvesChannelAndEvent(t *testing.T) {
	dir := t.TempDir()
	wrapper := writeFile(t, dir, "pusher_client.rb", pusherWrapperFixture)
	view := writeFile(t, dir, "index.html.erb", pusherERBFixture)

	nodes := []graph.Node{
		{ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
			File: wrapper, Line: 20, Meta: map[string]string{"pattern": "pusher_trigger", "key_dynamic": "true"}},
		{ID: "svc:index.html.erb:file", Type: graph.NodeTypeFile, Service: "svc", File: view},
	}
	serviceFiles := map[string][]string{"svc": {wrapper, view}}

	newNodes, newEdges := linker.EnrichPusherConsumers(nodes, serviceFiles)

	got := map[string]string{} // channel -> event
	patterns := map[string]string{}
	for _, n := range newNodes {
		assert.Equal(t, graph.NodeTypeSubscriber, n.Type)
		assert.Equal(t, "pusher-js", n.Meta["package"])
		got[n.Meta["channel"]] = n.Meta["event"]
		patterns[n.Meta["channel"]] = n.Meta["pattern"]
	}

	require.Len(t, newNodes, 3)
	assert.Equal(t, "lro_details", got["lro_update"], "CHANNELS[:lro_update] + LRO_DETAILS")
	assert.Equal(t, "folder_refresh", got["folder-status"], "template-literal segment + FOLDER_REFRESH")
	assert.Equal(t, "", got["job-logs"], "render partial path carries no event")
	assert.Equal(t, "pusher_subscribe_erb", patterns["folder-status"])
	assert.Equal(t, "pusher_subscribe_erb_channel", patterns["job-logs"])
	assert.NotContains(t, got, "", "a helper-call channel must resolve to nothing (no node)")

	var containsTo int
	for _, e := range newEdges {
		if e.Type == graph.EdgeTypeContains && e.From == "svc:index.html.erb:file" {
			containsTo++
		}
	}
	assert.Equal(t, 3, containsTo, "every ERB subscriber is contained by its file node")
}

func TestEnrichPusherConsumers_BridgesToJSSingleton(t *testing.T) {
	dir := t.TempDir()
	wrapper := writeFile(t, dir, "pusher_client.rb", pusherWrapperFixture)
	view := writeFile(t, dir, "index.html.erb", pusherERBFixture)

	singleton := "js:app/javascript/components/common/PusherConnection/PusherConnection.jsx:function:subscribe:170"
	nodes := []graph.Node{
		{ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
			File: wrapper, Line: 20, Meta: map[string]string{"pattern": "pusher_trigger", "key_dynamic": "true"}},
		{ID: "svc:index.html.erb:file", Type: graph.NodeTypeFile, Service: "svc", File: view},
		{ID: singleton, Type: graph.NodeTypeFunction, Line: 170,
			File: "app/javascript/components/common/PusherConnection/PusherConnection.jsx"},
		{ID: "js:.../PusherConnection.test.jsx:function:subscribe:9", Type: graph.NodeTypeFunction, Line: 9,
			File: "app/javascript/components/common/PusherConnection/PusherConnection.test.jsx"},
	}

	newNodes, newEdges := linker.EnrichPusherConsumers(nodes, map[string][]string{"svc": {wrapper, view}})

	bridged := map[string]bool{}
	for _, e := range newEdges {
		if e.Meta["via"] == "pusher_subscribe_singleton" {
			assert.Equal(t, singleton, e.To, "bridges to the real singleton, not the test file")
			assert.Equal(t, graph.EdgeTypeCalls, e.Type)
			bridged[e.From] = true
		}
	}
	assert.Len(t, bridged, len(newNodes), "every minted ERB subscriber gets a singleton bridge edge")
}

func TestEnrichPusherConsumers_NoHubNoWork(t *testing.T) {
	dir := t.TempDir()
	view := writeFile(t, dir, "index.html.erb", pusherERBFixture)
	nodes := []graph.Node{{ID: "svc:index.html.erb:file", Type: graph.NodeTypeFile, Service: "svc", File: "index.html.erb"}}
	newNodes, newEdges := linker.EnrichPusherConsumers(nodes, map[string][]string{"svc": {view}})
	assert.Empty(t, newNodes)
	assert.Empty(t, newEdges)
}
