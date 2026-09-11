package patterns

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	jssitter "github.com/smacker/go-tree-sitter/javascript"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

func parseJS(t *testing.T, src string) *sitter.Node {
	t.Helper()
	root, err := sitter.ParseCtx(context.Background(), []byte(src), jssitter.GetLanguage())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	return root
}

// TestScanHeaderDirectives is the pure-logic layer: given a parsed root and
// the allow-listed directive verbs, read only the `= verb path` lines inside
// the file's leading `//` comment run.
func TestScanHeaderDirectives(t *testing.T) {
	src := "//= require jquery\n//= require_tree .\nconsole.log(1)\n//= require dead\n"
	root := parseJS(t, src)
	got := scanHeaderDirectives(root, "require,require_tree,require_directory", []byte(src))
	want := []headerDirective{
		{verb: "require", path: "jquery", line: 1},
		{verb: "require_tree", path: ".", line: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("scanHeaderDirectives = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// TestScanHeaderDirectivesBlockComment covers the `/*= ... */` CSS spelling
// (a single multi-line `comment` node), including the `*`-per-line scaffold
// style and the `.css` extension filter argument of `link_directory`.
func TestScanHeaderDirectivesBlockComment(t *testing.T) {
	src := "/*\n *= require reset\n *= link_directory ../fonts .css\n */\nbody {}\n"
	root := parseJS(t, src) // the JS grammar's block-comment node is enough to test the scanner
	got := scanHeaderDirectives(root, "require,link_directory", []byte(src))
	want := []headerDirective{
		{verb: "require", path: "reset", line: 2},
		{verb: "link_directory", path: "../fonts", ext: ".css", line: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("scanHeaderDirectives = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// TestScanHeaderDirectivesStopsAtCode proves the positional rule: a `//=`
// line is a directive only inside the file's *leading* comment run — one
// after the first real statement is body text, not a header.
func TestScanHeaderDirectivesStopsAtCode(t *testing.T) {
	src := "console.log(1)\n//= require late\n"
	root := parseJS(t, src)
	got := scanHeaderDirectives(root, "require", []byte(src))
	if len(got) != 0 {
		t.Fatalf("scanHeaderDirectives matched a directive after code: %#v", got)
	}
}

// TestScanHeaderDirectivesUnknownVerb proves `stub`/`require_self` (present
// but deliberately excluded from the allow-list a caller passes) are not
// matched — the verb is entirely caller-driven, no Sprockets knowledge is
// hardcoded into the primitive itself.
func TestScanHeaderDirectivesUnknownVerb(t *testing.T) {
	src := "//= require_self\n//= stub other\n"
	root := parseJS(t, src)
	got := scanHeaderDirectives(root, "require", []byte(src))
	if len(got) != 0 {
		t.Fatalf("scanHeaderDirectives matched an unlisted verb: %#v", got)
	}
}

// TestHeaderDirectiveEndToEnd exercises the whole extract path: a pattern's
// `(program) @root` query captures the file root once, and
// header_directive(verb,...)/header_directive(path,...) zip into one fact per
// directive line — FX.8 resolve_path step 3b, the JS/CSS half of step 3.
func TestHeaderDirectiveEndToEnd(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "header_directive.yaml")
	if err := os.WriteFile(yamlPath, []byte(`
language: javascript
version: "1"

patterns:
  - name: header_directive
    query: |
      (program) @root
    facts:
      - pred: js_header_directive
        args:
          file: { extract: file }
          line: { capture: root, extract: "header_directive(line,require,require_tree)" }
          verb: { capture: root, extract: "header_directive(verb,require,require_tree)" }
          path: { capture: root, extract: "header_directive(path,require,require_tree)" }
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

	src := []byte("//= require jquery\n//= require_tree ./components\nconsole.log(1)\n//= require dead\n")
	results, err := m.Match("javascript", "app/assets/javascripts/application.js", src)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}

	ec := &ExtractContext{Src: src, Grammar: "javascript", File: "app/assets/javascripts/application.js"}
	var facts []factpipe.Fact
	for _, r := range results {
		facts = append(facts, MatchToFacts(r, pf.Patterns[0].Facts, ec)...)
	}

	if len(facts) != 2 {
		t.Fatalf("got %d facts, want 2 (the post-code directive must not match): %+v", len(facts), facts)
	}
	type row struct {
		line int64
		verb string
		path string
	}
	var rows []row
	for _, f := range facts {
		rows = append(rows, row{f.Args[1].Int, f.Args[2].Str, f.Args[3].Str})
	}
	want := []row{
		{1, "require", "jquery"},
		{2, "require_tree", "./components"},
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
