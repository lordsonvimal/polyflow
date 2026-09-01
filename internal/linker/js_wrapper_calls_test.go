package linker_test

// AH-follow-up-adjacent regression: the JS fetchGraph found-not-fixed gap
// (Tier JH's Measured result) traced to LinkJSAPIWrapperCalls only knowing a
// function is a wrapper when its OWN body calls fetch/axios directly — a
// wrapper of a wrapper (`fetchWithTimeout` forwards to another in-service
// helper, which itself forwards to `fetch`) was invisible. These tests pin
// the transitive same-service discovery that closes that class of gap.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func writeJSWrapperFixture(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

func parseJSWrapperFixture(t *testing.T, svc string, paths []string) []graph.Node {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	var nodes []graph.Node
	for _, f := range paths {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, _, _, err := p.Parse(f, svc, m, nil)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
	}
	return nodes
}

// TestLinkJSAPIWrapperCalls_TransitiveWrapper is the fetchGraph shape,
// simplified to a same-service two-hop chain: `outer` forwards its own `url`
// param to `inner`, and only `inner`'s body calls `fetch` directly. Before
// transitive discovery, `outer` was invisible to LinkJSAPIWrapperCalls
// because nothing in `outer`'s own body calls fetch/axios.
func TestLinkJSAPIWrapperCalls_TransitiveWrapper(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSWrapperFixture(t, map[string]string{
		"client.ts": `function inner(url: string, init: RequestInit = {}) {
  return fetch(url, init);
}

function outer(url: string) {
  return inner(url, {});
}

export function fetchGraph(params: string) {
  const url = ` + "`/api/graph${params}`" + `;
  return outer(url);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var sawOuterCallSite bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "outer" {
			sawOuterCallSite = true
			assert.Equal(t, "url", n.Meta["url_expr"],
				"outer's call site should mint an http_client node from its own url argument")
		}
	}
	assert.True(t, sawOuterCallSite,
		"outer was not discovered as a transitive wrapper of inner, which itself wraps fetch")
	_ = dir
}

// TestLinkJSAPIWrapperCalls_DedupesProducerAliasDuplicate is RT.5: a wrapper
// called with a literal URL (`apiGet("/app/things")`) is captured both as a
// producer_alias_url_call (any `ident("literal")`) and, richer, as a wrapper
// call site. The producer_alias_url_call duplicate at the same call site must
// be returned for removal.
func TestLinkJSAPIWrapperCalls_DedupesProducerAliasDuplicate(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"api.ts": `export function apiGet(path: string) {
  return fetch(path);
}

export function load() {
  return apiGet("/app/things");
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	var aliasDupID string
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["pattern"] == "producer_alias_url_call" {
			aliasDupID = n.ID
		}
	}
	require.NotEmpty(t, aliasDupID, "fixture should produce a producer_alias_url_call node for apiGet(\"/app/things\")")

	newNodes, _, removeIDs := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	var wrapperAtSameSite bool
	for _, n := range newNodes {
		if n.Meta["wrapper"] == "apiGet" && n.Meta["url"] == "/app/things" {
			wrapperAtSameSite = true
		}
	}
	require.True(t, wrapperAtSameSite, "apiGet call site should mint a wrapper http_client node")
	assert.True(t, removeIDs[aliasDupID], "producer_alias_url_call duplicate should be marked for removal")
}

// TestLinkJSAPIWrapperCalls_NestedClosureNotAttributed guards the "do not
// descend into a nested closure" rule: a callback defined INSIDE a wrapper
// candidate that itself forwards a parameter must not make the OUTER
// function a wrapper on the strength of an inner closure's own unrelated
// parameter.
func TestLinkJSAPIWrapperCalls_NestedClosureNotAttributed(t *testing.T) {
	t.Parallel()
	_, paths := writeJSWrapperFixture(t, map[string]string{
		"client.ts": `function inner(url: string) {
  return fetch(url);
}

function outer(unrelated: string) {
  const helper = (url: string) => inner(url);
  return helper(unrelated);
}
`,
	})
	nodes := parseJSWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, _, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	for _, n := range newNodes {
		assert.NotEqual(t, "outer", n.Meta["wrapper"],
			"outer must not be attributed as a wrapper via its nested closure's own forwarding")
	}
}
