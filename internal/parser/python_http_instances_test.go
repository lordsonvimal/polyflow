package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pyHTTPClients(nodes []graph.Node) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			out = append(out, n)
		}
	}
	return out
}

// TestPython_RequestsSessionCallResolved — Tier PH gate 1: a Session instance
// call must produce an http_client node, same shape as the existing
// requests.get(url) module-level pattern.
func TestPython_RequestsSessionCallResolved(t *testing.T) {
	t.Parallel()
	src := `
import requests

s = requests.Session()

def load():
    s.get("/users/1")
`
	nodes, _, _ := parsePythonSrc(t, "client.py", src)
	require.Len(t, pyHTTPClients(nodes), 1)
}

// TestPython_UntypedGetReceiverNotHTTPCall — Tier PH gate 2: a same-named
// .get() call on a receiver that was never assigned requests.Session()/
// httpx.Client() must not produce a false-positive http_client node.
func TestPython_UntypedGetReceiverNotHTTPCall(t *testing.T) {
	t.Parallel()
	src := `
def load(config):
    return config.get("key")
`
	nodes, _, _ := parsePythonSrc(t, "config.py", src)
	assert.Empty(t, pyHTTPClients(nodes), "a dict-like .get() is not an HTTP call")
}

// TestPython_HttpxClientBaseURLMeta — Tier PH gate 3: httpx.Client(base_url=...)
// captures base_url into node meta (via the generic alias/instance
// mechanism's URL composition, internal/contract/alias.go).
func TestPython_HttpxClientBaseURLMeta(t *testing.T) {
	t.Parallel()
	src := `
import httpx

c = httpx.Client(base_url="https://api.example.com")

def load():
    c.get("/users/1")
`
	nodes, _, _ := parsePythonSrc(t, "hx.py", src)
	clients := pyHTTPClients(nodes)
	require.Len(t, clients, 1)
	assert.Equal(t, "c", clients[0].Meta["via_alias"], "base_url composition happens later, in EnrichAliases")
}

// TestPython_RequestsModuleLevelStillWorks — Tier PH gate 4: existing
// requests_client.yaml/httpx_client.yaml module-level patterns are unaffected.
func TestPython_RequestsModuleLevelStillWorks(t *testing.T) {
	t.Parallel()
	src := `
import requests

def load():
    requests.get("/users/1")
`
	nodes, _, _ := parsePythonSrc(t, "mod.py", src)
	require.Len(t, pyHTTPClients(nodes), 1)
}

func TestPython_HttpxModuleLevelStillWorks(t *testing.T) {
	t.Parallel()
	src := `
import httpx

def load():
    httpx.get("/users/1")
`
	nodes, _, _ := parsePythonSrc(t, "mod2.py", src)
	require.Len(t, pyHTTPClients(nodes), 1)
}
