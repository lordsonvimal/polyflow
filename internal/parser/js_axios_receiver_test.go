package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseJSSource runs the JavaScriptParser over inline source.
func parseJSSource(t *testing.T, name, src string) []graph.Node {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(rbPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &JavaScriptParser{}
	nodes, _, _, err := p.Parse(file, "svc", m, nil)
	require.NoError(t, err)
	return nodes
}

func httpClients(nodes []graph.Node) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			out = append(out, n)
		}
	}
	return out
}

// TestMapReceiverIsNotAnHTTPCall is JobDetailModal.jsx:152. axios_instance_call
// matches any `x.get(y)` and is gated only on the service depending on axios,
// so a Map lookup was indexed as an outbound HTTP request — 44 such phantoms on
// the juniper fleet.
func TestMapReceiverIsNotAnHTTPCall(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "modal.js", `
function onMessage(data, prevDetail) {
  const updatedDetailsMap = new Map(data.map((r) => [r.id, r]));
  return prevDetail.details.map((d) => updatedDetailsMap.get(d.id));
}
`)

	assert.Empty(t, httpClients(nodes), "a Map.get is not an HTTP call")
}

func TestSetAndObjectReceiversAreNotHTTPCalls(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "caches.js", `
function go(id) {
  const listenerSet = new WeakSet();
  const positionCache = {};
  const rows = [];
  listenerSet.get(id);
  positionCache.get(id);
  rows.get(id);
}
`)

	assert.Empty(t, httpClients(nodes))
}

// TestUnknownReceiverIsKept — a cross-file axios instance has no visible
// binding. Dropping on absence of evidence would delete real edges, so the
// guard must fire only on positive evidence of a container.
func TestUnknownReceiverIsKept(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "api.js", `
import client from "./client";
function load(id) {
  client.get("/app/studies/" + id);
}
`)

	require.Len(t, httpClients(nodes), 1)
	assert.Equal(t, "/app/studies/*", httpClients(nodes)[0].Meta["url"])
}

// TestEmptyStringAliasCallIsNotAnHTTPCall — producer_alias_url_call matches
// any `ident("literal")`. React's `useState("")` is the fleet's dominant
// instance: 79 of 89 producer-alias http_clients had an empty URL.
func TestEmptyStringAliasCallIsNotAnHTTPCall(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "badge.jsx", `
function SyncStatusBadge() {
  const [status, setSyncStatus] = useState("");
  setSyncStatus("");
  return status;
}
`)

	assert.Empty(t, httpClients(nodes), "an empty string literal is not an address")
}

// TestAliasCallWithRealPathIsKept — the 10 genuine ones must survive.
func TestAliasCallWithRealPathIsKept(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "table.jsx", `
function BatchesTable() {
  apiGet("/app/favorites");
}
`)

	require.Len(t, httpClients(nodes), 1)
	assert.Equal(t, "/app/favorites", httpClients(nodes)[0].Meta["url"])
}

// TestLodashGetIsNotAnHTTPCall — JP.1: `_.get(cfg, path)` matches
// axios_instance_call and is not caught by the local-container check (the
// receiver is an imported binding, not a `new Map()`). URL-shape / instance
// evidence is what rejects it.
func TestLodashGetIsNotAnHTTPCall(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "cfg.jsx", `
import _ from "lodash";
function pick(customConfig) {
  return _.get(customConfig, ["display", "title"]);
}
`)
	assert.Empty(t, httpClients(nodes), "lodash _.get is not an HTTP call")
}

// TestParamContainerMethodsAreNotHTTPCalls — JP.1: a Map passed in as a
// parameter (elementsToReactRoots) — no local `new Map()` for the container
// check to see. `.get`/`.delete` with a non-URL argument must still drop.
func TestParamContainerMethodsAreNotHTTPCalls(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "roots.jsx", `
function detach(elementsToReactRoots, element) {
  elementsToReactRoots.get(element);
  elementsToReactRoots.delete(element);
}
`)
	assert.Empty(t, httpClients(nodes))
}

// TestBareAxiosBindingReceiverIsKept — JP.1: a call through `const http =
// axios` is a real request even when the URL is a dynamic parameter.
func TestBareAxiosBindingReceiverIsKept(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "bare.js", `
import axios from "axios";
const http = axios;
function load(p) { return http.get(p); }
`)
	require.Len(t, httpClients(nodes), 1)
}

// TestAxiosInstanceReceiverIsKept — the pattern's whole reason for existing.
func TestAxiosInstanceReceiverIsKept(t *testing.T) {
	t.Parallel()
	nodes := parseJSSource(t, "inst.js", `
const api = axios.create({ baseURL: "/client_api/v1" });
function load(id) {
  api.get("/lros/" + id);
}
`)

	assert.Len(t, httpClients(nodes), 1)
}
