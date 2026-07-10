package parser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
)

// Form actions carry their submit verb; data-method spoofs one on anchors;
// plain anchors (including those outside the form) stay GET.
func TestTemplParser_NavLinkMethods(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", "app", m)
	require.NoError(t, err)

	methodByPath := map[string]string{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["nav_link"] == "true" {
			methodByPath[n.Meta["path"]] = n.Meta["method"]
		}
	}
	assert.Equal(t, "POST", methodByPath["/users"], `<form action="/users" method="post">`)
	assert.Equal(t, "DELETE", methodByPath["/users/1"], `<a href="/users/1" data-method="delete">`)
	assert.Equal(t, "GET", methodByPath["/users/list"], "plain anchor stays GET")
	assert.Equal(t, "GET", methodByPath["/dashboard"], "plain anchor stays GET")
}
