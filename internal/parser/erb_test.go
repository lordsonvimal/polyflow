package parser_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestERBParser_Registered(t *testing.T) {
	p := parser.ForFile("views/users.html.erb")
	require.NotNil(t, p, "no parser registered for .erb (filepath.Ext('.html.erb') == '.erb')")
	assert.Equal(t, "erb", p.Language())
}

func TestERBParser_Fixture(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/view.erb")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/view.erb", "myapp", m)
	require.NoError(t, err)

	patCount := make(map[string]int)
	for _, n := range nodes {
		patCount[n.Meta["pattern"]]++
	}

	// ≥3 nav_link_rails_helper: reports_path, new_report_path, archive_report_path
	assert.GreaterOrEqual(t, patCount["nav_link_rails_helper"], 3,
		"expected ≥3 nav_link_rails_helper; patterns: %v", patCount)

	// ≥1 nav_link_rails_form_helper: form_with url: reports_path
	assert.GreaterOrEqual(t, patCount["nav_link_rails_form_helper"], 1,
		"expected ≥1 nav_link_rails_form_helper; patterns: %v", patCount)

	// Static href="/home" comes from the HTML pass.
	assert.GreaterOrEqual(t, patCount["nav_link_href"], 1,
		"expected ≥1 nav_link_href from static HTML <a href='/home'>")

	// Inline onclick="loadReports()" from HTML pass.
	assert.GreaterOrEqual(t, patCount["dom_event_attr"], 1,
		"expected ≥1 dom_event_attr for onclick handler")

	// All http_client nodes must carry nav_link=true.
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			assert.Equal(t, "true", n.Meta["nav_link"],
				"http_client node %s missing nav_link=true", n.ID)
		}
	}
}

func TestERBParser_NegativeStaticHTML(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("x.erb")
	require.NotNil(t, p)

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "static.erb")
	content := `<html><body><a href="/about">About</a><p>No Ruby here</p></body></html>`
	require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0o644))

	nodes, _, _, err := p.Parse(tmpFile, "svc", m)
	require.NoError(t, err)

	for _, n := range nodes {
		if n.Language == "ruby" {
			t.Errorf("unexpected ruby node in static-only ERB: %s (pattern=%s)", n.ID, n.Meta["pattern"])
		}
	}

	hasNavLink := false
	for _, n := range nodes {
		if strings.HasPrefix(n.Meta["pattern"], "nav_link") {
			hasNavLink = true
		}
	}
	assert.True(t, hasNavLink, "expected at least one nav_link node from static <a href>")
}

func TestERBParser_Determinism(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/view.erb")
	require.NotNil(t, p)

	run := func() []string {
		nodes, _, _, err := p.Parse("testdata/view.erb", "myapp", m)
		require.NoError(t, err)
		ids := make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID + "|" + n.Meta["pattern"]
		}
		sort.Strings(ids)
		return ids
	}

	a, b := run(), run()
	require.Equal(t, len(a), len(b), "run 1 produced %d nodes, run 2 produced %d", len(a), len(b))
	for i := range a {
		assert.Equal(t, a[i], b[i], "position %d: %q vs %q", i, a[i], b[i])
	}
}

func TestForFile_ERBExtension(t *testing.T) {
	assert.NotNil(t, parser.ForFile("app/views/users/index.html.erb"))
	assert.NotNil(t, parser.ForFile("layouts/application.erb"))
	assert.Equal(t, "erb", parser.ForFile("index.erb").Language())
}
