package patterns_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// countByType tallies nodes of each type produced by parsing src as Go through
// the full pattern path (Match → MatchToGraph), the same route the indexer uses.
func countByType(t *testing.T, src string) (map[graph.NodeType]int, []graph.Node) {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "f.go", []byte(src))
	require.NoError(t, err)
	nodes, _, _ := patterns.MatchToGraph("svc", results)
	counts := map[graph.NodeType]int{}
	for _, n := range nodes {
		counts[n.Type]++
	}
	return counts, nodes
}

// Cross-yield fix #1: the un-gated .Get(...) query captures non-HTTP calls
// (url.Values.Get, http.Header.Get) and relative asset strings as bogus
// http_client producers. Only the real absolute-path call survives.
func TestCrossYield_SuppressNonURLHTTPClients(t *testing.T) {
	src := `package svc

import "net/http"

func handler(values url.Values, header http.Header) {
	_ = values.Get("user_id")   // not a URL — suppressed
	_ = header.Get("email")     // not a URL — suppressed
	_ = cache.Get("md5")        // not a URL — suppressed
	http.Get("static/js/x.js")  // relative asset — suppressed
	http.Get("/api/v1/real")    // real absolute path — kept
}`
	counts, nodes := countByType(t, src)
	assert.Equal(t, 1, counts[graph.NodeTypeHTTPClient],
		"only the absolute-path http.Get is a real http_client; got %+v", nodes)
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			assert.Equal(t, "/api/v1/real", n.Meta["url"])
		}
	}
}

// Cross-yield fix #4: a literal third-party URL is a real external boundary and
// must type as external_service (resolved-external), not an unresolved http_call.
func TestCrossYield_ExternalHostBecomesExternalService(t *testing.T) {
	src := `package svc

import "net/http"

func fetch() {
	http.Get("https://pypi.org/pypi/requests/json")
}`
	counts, nodes := countByType(t, src)
	assert.Equal(t, 0, counts[graph.NodeTypeHTTPClient], "external URL must not stay http_client")
	assert.Equal(t, 1, counts[graph.NodeTypeExternalService], "external URL → external_service")
	for _, n := range nodes {
		if n.Type == graph.NodeTypeExternalService {
			assert.Equal(t, "pypi.org", n.Meta["cloud_service"])
			assert.Equal(t, "pypi.org", n.Label)
		}
	}
}

// A bare (dot-less) host is a workspace-internal service name, not a public
// boundary, and must not be externalized.
func TestCrossYield_BareHostNotExternal(t *testing.T) {
	src := `package svc

import "net/http"

func fetch() {
	http.Get("http://svc-c-agent/api/v1/health")
}`
	counts, _ := countByType(t, src)
	assert.Equal(t, 1, counts[graph.NodeTypeHTTPClient], "bare-host URL stays http_client (internal)")
	assert.Equal(t, 0, counts[graph.NodeTypeExternalService])
}
