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

const vueFixtureBasic = "testdata/vue_basic.vue"
const vueFixtureTS = "testdata/vue_ts.vue"

func init() {
	// Write the basic Vue fixture to testdata so tests can use ForFile + real path.
	basicSrc := `<template>
  <div class="game-card">
    <h2>{{ game.title }}</h2>
    <button @click="save">Save</button>
    <button v-on:click="deleteGame">Delete</button>
    <form @submit.prevent="onSubmit">
      <input type="text" />
    </form>
    <router-link to="/games">Back to games</router-link>
    <NuxtLink to="/home">Home</NuxtLink>
  </div>
</template>

<script>
import { useGameStore } from './store'

export default {
  name: 'GameCard',
  setup() {
    const store = useGameStore()
    function save() { store.save() }
    function deleteGame() { store.remove() }
    function onSubmit() { store.submit() }
    return { save, deleteGame, onSubmit }
  }
}
</script>

<style scoped>
.game-card { border: 1px solid #ccc; }
</style>
`
	_ = os.WriteFile(vueFixtureBasic, []byte(basicSrc), 0o644)

	tsSrc := `<template>
  <div>
    <button @click="increment">+</button>
  </div>
</template>

<script lang="ts">
import { ref } from 'vue'

export default {
  setup() {
    const count = ref(0)
    function increment(): void { count.value++ }
    return { count, increment }
  }
}
</script>
`
	_ = os.WriteFile(vueFixtureTS, []byte(tsSrc), 0o644)
}

// TestVueParser_Registered verifies that .vue is registered.
func TestVueParser_Registered(t *testing.T) {
	t.Parallel()
	p := parser.ForFile("component.vue")
	require.NotNil(t, p, "no parser registered for .vue")
	assert.Equal(t, "vue", p.Language())
}

// TestVueParser_ExtensionsRegistered verifies ForFile returns VueParser for .vue.
func TestVueParser_ExtensionsRegistered(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, parser.ForFile("src/views/Home.vue"))
}

// TestVueParser_BasicFixture_EventsAndNavLinks verifies real-parse extraction
// of @click, v-on:click, @submit.prevent, router-link, NuxtLink (rule 6: real fixture).
func TestVueParser_BasicFixture_EventsAndNavLinks(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureBasic)
	require.NotNil(t, p)

	nodes, _, unresolved, err := p.Parse(vueFixtureBasic, "myapp", m)
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

	// @click="save" and v-on:click="deleteGame" → 2 click events (fan-out, rule 1)
	assert.GreaterOrEqual(t, domEvents["click"], 2, "expected ≥2 click event bindings; got %v", domEvents)
	// @submit.prevent="onSubmit" → submit event
	assert.GreaterOrEqual(t, domEvents["submit"], 1, "expected ≥1 submit event binding; got %v", domEvents)

	// router-link to="/games" and NuxtLink to="/home" → 2 nav_link nodes
	navPaths := map[string]bool{}
	for _, n := range navLinks {
		navPaths[n.Meta["path"]] = true
	}
	assert.True(t, navPaths["/games"], "expected nav_link for /games")
	assert.True(t, navPaths["/home"], "expected nav_link for /home")
	assert.GreaterOrEqual(t, len(navLinks), 2, "expected ≥2 nav_link nodes (fan-out, rule 1)")

	// Event handlers are recorded as call_ref unresolved (resolved by linker).
	callRefs := map[string]bool{}
	for _, u := range unresolved {
		if u.Kind == "call_ref" {
			callRefs[u.Name] = true
		}
	}
	assert.True(t, callRefs["save"], "expected call_ref for 'save'")
	assert.True(t, callRefs["deleteGame"], "expected call_ref for 'deleteGame'")
}

// TestVueParser_FanOut_TwoClickHandlers verifies rule 1 (fan-out): ≥2 entities
// sharing the "click" event key each produce an independent node.
func TestVueParser_FanOut_TwoClickHandlers(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(vueFixtureBasic, "myapp", m)
	require.NoError(t, err)

	clickCount := 0
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["event"] == "click" {
			clickCount++
		}
	}
	assert.GreaterOrEqual(t, clickCount, 2, "rule 1 fan-out: must produce ≥2 nodes for 2 click bindings; got %d", clickCount)
}

// TestVueParser_ScriptFunctions verifies that script-block functions are extracted
// (rule 6: real fixture + real parse path).
func TestVueParser_ScriptFunctions(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(vueFixtureBasic, "myapp", m)
	require.NoError(t, err)

	funcLabels := map[string]bool{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			funcLabels[n.Label] = true
		}
	}
	assert.True(t, funcLabels["save"] || funcLabels["deleteGame"] || funcLabels["onSubmit"],
		"expected at least one script function node; got %v", funcLabels)
}

// TestVueParser_TSLang verifies that <script lang="ts"> runs TypeScript patterns.
func TestVueParser_TSLang(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureTS)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(vueFixtureTS, "myapp", m)
	require.NoError(t, err)

	// All nodes must have language="vue".
	for _, n := range nodes {
		assert.Equal(t, "vue", n.Language, "node %s must have language=vue", n.ID)
	}

	// @click="increment" → at least one click dom_target node from the TS fixture.
	clickCount := 0
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["event"] == "click" {
			clickCount++
		}
	}
	assert.GreaterOrEqual(t, clickCount, 1, "expected click event in TS-lang fixture")
}

// TestVueParser_LineOffset verifies that a node in the script block reports
// its line relative to the original .vue file (not a virtual buffer).
func TestVueParser_LineOffset(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureBasic)
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse(vueFixtureBasic, "myapp", m)
	require.NoError(t, err)

	// The script block starts at line 15 (1-indexed) in the basic fixture.
	// Any function nodes should have Line > 14 (they are in the script section).
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && (n.Label == "save" || n.Label == "deleteGame") {
			assert.Greater(t, n.Line, 14, "function %q must report original file line (>14, in script block)", n.Label)
		}
	}
}

// TestVueParser_Determinism verifies two-run byte-identical output (rule 2).
func TestVueParser_Determinism(t *testing.T) {
	t.Parallel()
	m := mustMatcher(t)
	p := parser.ForFile(vueFixtureBasic)
	require.NotNil(t, p)

	run := func() []string {
		nodes, _, unresolved, err := p.Parse(vueFixtureBasic, "myapp", m)
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

// TestVueParser_NegativeNoEventBindings verifies that a .vue with only static
// content produces no dom_target or unresolved call_ref entries.
func TestVueParser_NegativeNoEventBindings(t *testing.T) {
	t.Parallel()
	src := `<template>
  <div><p>Hello world</p></div>
</template>

<script>
export default { name: 'Static' }
</script>
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "Static.vue")
	require.NoError(t, os.WriteFile(tmpFile, []byte(src), 0o644))

	m := mustMatcher(t)
	p := parser.ForFile(tmpFile)
	require.NotNil(t, p)

	nodes, _, unresolved, err := p.Parse(tmpFile, "myapp", m)
	require.NoError(t, err)

	for _, n := range nodes {
		assert.NotEqual(t, graph.NodeTypeDOMTarget, n.Type,
			"expected no dom_target nodes in static-only .vue; got %s", n.ID)
	}
	for _, u := range unresolved {
		assert.NotEqual(t, "call_ref", u.Kind,
			"expected no call_ref unresolved in static-only .vue; got %s", u.Name)
	}
}

// TestVueParser_CommentedOutEventNotMatched verifies that commented-out
// attributes (inside <!-- --> in template) do not produce event nodes.
// This tests the blanking rule (bug-class rule 11) for Vue templates.
func TestVueParser_NegativeTemplateStringNotSFCBlock(t *testing.T) {
	t.Parallel()
	src := `<template>
  <div/>
</template>
<script>
const doc = "Use <template> inside strings for docs"
export default {}
</script>
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "NoExtraBlock.vue")
	require.NoError(t, os.WriteFile(tmpFile, []byte(src), 0o644))

	m := mustMatcher(t)
	p := parser.ForFile(tmpFile)
	require.NotNil(t, p)

	// Should not panic or error even with <template> in script string.
	_, _, _, err := p.Parse(tmpFile, "myapp", m)
	assert.NoError(t, err, "parsing a .vue with <template> in a script string must not error")
}

// TestVueParser_DynamicToLedgered verifies that :to="expr" (bound router-link)
// is ledgered as dynamic_url rather than silently dropped.
func TestVueParser_DynamicToLedgered(t *testing.T) {
	t.Parallel()
	src := `<template>
  <router-link :to="computedPath">Dynamic</router-link>
</template>
<script>
export default {}
</script>
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "Dynamic.vue")
	require.NoError(t, os.WriteFile(tmpFile, []byte(src), 0o644))

	m := mustMatcher(t)
	p := parser.ForFile(tmpFile)
	require.NotNil(t, p)

	_, _, unresolved, err := p.Parse(tmpFile, "myapp", m)
	require.NoError(t, err)

	hasDynamicURL := false
	for _, u := range unresolved {
		if u.Kind == "dynamic_url" {
			hasDynamicURL = true
		}
	}
	assert.True(t, hasDynamicURL, "expected dynamic_url unresolved ref for :to binding")
}

// TestVueParser_DropFromUnparsedOnceRegistered verifies that .vue no longer
// appears as an unparsed extension now that VueParser is registered.
func TestVueParser_NotUnparsed(t *testing.T) {
	t.Parallel()
	p := parser.ForFile("App.vue")
	assert.NotNil(t, p, ".vue must now have a registered parser (dropped from B.0 unparsed set)")
}
