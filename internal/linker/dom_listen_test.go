package linker_test

// Tier K.4 acceptance, through REAL parses (bug-class rule 6 — hand-built nodes
// would not exercise the pattern matcher / structural-pass seam this phase is
// built on): a jQuery listener in a .js asset must bind to the ERB element its
// selector names, in the direction a trace walks —
//
//	element --dom_listen--> handler --calls--> http_client
//
// so "what happens when I click this button" is answerable from the view.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func domListenFixture(t *testing.T) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	for _, f := range []string{
		"testdata/dom_listen/index.html.erb",
		"testdata/dom_listen/page.js",
	} {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, es, us, err := p.Parse(f, "app", m)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
		edges = append(edges, es...)
		unresolved = append(unresolved, us...)
	}
	_, linkEdges, linkUnresolved := linker.LinkDOMDefinitions(nodes)
	return nodes, append(edges, linkEdges...), append(unresolved, linkUnresolved...)
}

// elementIDs returns the node IDs of every element node declaring the given id
// or class attribute value.
func elementIDs(nodes []graph.Node, attr, value string) []string {
	var out []string
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeElement {
			continue
		}
		switch attr {
		case "id":
			if n.Meta["id"] == value {
				out = append(out, n.ID)
			}
		case "class":
			for _, c := range splitFields(n.Meta["class"]) {
				if c == value {
					out = append(out, n.ID)
				}
			}
		}
	}
	return out
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// listenTo returns the labels of every node an element dom_listens to.
func listenTo(nodes []graph.Node, edges []graph.Edge, elemID string) map[string]graph.Edge {
	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	out := map[string]graph.Edge{}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeDOMListen || e.From != elemID {
			continue
		}
		if n, ok := byID[e.To]; ok {
			out[n.Label] = e
		}
	}
	return out
}

// TestDOMListen_DelegatedBindsToSelectorNotRoot is the phase's central claim.
// `$(document).on("click", ".js-remove-track", fn)` listens on .js-remove-track;
// binding it to `document` — one node for every delegated listener in the file —
// is what made listener attribution useless. Both elements carrying the class
// bind, because only the request that rendered the page knows which were on it
// (rule #1: fan out, never pick one).
func TestDOMListen_DelegatedBindsToSelectorNotRoot(t *testing.T) {
	t.Parallel()
	nodes, edges, _ := domListenFixture(t)

	targets := elementIDs(nodes, "class", "js-remove-track")
	require.Len(t, targets, 2, "fixture declares .js-remove-track twice")

	for _, elemID := range targets {
		got := listenTo(nodes, edges, elemID)
		e, ok := got["click@.js-remove-track"]
		require.True(t, ok, "element %s has no delegated click handler; got %v", elemID, got)
		assert.Equal(t, "click", e.Meta["event"])
		assert.Equal(t, "true", e.Meta["delegated"])
		assert.Equal(t, "document", e.Meta["delegate_root"])
	}

	// The delegate root must not itself become the listening element.
	for _, e := range edges {
		if e.Type == graph.EdgeTypeDOMListen {
			assert.NotContains(t, e.From, ":element:document:", "delegated listener bound to its root")
		}
	}
}

// TestDOMListen_ReceiverSelectorAndShorthand: the two non-delegated spellings.
// The pattern matcher never captured the receiver's selector — its query cannot
// make that argument optional without dropping `$(document).on(evt, fn)` — so
// these bound to nothing at all before this phase.
func TestDOMListen_ReceiverSelectorAndShorthand(t *testing.T) {
	t.Parallel()
	nodes, edges, _ := domListenFixture(t)

	btn := elementIDs(nodes, "id", "approve-btn")
	require.Len(t, btn, 1)
	got := listenTo(nodes, edges, btn[0])

	assert.Contains(t, got, "click@#approve-btn", "2-arg .on() did not bind its receiver selector")
	assert.Contains(t, got, "showHint", "named handler did not resolve to its function node")
	assert.Equal(t, "mouseenter", got["showHint"].Meta["event"])

	cls := elementIDs(nodes, "class", "js-approve")
	require.Len(t, cls, 1)
	shorthand := listenTo(nodes, edges, cls[0])
	assert.Contains(t, shorthand, "change@.js-approve", "shorthand .change() did not bind")
}

// TestDOMListen_HandlerOwnsItsBody is what makes the chain worth having: the
// inline handler's `$.ajax` call must attribute to the handler, not to the
// file-wide (module) node every listener used to share.
func TestDOMListen_HandlerOwnsItsBody(t *testing.T) {
	t.Parallel()
	nodes, edges, _ := domListenFixture(t)

	var handlerID string
	for i := range nodes {
		if nodes[i].Label == "click@#approve-btn" {
			handlerID = nodes[i].ID
		}
	}
	require.NotEmpty(t, handlerID, "no node for the inline handler")

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	var reached []string
	for _, e := range edges {
		if e.From != handlerID {
			continue
		}
		if n, ok := byID[e.To]; ok && n.Type == graph.NodeTypeHTTPClient {
			reached = append(reached, n.Label)
		}
	}
	assert.Contains(t, reached, "/approve",
		"handler does not reach the endpoint its click fires; edges from %s: %v", handlerID, reached)
}

// TestDOMListen_LedgerDiscipline: a selector held in a variable is a clue this
// pass tried and failed to resolve, so it is ledgered. A document-scoped
// listener with no delegated selector names no element at all — there is nothing
// to resolve, so ledgering it would fabricate a clue (the K.2 rule).
func TestDOMListen_LedgerDiscipline(t *testing.T) {
	t.Parallel()
	_, _, unresolved := domListenFixture(t)

	var selectors []string
	for _, u := range unresolved {
		if u.Kind == "selector_dynamic" {
			selectors = append(selectors, u.Name)
		}
	}
	assert.Contains(t, selectors, "rowSelector")
	assert.NotContains(t, selectors, "document")
	assert.NotContains(t, selectors, "$(document)")
	assert.NotContains(t, selectors, "body", "a bare tag selector is not a failed resolution")
}

func TestDOMListen_Deterministic(t *testing.T) {
	t.Parallel()
	_, first, _ := domListenFixture(t)
	for i := 0; i < 4; i++ {
		_, again, _ := domListenFixture(t)
		require.Equal(t, first, again)
	}
}
