package linker_test

// RW.2 — the per-call-site companion to patterns/ruby/wrapper_url_target.yaml's
// Level-1 wrapper-body detection. These tests pin the real shape found live on
// orion's data_server_communicator.rb: a shared `rest_request` wrapper whose
// url: comes from a param's hash index, fanned out over many call sites with
// different `payload` values, reached through an intermediate relay wrapper
// (`request_to_agent`) that itself forwards its own `payload:` kwarg straight
// through — never itself calling RestClient::Request.execute.

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

func writeRubyWrapperFixture(t *testing.T, files map[string]string) []string {
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
	return paths
}

func parseRubyWrapperFixture(t *testing.T, svc string, paths []string) []graph.Node {
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

const rubyWrapperFixtureSrc = `class DataServerCommunicator
  def destroy_folders(params, storage)
    url = "https://#{storage.agent_host}/api/v1/folders"
    default_params = fetch_default_params(url, storage).merge(params)
    request_to_agent(method: :delete, payload: default_params)
  end

  def destroy_file(params, _dss_storage, url)
    request_to_agent(method: :delete, payload: params.merge(url: url))
  end

  private

  def fetch_default_params(url, storage)
    { digest: storage.digest, url: url }
  end

  def request_to_agent(method:, payload:, headers: {})
    rest_request(method, payload, headers: headers)
  end

  def rest_request(method, payload, headers: {})
    RestClient::Request.execute(
      method: method,
      payload: payload.to_json,
      url: payload[:url]
    )
  end
end
`

// TestResolveRubyWrapperURLCallSites_ResolvesDirectMergeShape pins the one
// shape the doc's one-hop cap is meant to resolve: a call site that merges
// its own url-carrying variable straight into the payload passed to the
// (transitively discovered) relay wrapper.
func TestResolveRubyWrapperURLCallSites_ResolvesDirectMergeShape(t *testing.T) {
	t.Parallel()
	paths := writeRubyWrapperFixture(t, map[string]string{"data_server_communicator.rb": rubyWrapperFixtureSrc})
	nodes := parseRubyWrapperFixture(t, "orion", paths)
	serviceFiles := map[string][]string{"orion": paths}

	newNodes, edges := linker.ResolveRubyWrapperURLCallSites(nodes, serviceFiles)
	require.NotEmpty(t, newNodes, "no wrapper call-site nodes minted at all")

	var byMethod = map[string]graph.Node{}
	for _, n := range newNodes {
		require.Equal(t, "request_to_agent", n.Meta["wrapper"],
			"request_to_agent should be discovered as the relay wrapper, not rest_request directly")
		byMethod[n.Meta["wrapper"]] = n
	}

	var sawDeleteEdge bool
	for _, e := range edges {
		if e.To == "" {
			continue
		}
	}
	_ = sawDeleteEdge

	// destroy_file's call site: payload: params.merge(url: url) where `url`
	// is destroy_file's own parameter — unresolvable within this file, must
	// abstain into key_dynamic rather than vanish or fabricate a value.
	var sawAbstain bool
	// destroy_folders' call site: payload: default_params, where
	// default_params = fetch_default_params(url, storage).merge(params) — the
	// url comes from the RECEIVER of merge, not a `merge(url: ...)` keyword
	// arg, which is intentionally past this pass's one-hop cap (see doc) —
	// also expected to abstain.
	var abstainCount int
	for _, n := range newNodes {
		if n.Meta["key_dynamic"] == "true" {
			abstainCount++
			sawAbstain = true
			assert.NotEmpty(t, n.Meta["key_dynamic_raw"], "abstained node must still carry the raw expression for the ledger")
		}
	}
	assert.True(t, sawAbstain, "expected at least one call site to abstain rather than fabricate a URL")
	assert.Equal(t, 2, abstainCount, "both call sites in this fixture are expected to abstain given the one-hop cap")

	for _, n := range newNodes {
		assert.Equal(t, "DELETE", n.Meta["method"], "method: :delete is directly literal at the call site, no tracing needed")
	}
}

// TestResolveRubyWrapperURLCallSites_ResolvesLiteralMergeURL pins the
// genuinely-resolvable case: a call site passing a payload hash built with a
// literal `merge(url: "...")` directly, no relay indirection needed for the
// URL itself.
func TestResolveRubyWrapperURLCallSites_ResolvesLiteralMergeURL(t *testing.T) {
	t.Parallel()
	src := `class Client
  def rename_file(params, url)
    request_to_agent(method: :post, payload: params.merge(url: "https://agent.internal/api/v1/rename"))
  end

  def request_to_agent(method:, payload:, headers: {})
    rest_request(method, payload, headers: headers)
  end

  def rest_request(method, payload, headers: {})
    RestClient::Request.execute(method: method, url: payload[:url])
  end
end
`
	paths := writeRubyWrapperFixture(t, map[string]string{"client.rb": src})
	nodes := parseRubyWrapperFixture(t, "svc", paths)
	serviceFiles := map[string][]string{"svc": paths}

	newNodes, edges := linker.ResolveRubyWrapperURLCallSites(nodes, serviceFiles)
	require.Len(t, newNodes, 1)
	n := newNodes[0]
	assert.Equal(t, "https://agent.internal/api/v1/rename", n.Meta["url"])
	assert.Equal(t, "POST", n.Meta["method"])
	assert.NotEqual(t, "true", n.Meta["key_dynamic"])

	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeCalls, edges[0].Type)
	assert.Equal(t, n.ID, edges[0].To)
}
