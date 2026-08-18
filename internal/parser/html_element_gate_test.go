package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseERBElements parses src as an ERB template and returns its element nodes
// keyed by label. Labels are unique per line, so the fixtures below keep one
// element per line.
func parseERBElements(t *testing.T, src string) map[string]graph.Node {
	t.Helper()
	m := mustMatcher(t)
	file := filepath.Join(t.TempDir(), "index.html.erb")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	p := parser.ForFile(file)
	require.NotNil(t, p)
	nodes, _, _, err := p.Parse(file, "app", m)
	require.NoError(t, err)

	out := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeElement {
			out[n.Label] = n
		}
	}
	return out
}

// An attribute value that is nothing but an ERB tag blanks away entirely, so
// the node it would mint names nothing at all.
func TestHTMLElementGate_InterpolatedOnlyValueIsNotAnElement(t *testing.T) {
	t.Parallel()
	els := parseERBElements(t, `
<div class="<%= status_class %>"></div>
<div id="<%= dom_id(task) %>"></div>
`)
	assert.Empty(t, els, "an all-interpolation id=/class= must mint no element node")
}

// The literal half of a mixed class list is real markup, and it survives —
// including when the interpolation comes first, which is the case that used to
// produce a node labelled "." because the label took the text before the first
// space.
func TestHTMLElementGate_LiteralClassesSurviveInterpolation(t *testing.T) {
	t.Parallel()
	els := parseERBElements(t, `
<div class="<%= tone %> cell"></div>
<div class="row <%= tone %>"></div>
`)
	require.Contains(t, els, ".cell", "leading interpolation must not swallow the literal class; got %v", keysOf(els))
	require.Contains(t, els, ".row")
	assert.Equal(t, "cell", els[".cell"].Meta["class"], "meta must carry the collapsed class list, not the blanked one")
	assert.Equal(t, "row", els[".row"].Meta["class"])
}

// An id is a single token by definition. `id="task-<%= task.id %>"` blanks to
// `task-` plus spaces, and `task-` is not that element's id — emitting it would
// let a `#task-` selector resolve to an element that never carries that id.
func TestHTMLElementGate_TruncatedIDIsNotEmitted(t *testing.T) {
	t.Parallel()
	els := parseERBElements(t, `
<div id="task-<%= task.id %>"></div>
<div id="save-btn"></div>
`)
	assert.NotContains(t, els, "#task-", "a partially interpolated id must be dropped, not truncated")
	assert.Contains(t, els, "#save-btn", "a literal id is unaffected")
}

// Static markup with ordinary multi-class attributes keeps working: the gate
// normalizes whitespace, so a plain `class="btn primary"` passes through with
// both classes intact for LinkDOMDefinitions to fan out over.
func TestHTMLElementGate_StaticMarkupIsUnchanged(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	file := filepath.Join(t.TempDir(), "page.html")
	require.NoError(t, os.WriteFile(file, []byte(`<div class="btn primary" ></div><p id="intro"></p>`), 0o644))

	p := parser.ForFile(file)
	require.NotNil(t, p)
	nodes, _, _, err := p.Parse(file, "app", m)
	require.NoError(t, err)

	els := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeElement {
			els[n.Label] = n
		}
	}
	require.Contains(t, els, ".btn", "got %v", keysOf(els))
	assert.Equal(t, "btn primary", els[".btn"].Meta["class"])
	assert.Contains(t, els, "#intro")
}
