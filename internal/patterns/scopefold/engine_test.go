package scopefold

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

// This test proves the engine mechanics in isolation against a toy grammar
// with no cedar/Rails dependency (Phase 1 of docs/scope-fold-engine-plan.md).
// The DSL below ("group"/"endpoint"/"resource") is deliberately NOT Rails —
// it's parsed with the Ruby grammar only because that's a tree-sitter
// grammar already vendored, not because the shape is Ruby-specific; the same
// engine would work unchanged over an equivalent Go/JS/Python toy DSL.
const toySrc = `group "api" do
  group "v1" do
    endpoint "get", "users" do
    end
    endpoint "post", "users" do
    end
  end
end
group "legacy" do
  endpoint "get", "status" do
  end
end
resource "widgets", only: ":index,:show" do
end
`

// lineOf returns the 1-based line number of needle's first occurrence.
func lineOf(t *testing.T, src, needle string) int {
	t.Helper()
	idx := strings.Index(src, needle)
	if idx < 0 {
		t.Fatalf("fixture missing %q", needle)
	}
	return strings.Count(src[:idx], "\n") + 1
}

func parseToy(t *testing.T) *sitter.Node {
	t.Helper()
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(toySrc))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}
	return tree.RootNode()
}

func testGrammar() *Grammar {
	return &Grammar{
		Stacks: []string{"path", "module"},
		Scopes: []ScopeSpec{
			{
				Match:   "group_scope",
				Recurse: "block",
				Contributes: map[string][]Contribution{
					"path": {{Capture: "seg", Extract: "segment"}},
				},
			},
			{
				Match:   "resource_scope",
				Recurse: "block",
				Contributes: map[string][]Contribution{
					"path": {{Capture: "seg", Extract: "segment"}},
				},
				Expand: "widget_actions",
			},
		},
		Leaves: []LeafSpec{
			{
				Match: "endpoint_leaf",
				Emit: EmitSpec{
					Pred: "route_composed",
					Args: []EmitArg{
						{Stack: "path", Compose: "join_segments", AppendCapture: "path", AppendExtract: "segment"},
						{Capture: "method", Extract: "segment|upcase"},
					},
				},
			},
		},
		ExpandTables: map[string]ExpandTable{
			"widget_actions": {
				Emit: EmitSpec{
					Pred: "widget_route",
					Args: []EmitArg{
						{Stack: "path", Compose: "join_segments"},
						{Row: "method"},
					},
				},
				FilterKeywords: []string{"only", "except"},
				Rows: []ExpandRow{
					{Name: "index", Method: "GET"},
					{Name: "show", Method: "GET", Member: true},
					{Name: "destroy", Method: "DELETE", Member: true},
				},
			},
		},
	}
}

func toyMatches(t *testing.T) []Match {
	return []Match{
		{PatternName: "group_scope", Line: lineOf(t, toySrc, `group "api" do`), Captures: map[string]string{"seg": `"api"`}},
		{PatternName: "group_scope", Line: lineOf(t, toySrc, `group "v1" do`), Captures: map[string]string{"seg": `"v1"`}},
		{PatternName: "endpoint_leaf", Line: lineOf(t, toySrc, `endpoint "get", "users" do`), Captures: map[string]string{"method": `"get"`, "path": `"users"`}},
		{PatternName: "endpoint_leaf", Line: lineOf(t, toySrc, `endpoint "post", "users" do`), Captures: map[string]string{"method": `"post"`, "path": `"users"`}},
		{PatternName: "group_scope", Line: lineOf(t, toySrc, `group "legacy" do`), Captures: map[string]string{"seg": `"legacy"`}},
		{PatternName: "endpoint_leaf", Line: lineOf(t, toySrc, `endpoint "get", "status" do`), Captures: map[string]string{"method": `"get"`, "path": `"status"`}},
		{PatternName: "resource_scope", Line: lineOf(t, toySrc, `resource "widgets", only: ":index,:show" do`), Captures: map[string]string{"seg": `"widgets"`, "only": `:index,:show`}},
	}
}

func factStrs(f factpipe.Fact) []string {
	out := make([]string, len(f.Args))
	for i, a := range f.Args {
		out[i] = a.Value()
	}
	return out
}

func findFacts(facts []factpipe.Fact, pred string) []factpipe.Fact {
	var out []factpipe.Fact
	for _, f := range facts {
		if f.Pred == pred {
			out = append(out, f)
		}
	}
	return out
}

// TestFold_NestedScopeAccumulation proves the core recursion: path segments
// accumulate through two levels of nesting and compose correctly at each
// leaf, method text is stripped of quotes and upcased via the "|" verb
// chain.
func TestFold_NestedScopeAccumulation(t *testing.T) {
	root := parseToy(t)
	facts := Fold(root, toyMatches(t), testGrammar())

	routes := findFacts(facts, "route_composed")
	if len(routes) != 3 {
		t.Fatalf("route_composed count = %d, want 3: %+v", len(routes), routes)
	}

	want := map[string]bool{
		"/api/v1/users GET":  true,
		"/api/v1/users POST": true,
		"/legacy/status GET": true,
	}
	got := map[string]bool{}
	for _, r := range routes {
		vals := factStrs(r)
		got[vals[0]+" "+vals[1]] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing composed route %q, got %v", k, got)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected composed route %q", k)
		}
	}
}

// TestFold_SiblingScopesDoNotAlias proves the copy-on-write stack push: the
// "legacy" sibling scope must not see "api"/"v1"'s prefix (the exact
// aliasing bug internal/parser/ruby_route_paths.go's appendSeg comment
// documents, generalized to N stacks here).
func TestFold_SiblingScopesDoNotAlias(t *testing.T) {
	root := parseToy(t)
	facts := Fold(root, toyMatches(t), testGrammar())

	for _, r := range findFacts(facts, "route_composed") {
		vals := factStrs(r)
		if strings.HasPrefix(vals[0], "/legacy") && strings.Contains(vals[0], "/api") {
			t.Fatalf("sibling scope aliased another scope's prefix: %v", vals)
		}
	}
}

// TestFold_ExpandTableFiltering proves table-driven implicit-construct
// synthesis: the "widgets" resource's only: ":index,:show" keeps exactly
// those two rows (generalizing emitRESTRoutes' only:/except: filter off
// grammar data instead of a hardcoded Go table).
func TestFold_ExpandTableFiltering(t *testing.T) {
	root := parseToy(t)
	facts := Fold(root, toyMatches(t), testGrammar())

	widgets := findFacts(facts, "widget_route")
	if len(widgets) != 2 {
		t.Fatalf("widget_route count = %d, want 2 (only: index,show): %+v", len(widgets), widgets)
	}
	want := map[string]bool{"/widgets GET": true, "/widgets/* GET": true}
	got := map[string]bool{}
	for _, w := range widgets {
		vals := factStrs(w)
		got[vals[0]+" "+vals[1]] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing expand-table route %q, got %v", k, got)
		}
	}
	for _, w := range widgets {
		vals := factStrs(w)
		if vals[1] == "DELETE" {
			t.Fatalf("destroy row should have been filtered by only:, got %v", vals)
		}
	}
}
