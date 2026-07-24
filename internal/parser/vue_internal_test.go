package parser

import (
	"strings"
	"testing"
)

// TestSplitSFC_PreservesLineNumbers verifies that byte offsets are preserved
// after splitting: newline count must be identical to the original source.
func TestSplitSFC_PreservesLineNumbers(t *testing.T) {
	src := []byte("<template>\n  <div>{{ msg }}</div>\n</template>\n\n<script>\nexport default { name: 'A' }\n</script>\n")
	blocks := splitSFC(src)

	virtualScript, _ := buildVirtualScript(src, blocks)
	blankedScript := buildBlankedScript(src, blocks)

	origNL := strings.Count(string(src), "\n")
	if got := strings.Count(string(virtualScript), "\n"); got != origNL {
		t.Errorf("virtualScript newline count %d != %d", got, origNL)
	}
	if got := strings.Count(string(blankedScript), "\n"); got != origNL {
		t.Errorf("blankedScript newline count %d != %d", got, origNL)
	}
}

// TestSplitSFC_ScriptContentPreserved verifies that the script body appears
// in virtualScript and is blanked in blankedScript.
func TestSplitSFC_ScriptContentPreserved(t *testing.T) {
	src := []byte("<template>\n  <div>hi</div>\n</template>\n<script>\nconst x = 1\n</script>\n")
	blocks := splitSFC(src)

	virtualScript, _ := buildVirtualScript(src, blocks)
	blankedScript := buildBlankedScript(src, blocks)

	if !strings.Contains(string(virtualScript), "x = 1") {
		t.Error("virtualScript must contain script body 'x = 1'")
	}
	if strings.Contains(string(blankedScript), "x = 1") {
		t.Error("blankedScript must NOT contain script body 'x = 1'")
	}
}

// TestSplitSFC_TemplateContentPreserved verifies that the template body
// appears in blankedScript and is blanked in virtualScript.
func TestSplitSFC_TemplateContentPreserved(t *testing.T) {
	src := []byte("<template>\n  <div class=\"card\">hi</div>\n</template>\n<script>\nconst x = 1\n</script>\n")
	blocks := splitSFC(src)

	virtualScript, _ := buildVirtualScript(src, blocks)
	blankedScript := buildBlankedScript(src, blocks)

	if strings.Contains(string(virtualScript), "card") {
		t.Error("virtualScript must NOT contain template content")
	}
	if !strings.Contains(string(blankedScript), "card") {
		t.Error("blankedScript must contain template content 'card'")
	}
}

// TestSplitSFC_ScriptLangDetected verifies that lang="ts" is detected.
func TestSplitSFC_ScriptLangDetected(t *testing.T) {
	src := []byte("<template><div/></template>\n<script lang=\"ts\">\nconst x: number = 1\n</script>\n")
	blocks := splitSFC(src)
	_, lang := buildVirtualScript(src, blocks)
	if lang != "ts" {
		t.Errorf("expected lang=ts, got %q", lang)
	}
}

// TestSplitSFC_SetupScript verifies that <script setup> is recognised.
func TestSplitSFC_SetupScript(t *testing.T) {
	src := []byte("<template><div/></template>\n<script setup>\nconst emit = defineEmits()\n</script>\n")
	blocks := splitSFC(src)
	if len(blocks) < 2 {
		t.Fatalf("expected 2 blocks (template + script setup), got %d", len(blocks))
	}
	found := false
	for _, b := range blocks {
		if b.kind == "script setup" {
			found = true
		}
	}
	if !found {
		t.Error("expected block with kind='script setup'")
	}
}

// TestSplitSFC_TemplateInsideScriptString verifies that a <template> inside a
// script string literal does NOT create a new SFC block (rule: column 0 only).
func TestSplitSFC_TemplateInsideScriptString(t *testing.T) {
	src := []byte("<template>\n  <div/>\n</template>\n<script>\nconst s = \"Use <template> in docs\"\n</script>\n")
	blocks := splitSFC(src)

	templateCount := 0
	for _, b := range blocks {
		if b.kind == "template" {
			templateCount++
		}
	}
	// Only the top-level <template> at column 0 should be recognised.
	if templateCount != 1 {
		t.Errorf("expected 1 template block, got %d", templateCount)
	}
}

// TestNormalizeVueEvent verifies Vue event normalisation.
func TestNormalizeVueEvent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"click", "click"},
		{"submit.prevent", "submit"},
		{"submit.stop.prevent", "submit"},
		{"keydown.enter", "keydown"},
	}
	for _, c := range cases {
		got := normalizeVueEvent(c.in)
		if got != c.want {
			t.Errorf("normalizeVueEvent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStripCallArgs verifies handler expression stripping.
func TestStripCallArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"save", "save"},
		{"save(x)", "save"},
		{"onSubmit(event)", "onSubmit"},
		{"  doThing(a, b) ", "doThing"},
	}
	for _, c := range cases {
		got := stripCallArgs(c.in)
		if got != c.want {
			t.Errorf("stripCallArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
