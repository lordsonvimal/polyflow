package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pyNodesOfType(nodes []graph.Node, typ graph.NodeType) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Type == typ {
			out = append(out, n)
		}
	}
	return out
}

// TestPython_WSReadPumpInsideUpgradeScoped — PW.2 gate 1: a receive call
// inside a ws_upgrade_fastapi-decorated function produces a
// subscriber/EdgeTypeWSRead node.
func TestPython_WSReadPumpInsideUpgradeScoped(t *testing.T) {
	t.Parallel()
	src := `
from fastapi import FastAPI, WebSocket

app = FastAPI()

@app.websocket("/notifications")
async def ws_handler(websocket: WebSocket):
    await websocket.accept()
    message = await websocket.receive_text()
`
	nodes, _, _ := parsePythonSrc(t, "ws.py", src)
	subs := pyNodesOfType(nodes, graph.NodeTypeSubscriber)
	require.Len(t, subs, 1)
	assert.Equal(t, "ws_read_pump_fastapi", subs[0].Meta["pattern"])
}

// TestPython_WSWritePumpInsideUpgradeScoped — PW.2 gate 2: a send_json call
// in the same function produces a publisher/EdgeTypeWSSend node.
func TestPython_WSWritePumpInsideUpgradeScoped(t *testing.T) {
	t.Parallel()
	src := `
from fastapi import FastAPI, WebSocket

app = FastAPI()

@app.websocket("/notifications")
async def ws_handler(websocket: WebSocket):
    await websocket.accept()
    await websocket.send_json({"type": "ack"})
`
	nodes, _, _ := parsePythonSrc(t, "ws2.py", src)
	pubs := pyNodesOfType(nodes, graph.NodeTypePublisher)
	require.Len(t, pubs, 1)
	assert.Equal(t, "ws_write_pump_fastapi", pubs[0].Meta["pattern"])
}

// TestPython_WSPumpOutsideUpgradeDropped — PW.2 gate 3: the same method
// names called on an object with no enclosing ws_upgrade_fastapi decorator
// produce no node (containment gate working).
func TestPython_WSPumpOutsideUpgradeDropped(t *testing.T) {
	t.Parallel()
	src := `
def poll(queue):
    message = queue.receive()
    return message

def notify(response):
    response.send("done")
`
	nodes, _, _ := parsePythonSrc(t, "unrelated.py", src)
	assert.Empty(t, pyNodesOfType(nodes, graph.NodeTypeSubscriber))
	assert.Empty(t, pyNodesOfType(nodes, graph.NodeTypePublisher))
}

// TestPython_WSPumpMixedFileOnlyScopedKept — a file with both a WS handler
// and an unrelated function using the same method names: only the scoped
// call sites produce nodes.
func TestPython_WSPumpMixedFileOnlyScopedKept(t *testing.T) {
	t.Parallel()
	src := `
from fastapi import FastAPI, WebSocket

app = FastAPI()

@app.websocket("/notifications")
async def ws_handler(websocket: WebSocket):
    await websocket.accept()
    message = await websocket.receive_text()
    await websocket.send_json({"type": "ack"})

def poll(queue):
    return queue.receive()
`
	nodes, _, _ := parsePythonSrc(t, "mixed.py", src)
	require.Len(t, pyNodesOfType(nodes, graph.NodeTypeSubscriber), 1)
	require.Len(t, pyNodesOfType(nodes, graph.NodeTypePublisher), 1)
}
