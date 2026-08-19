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

	newNodes, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

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

	newNodes, _ := linker.LinkJSAPIWrapperCalls(nodes, serviceFiles)

	for _, n := range newNodes {
		assert.NotEqual(t, "outer", n.Meta["wrapper"],
			"outer must not be attributed as a wrapper via its nested closure's own forwarding")
	}
}
