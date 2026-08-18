package parser_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const svelteFixtureBasic = "testdata/svelte_basic.svelte"
const svelteFixtureTS = "testdata/svelte_ts.svelte"
const svelteFixtureMarkupOnly = "testdata/svelte_markup_only.svelte"

func init() {
	// svelte_basic.svelte: script + template events + nav links + ternary href
	basicSrc := `<script>
import { goto } from '$app/navigation'

export function save() { goto('/saved') }
export function deleteItem() { goto('/items') }
export function onSubmit() { goto('/submit') }
</script>

<div class="container">
  <button on:click={save}>Save</button>
  <button on:click={deleteItem}>Delete</button>
  <form on:submit|preventDefault={onSubmit}>
    <input type="text" />
  </form>
  <a href="/games">Games</a>
  <a href={isAdmin ? "/admin" : "/dashboard"}>Admin or dashboard</a>
</div>

<style>
.container { padding: 1rem; }
</style>
`
	_ = os.WriteFile(svelteFixtureBasic, []byte(basicSrc), 0o644)

	// svelte_ts.svelte: lang="ts" script
	tsSrc := `<script lang="ts">
let count: number = 0
function increment(): void { count++ }
</script>

<button on:click={increment}>+{count}</button>
`
	_ = os.WriteFile(svelteFixtureTS, []byte(tsSrc), 0o644)

	// svelte_markup_only.svelte: no <script> block — only markup
	markupSrc := `<div>
  <a href="/home">Home</a>
  <p>Static content only</p>
</div>
`
	_ = os.WriteFile(svelteFixtureMarkupOnly, []byte(markupSrc), 0o644)
}

// TestSvelteParser_Registered verifies that .svelte is registered.
func TestSvelteParser_Registered(t *testing.T) {
	t.Parallel()
	p := parser.ForFile("component.svelte")
	require.NotNil(t, p, "no parser registered for .svelte")
	assert.Equal(t, "svelte", p.Language())
}

// TestSvelteParser_ExtensionsRegistered verifies ForFile returns SvelteParser for .svelte.
func TestSvelteParser_ExtensionsRegistered(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, parser.ForFile("src/routes/+page.svelte"))
}

// TestSvelteParser_BasicFixture_EventsAndNavLinks verifies real-parse extraction
// of on:click, on:submit|preventDefault, static nav links, and ternary href
// (rule 6: real fixture through real parse path).
func TestSvelteParser_BasicFixture_EventsAndNavLinks(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, unresolved, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	// Count dom_target nodes (event bindings).
	domEvents := map[string]int{}
	navLinks := []graph.Node{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget {
			domEvents[n.Meta["event"]]++
		}
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["nav_link"] == "true" {
			navLinks = append(navLinks, n)
		}
	}

	// on:click={save} and on:click={deleteItem} → 2 click events (fan-out, rule 1)
	assert.GreaterOrEqual(t, domEvents["click"], 2,
		"expected ≥2 click event bindings; got %v", domEvents)
	// on:submit|preventDefault={onSubmit} → submit event
	assert.GreaterOrEqual(t, domEvents["submit"], 1,
		"expected ≥1 submit event binding; got %v", domEvents)

	// <a href="/games"> → static nav_link (via HTML patterns)
	navPaths := map[string]bool{}
	for _, n := range navLinks {
		if p := n.Meta["path"]; p != "" {
			navPaths[p] = true
		}
	}
	assert.True(t, navPaths["/games"], "expected nav_link for /games")
	assert.GreaterOrEqual(t, len(navLinks), 1, "expected ≥1 nav_link node")

	// Event handlers are recorded as call_ref unresolved.
	callRefs := map[string]bool{}
	for _, u := range unresolved {
		if u.Kind == "call_ref" {
			callRefs[u.Name] = true
		}
	}
	assert.True(t, callRefs["save"], "expected call_ref for 'save'")
	assert.True(t, callRefs["deleteItem"], "expected call_ref for 'deleteItem'")
}

// TestSvelteParser_FanOut_TwoClickHandlers verifies rule 1 (fan-out): ≥2 entities
// sharing the "click" event key each produce an independent dom_target node.
func TestSvelteParser_FanOut_TwoClickHandlers(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	clickCount := 0
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["event"] == "click" {
			clickCount++
		}
	}
	assert.GreaterOrEqual(t, clickCount, 2,
		"rule 1 fan-out: must produce ≥2 nodes for 2 click bindings; got %d", clickCount)
}

// TestSvelteParser_ScriptFunctions verifies that script-block functions are
// extracted (rule 6: real fixture + real parse path).
func TestSvelteParser_ScriptFunctions(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	funcLabels := map[string]bool{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			funcLabels[n.Label] = true
		}
	}
	assert.True(t, funcLabels["save"] || funcLabels["deleteItem"] || funcLabels["onSubmit"],
		"expected at least one script function node; got %v", funcLabels)
}

// TestSvelteParser_TSLang verifies that <script lang="ts"> runs TypeScript patterns.
func TestSvelteParser_TSLang(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureTS)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureTS, "myapp", m, nil)
	require.NoError(t, err)

	// All nodes must have language="svelte".
	for _, n := range nodes {
		assert.Equal(t, "svelte", n.Language, "node %s must have language=svelte", n.ID)
	}

	// on:click={increment} → at least one click dom_target node from the TS fixture.
	clickCount := 0
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["event"] == "click" {
			clickCount++
		}
	}
	assert.GreaterOrEqual(t, clickCount, 1, "expected click event in TS-lang fixture")
}

// TestSvelteParser_LineOffset verifies that a node in the script block reports
// its line relative to the original .svelte file (not a virtual buffer).
func TestSvelteParser_LineOffset(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	// The script block occupies lines 1-8 in the basic fixture.
	// Function nodes (save, deleteItem) should have Line <= 8.
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && (n.Label == "save" || n.Label == "deleteItem") {
			assert.LessOrEqual(t, n.Line, 8,
				"function %q must report original file line (≤8, in script block)", n.Label)
			assert.Greater(t, n.Line, 0,
				"function %q must have positive line number", n.Label)
		}
	}
}

// TestSvelteParser_Determinism verifies two-run byte-identical output (rule 2).
func TestSvelteParser_Determinism(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	run := func() []string {
		nodes, _, unresolved, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
		require.NoError(t, err)
		var keys []string
		for _, n := range nodes {
			keys = append(keys, fmt.Sprintf("node:%s|%s", n.ID, n.Meta["pattern"]))
		}
		for _, u := range unresolved {
			keys = append(keys, fmt.Sprintf("unresolved:%s|%s", u.Kind, u.Name))
		}
		sort.Strings(keys)
		return keys
	}

	a, b := run(), run()
	require.Equal(t, len(a), len(b), "run 1 produced %d items, run 2 produced %d", len(a), len(b))
	for i := range a {
		assert.Equal(t, a[i], b[i], "position %d: %q vs %q", i, a[i], b[i])
	}
}

// TestSvelteParser_TernaryHrefCandidates verifies that href={cond ? "/a" : "/b"}
// emits key_candidates meta on an http_client node (G.6 walker convention).
func TestSvelteParser_TernaryHrefCandidates(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	var keyCandNode *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient && nodes[i].Meta["key_candidates"] != "" {
			keyCandNode = &nodes[i]
			break
		}
	}
	require.NotNil(t, keyCandNode,
		"expected an http_client node with key_candidates meta for ternary href")
	assert.Contains(t, keyCandNode.Meta["key_candidates"], "/admin",
		"key_candidates must include /admin branch")
	assert.Contains(t, keyCandNode.Meta["key_candidates"], "/dashboard",
		"key_candidates must include /dashboard branch")
}

// TestSvelteParser_MarkupOnly verifies that a .svelte file with only markup
// (no <script> block) parses via html patterns alone and produces nav_link nodes.
func TestSvelteParser_MarkupOnly(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureMarkupOnly)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureMarkupOnly, "myapp", m, nil)
	require.NoError(t, err)

	// No function nodes (no script).
	for _, n := range nodes {
		assert.NotEqual(t, graph.NodeTypeFunction, n.Type,
			"markup-only .svelte must produce no function nodes; got %s", n.ID)
	}

	// <a href="/home"> must produce a nav_link node (HTML patterns run on markup).
	hasNavLink := false
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["nav_link"] == "true" {
			if n.Meta["path"] == "/home" {
				hasNavLink = true
			}
		}
	}
	assert.True(t, hasNavLink,
		"markup-only .svelte must have a nav_link for /home via HTML patterns")
}

// TestSvelteParser_NegativeNoEventBindings verifies that a .svelte with only static
// content produces no dom_target or unresolved call_ref entries.
func TestSvelteParser_NegativeNoEventBindings(t *testing.T) {
	t.Parallel()
	src := `<div><p>Hello world</p></div>
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "Static.svelte")
	require.NoError(t, os.WriteFile(tmpFile, []byte(src), 0o644))

	m := mustMatcher(t)
	p := parser.ForFile(tmpFile)
	require.NotNil(t, p)

	nodes, _, unresolved, err := p.Parse(tmpFile, "myapp", m, nil)
	require.NoError(t, err)

	for _, n := range nodes {
		assert.NotEqual(t, graph.NodeTypeDOMTarget, n.Type,
			"expected no dom_target nodes in static-only .svelte; got %s", n.ID)
	}
	for _, u := range unresolved {
		assert.NotEqual(t, "call_ref", u.Kind,
			"expected no call_ref unresolved in static-only .svelte; got %s", u.Name)
	}
}

// TestSvelteParser_DynamicHrefLedgered verifies that href={someVar} (unresolvable
// dynamic expression) is ledgered as dynamic_url rather than silently dropped.
func TestSvelteParser_DynamicHrefLedgered(t *testing.T) {
	t.Parallel()
	src := `<script>
export let targetPath
</script>
<a href={targetPath}>Dynamic</a>
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "Dynamic.svelte")
	require.NoError(t, os.WriteFile(tmpFile, []byte(src), 0o644))

	m := mustMatcher(t)
	p := parser.ForFile(tmpFile)
	require.NotNil(t, p)

	_, _, unresolved, err := p.Parse(tmpFile, "myapp", m, nil)
	require.NoError(t, err)

	hasDynamicURL := false
	for _, u := range unresolved {
		if u.Kind == "dynamic_url" {
			hasDynamicURL = true
		}
	}
	assert.True(t, hasDynamicURL,
		"expected dynamic_url unresolved ref for unresolvable href={...} binding")
}

// TestSvelteParser_EventModifierStripped verifies that on:submit|preventDefault
// produces event name "submit" (modifier stripped).
func TestSvelteParser_EventModifierStripped(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(svelteFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(svelteFixtureBasic, "myapp", m, nil)
	require.NoError(t, err)

	hasSubmit := false
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["event"] == "submit" {
			hasSubmit = true
		}
	}
	assert.True(t, hasSubmit,
		"on:submit|preventDefault must produce a dom_target with event='submit'")
}

// TestSvelteParser_NotUnparsed verifies that .svelte no longer appears as an
// unparsed extension now that SvelteParser is registered.
func TestSvelteParser_NotUnparsed(t *testing.T) {
	t.Parallel()
	p := parser.ForFile("App.svelte")
	assert.NotNil(t, p, ".svelte must now have a registered parser (dropped from B.0 unparsed set)")
}
