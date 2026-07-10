package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
)

func TestHTMLParser_NavLinksAndEvents(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "index.html")
	src := `<html>
  <body>
    <a href="/reports">Reports</a>
    <a href="https://external.example.com">External</a>
    <form action="/save"><button onclick="submitForm()">Save</button></form>
  </body>
</html>`
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	p := parser.ForFile(file)
	require.NotNil(t, p, "expected a parser for .html files")

	m := mustMatcher(t)
	nodes, _, _, err := p.Parse(file, "site", m)
	require.NoError(t, err)

	navPaths := map[string]bool{}
	events := map[string]bool{}
	for _, n := range nodes {
		if n.Language != "" {
			assert.Equal(t, "html", n.Language)
		}
		switch {
		case n.Meta["nav_link"] == "true":
			navPaths[n.Meta["path"]] = true
			assert.Equal(t, "GET", n.Meta["method"])
		case n.Meta["pattern"] == "dom_event_attr":
			events[n.Meta["prop"]] = true
			assert.Equal(t, graph.NodeTypeDOMTarget, n.Type)
			assert.Equal(t, "submitForm()", n.Meta["handler"])
		}
	}
	assert.True(t, navPaths["/reports"], "expected nav link for /reports; got %v", navPaths)
	assert.True(t, navPaths["/save"], "expected nav link for form action /save; got %v", navPaths)
	assert.False(t, navPaths["https://external.example.com"], "external URLs must not be nav links")
	assert.True(t, events["onclick"], "expected onclick dom_event_attr node; got %v", events)
}

func TestTemplParser_NativeEventAttr(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, edges, _, err := p.Parse("testdata/page.templ", "app", m)
	require.NoError(t, err)

	var eventNode *graph.Node
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "dom_event_attr" {
			eventNode = &nodes[i]
		}
	}
	require.NotNil(t, eventNode, "expected a dom_event_attr node for onclick")
	assert.Equal(t, graph.NodeTypeDOMTarget, eventNode.Type)
	assert.Equal(t, "refresh()", eventNode.Meta["handler"])
	assert.Equal(t, "click", eventNode.Meta["event_type"])

	hasListen := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeDOMListen && e.To == eventNode.ID {
			hasListen = true
		}
	}
	assert.True(t, hasListen, "expected dom_listen edge component → onclick node")
}
