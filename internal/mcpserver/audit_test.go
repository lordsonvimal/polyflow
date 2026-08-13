package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

func TestAuditTool_RecordsOkAndErrorCalls(t *testing.T) {
	store, idx := fixture()
	srv, handle := New(store, idx, "test", 0, true)

	o, err := ops.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { o.Close() })
	handle.SetOps(o)

	st, ct := mcp.NewInMemoryTransports()
	_, err = srv.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })

	// ok call
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search", Arguments: map[string]any{"query": "getUser"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	// error call: impact requires exactly one of target/file
	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "impact", Arguments: map[string]any{},
	})
	require.NoError(t, err) // MCP transport error is reported via res.IsError, not err
	list, lerr := o.ListCalls(context.Background(), ops.ListFilter{})
	require.NoError(t, lerr)
	require.Len(t, list.Calls, 2)

	// Newest first: impact (error), then search (ok).
	assert.Equal(t, "impact", list.Calls[0].Tool)
	assert.Equal(t, "mcp", list.Calls[0].Source)
	assert.Equal(t, "error", list.Calls[0].Status)
	assert.NotEmpty(t, list.Calls[0].Error)

	assert.Equal(t, "search", list.Calls[1].Tool)
	assert.Equal(t, "mcp", list.Calls[1].Source)
	assert.Equal(t, "ok", list.Calls[1].Status)
	assert.Contains(t, list.Calls[1].Params, "getUser")
	assert.NotEmpty(t, list.Calls[1].Result)
}

func TestAuditTool_NoOpsStoreIsNoop(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search", Arguments: map[string]any{"query": "getUser"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
}
