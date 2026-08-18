package parser

import (
	"strings"
	"testing"
)

// TestSplitSvelteSFC_PreservesLineNumbers verifies byte offset preservation:
// newline count must be identical to the original source.
func TestSplitSvelteSFC_PreservesLineNumbers(t *testing.T) {
	t.Parallel()
	src := []byte("<script>\nconst x = 1\n</script>\n\n<div>\n  <p>Hello</p>\n</div>\n")
	blocks := splitSvelteSFC(src)

	virtualScript, _ := buildSvelteVirtualScript(src, blocks)
	blankedScript := buildSvelteBlankedScript(src, blocks)

	origNL := strings.Count(string(src), "\n")
	if got := strings.Count(string(virtualScript), "\n"); got != origNL {
		t.Errorf("virtualScript newline count %d != %d", got, origNL)
	}
	if got := strings.Count(string(blankedScript), "\n"); got != origNL {
		t.Errorf("blankedScript newline count %d != %d", got, origNL)
	}
}

// TestSplitSvelteSFC_ScriptContentPreserved verifies that the script body appears
// in virtualScript and is blanked in blankedScript.
func TestSplitSvelteSFC_ScriptContentPreserved(t *testing.T) {
	t.Parallel()
	src := []byte("<script>\nconst x = 1\n</script>\n<div>hi</div>\n")
	blocks := splitSvelteSFC(src)

	virtualScript, _ := buildSvelteVirtualScript(src, blocks)
	blankedScript := buildSvelteBlankedScript(src, blocks)

	if !strings.Contains(string(virtualScript), "x = 1") {
		t.Error("virtualScript must contain script body 'x = 1'")
	}
	if strings.Contains(string(blankedScript), "x = 1") {
		t.Error("blankedScript must NOT contain script body 'x = 1'")
	}
}

// TestSplitSvelteSFC_MarkupContentPreserved verifies that markup content appears
// in blankedScript and is blanked in virtualScript.
func TestSplitSvelteSFC_MarkupContentPreserved(t *testing.T) {
	t.Parallel()
	src := []byte("<script>\nconst x = 1\n</script>\n<div class=\"card\">hi</div>\n")
	blocks := splitSvelteSFC(src)

	virtualScript, _ := buildSvelteVirtualScript(src, blocks)
	blankedScript := buildSvelteBlankedScript(src, blocks)

	if strings.Contains(string(virtualScript), "card") {
		t.Error("virtualScript must NOT contain markup content")
	}
	if !strings.Contains(string(blankedScript), "card") {
		t.Error("blankedScript must contain markup content 'card'")
	}
}

// TestSplitSvelteSFC_ScriptLangDetected verifies that lang="ts" is detected.
func TestSplitSvelteSFC_ScriptLangDetected(t *testing.T) {
	t.Parallel()
	src := []byte("<script lang=\"ts\">\nconst x: number = 1\n</script>\n<div/>\n")
	blocks := splitSvelteSFC(src)
	_, lang := buildSvelteVirtualScript(src, blocks)
	if lang != "ts" {
		t.Errorf("expected lang=ts, got %q", lang)
	}
}

// TestSplitSvelteSFC_ContextModule verifies that <script context="module"> is
// recognised as a script block.
func TestSplitSvelteSFC_ContextModule(t *testing.T) {
	t.Parallel()
	src := []byte("<script context=\"module\">\nexport const prerender = true\n</script>\n<script>\nconst x = 1\n</script>\n<div/>\n")
	blocks := splitSvelteSFC(src)

	moduleFound := false
	for _, b := range blocks {
		if b.kind == "script module" {
			moduleFound = true
		}
	}
	if !moduleFound {
		t.Error("expected block with kind='script module' for <script context='module'>")
	}

	// virtualScript should include both script bodies.
	virtualScript, _ := buildSvelteVirtualScript(src, blocks)
	if !strings.Contains(string(virtualScript), "prerender") {
		t.Error("virtualScript must include script module body 'prerender'")
	}
	if !strings.Contains(string(virtualScript), "x = 1") {
		t.Error("virtualScript must include regular script body 'x = 1'")
	}
}

// TestSplitSvelteSFC_StyleBlankedInVirtualScript verifies that <style> blocks are
// blanked in virtualScript.
func TestSplitSvelteSFC_StyleBlankedInVirtualScript(t *testing.T) {
	t.Parallel()
	src := []byte("<script>\nconst x = 1\n</script>\n<div/>\n<style>\n.foo { color: red; }\n</style>\n")
	blocks := splitSvelteSFC(src)
	virtualScript, _ := buildSvelteVirtualScript(src, blocks)

	if strings.Contains(string(virtualScript), "color") {
		t.Error("virtualScript must NOT contain style content")
	}
}

// TestNormalizeSvelteEvent verifies Svelte event modifier stripping.
func TestNormalizeSvelteEvent(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"click", "click"},
		{"click|preventDefault", "click"},
		{"submit|stopPropagation|preventDefault", "submit"},
		{"keydown", "keydown"},
	}
	for _, c := range cases {
		got := normalizeSvelteEvent(c.in)
		if got != c.want {
			t.Errorf("normalizeSvelteEvent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
