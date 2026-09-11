package patterns

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

// TestScanCallRefs is the pure-logic layer: given code text (what an ERB
// output_directive's `code` child already hands us, delimiter-free), find
// every standalone call to a named helper and read its leading literal
// sources, or the raw expression when it isn't one.
func TestScanCallRefs(t *testing.T) {
	got := scanCallRefs(` javascript_include_tag "application", "print" `,
		"javascript_include_tag,stylesheet_link_tag")
	want := []callRef{
		{helper: "javascript_include_tag", spec: "application"},
		{helper: "javascript_include_tag", spec: "print"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("scanCallRefs = %#v, want %#v", got, want)
	}
}

func TestScanCallRefsDynamic(t *testing.T) {
	got := scanCallRefs(`javascript_include_tag @asset`, "javascript_include_tag")
	if len(got) != 1 || got[0].helper != "javascript_include_tag" || got[0].spec != "@asset" {
		t.Fatalf("scanCallRefs dynamic = %#v", got)
	}
}

func TestScanCallRefsIgnoresLongerIdentifier(t *testing.T) {
	got := scanCallRefs(`custom_javascript_include_tag "x"`, "javascript_include_tag")
	if len(got) != 0 {
		t.Fatalf("scanCallRefs matched a longer identifier: %#v", got)
	}
}

// TestERBCallRefEndToEnd exercises the whole extract path: the vendored
// embedded_template grammar parses `.erb`, a pattern's `(output_directive
// (code) @code)` query finds each `<%= %>` span, comment_directive
// (`<%# %>`) is a different node type so it's never matched at all, and
// call_ref(helper,...)/call_ref(spec,...) zip into one fact per source —
// this is FX.8 resolve_path step 3, the ERB half.
func TestERBCallRefEndToEnd(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "erb_asset_ref.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
language: erb
version: "1"

patterns:
  - name: asset_ref
    query: |
      (output_directive (code) @code) @tag
    facts:
      - pred: erb_asset_ref
        args:
          file: { extract: file }
          line: { capture: tag, extract: line }
          helper: { capture: code, extract: "call_ref(helper,javascript_include_tag,stylesheet_link_tag)" }
          spec: { capture: code, extract: "call_ref(spec,javascript_include_tag,stylesheet_link_tag)" }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	pf, err := LoadFile(yamlPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	reg := NewRegistry()
	reg.RegisterFile(pf)
	m := NewTreeSitterMatcher(reg)

	src := []byte(`<%= javascript_include_tag "application" %>` +
		`<%# javascript_include_tag "dead_code" %>` +
		`<%= stylesheet_link_tag "app", "print" %>`)
	results, err := m.Match("erb", "app/views/layouts/application.html.erb", src)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}

	ec := &ExtractContext{Src: src, Grammar: "erb", File: "app/views/layouts/application.html.erb"}
	var facts []factpipe.Fact
	for _, r := range results {
		facts = append(facts, MatchToFacts(r, pf.Patterns[0].Facts, ec)...)
	}

	if len(facts) != 3 {
		t.Fatalf("got %d facts, want 3 (the comment_directive must not match): %+v", len(facts), facts)
	}
	type row struct{ helper, spec string }
	var rows []row
	for _, f := range facts {
		rows = append(rows, row{f.Args[2].Str, f.Args[3].Str})
	}
	want := []row{
		{"javascript_include_tag", "application"},
		{"stylesheet_link_tag", "app"},
		{"stylesheet_link_tag", "print"},
	}
	for _, w := range want {
		found := false
		for _, r := range rows {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing row %+v in %+v", w, rows)
		}
	}
}
