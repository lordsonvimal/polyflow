package patterns

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

func parseTS(t *testing.T, grammar, src string) *sitter.Node {
	t.Helper()
	root, err := sitter.ParseCtx(context.Background(), []byte(src), languageFor(grammar))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return root
}

// firstOfType returns the first node of type typ in a pre-order walk.
func firstOfType(n *sitter.Node, typ string) *sitter.Node {
	if n.Type() == typ {
		return n
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if got := firstOfType(n.NamedChild(i), typ); got != nil {
			return got
		}
	}
	return nil
}

func TestExtractVerbs(t *testing.T) {
	rb := `class Api::UsersController < ApplicationController
  helper_method :current_user
  private
  def current_user
    RestClient.get("https://x/#{id}")
  end
end
`
	src := []byte(rb)
	ec := &ExtractContext{Src: src, Grammar: "ruby", File: "users_controller.rb"}
	root := parseTS(t, "ruby", rb)

	call := firstOfType(root, "call") // helper_method :current_user
	sym := firstOfType(root, "simple_symbol")
	method := firstOfType(root, "method")

	tests := []struct {
		name string
		verb string
		node *sitter.Node
		want string
	}{
		{"text", "text", sym, ":current_user"},
		{"string_value symbol", "string_value", sym, "current_user"},
		{"trailing_identifier call", "trailing_identifier", call, "helper_method"},
		{"enclosing_name class", "enclosing_name(class)", sym, "UsersController"},
		{"line", "line", method, "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runVerb(tt.verb, tt.node, ec)
			if len(got) != 1 {
				t.Fatalf("got %d values, want 1", len(got))
			}
			if got[0].atom().Value() != tt.want {
				t.Errorf("%s = %q, want %q", tt.verb, got[0].atom().Value(), tt.want)
			}
		})
	}

	t.Run("line is an int atom", func(t *testing.T) {
		got := runVerb("line", method, ec)[0]
		if got.Kind != factpipe.AtomInt {
			t.Errorf("line kind = %d, want AtomInt", got.Kind)
		}
	})

	t.Run("enclosing returns a node atom", func(t *testing.T) {
		got := runVerb("enclosing(class)", sym, ec)[0]
		if got.Kind != factpipe.AtomNode || got.Str == "" {
			t.Errorf("enclosing(class) = %+v, want non-empty node atom", got)
		}
	})

	t.Run("resolved verbs return empty without a hook", func(t *testing.T) {
		for _, v := range []string{"resolved_type", "resolved_value", "resolved_target"} {
			if got := runVerb(v, sym, ec)[0]; got.atom().Value() != "" {
				t.Errorf("%s without hook = %q, want empty", v, got.atom().Value())
			}
		}
	})
}

func TestPrecededBy(t *testing.T) {
	rb := `class C
  before_action :a
  private
  def x; end
  def y; end
end
`
	src := []byte(rb)
	ec := &ExtractContext{Src: src, Grammar: "ruby", File: "c.rb"}
	root := parseTS(t, "ruby", rb)

	query := `(identifier) @p (#eq? @p "private")`
	var methods []*sitter.Node
	var collect func(*sitter.Node)
	collect = func(n *sitter.Node) {
		if n.Type() == "method" {
			methods = append(methods, n)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collect(n.NamedChild(i))
		}
	}
	collect(root)
	if len(methods) != 2 {
		t.Fatalf("found %d methods, want 2", len(methods))
	}
	for _, m := range methods {
		if got := precededBy(m, query, ec); got != 1 {
			t.Errorf("preceded_by(private) for method below private = %d, want 1", got)
		}
	}

	call := firstOfType(root, "call") // before_action, above `private`
	if got := precededBy(call, query, ec); got != 0 {
		t.Errorf("preceded_by(private) for call above private = %d, want 0", got)
	}
}

func TestListElementsFanout(t *testing.T) {
	rb := `x = [:show, :edit, :destroy]`
	src := []byte(rb)
	ec := &ExtractContext{Src: src, Grammar: "ruby", File: "x.rb"}
	root := parseTS(t, "ruby", rb)
	arr := firstOfType(root, "array")
	got := runVerb("list_elements", arr, ec)
	if len(got) != 3 {
		t.Fatalf("list_elements = %d values, want 3", len(got))
	}
	want := []string{"show", "edit", "destroy"}
	for i, v := range got {
		if v.atom().Value() != want[i] {
			t.Errorf("element %d = %q, want %q", i, v.atom().Value(), want[i])
		}
	}
}

func TestSynthesizeNodeStable(t *testing.T) {
	rb := `before_action { authenticate_user! }`
	ec := &ExtractContext{Src: []byte(rb), Grammar: "ruby", File: "a.rb"}
	a := firstOfType(parseTS(t, "ruby", rb), "block")
	if a == nil {
		a = firstOfType(parseTS(t, "ruby", rb), "do_block")
	}
	id1 := runVerb("synthesize_node(block)", a, ec)[0].Str
	b := firstOfType(parseTS(t, "ruby", rb), a.Type())
	id2 := runVerb("synthesize_node(block)", b, ec)[0].Str
	if id1 == "" || id1 != id2 {
		t.Errorf("synthesize_node ids not stable: %q vs %q", id1, id2)
	}
}

func TestMatchToFacts_RailsFiltersShape(t *testing.T) {
	pf := &PatternFile{
		Language: "ruby",
		Patterns: []Pattern{{
			Name: "before_action",
			Query: `(call
			  method: (identifier) @m (#match? @m "^(before_action|prepend_before_action|append_before_action)$")
			  arguments: (argument_list (simple_symbol) @cb)) @call`,
			Facts: []FactSpec{
				{Pred: "filter_reg", Args: ArgList{
					{Name: "klass", Extract: "enclosing_name(class)"},
					{Name: "callback", Capture: "cb", Extract: "string_value"},
					{Name: "kind", Literal: strptr("before")},
					{Name: "private", Extract: `preceded_by((identifier) @p (#eq? @p "private"))`},
				}},
				{Pred: "reg_only", Args: ArgList{
					{Name: "callback", Capture: "cb", Extract: "string_value"},
					{Name: "action", Capture: "call", Extract: "keyword_arg(only)", Then: "list_elements"},
				}},
			},
		}},
	}
	reg := NewRegistry()
	reg.RegisterFile(pf)
	m := NewTreeSitterMatcher(reg)

	rb := `class UsersController < ApplicationController
  before_action :authenticate, only: [:show, :edit]
  private
  def authenticate; end
end
`
	results, err := m.Match("ruby", "users_controller.rb", []byte(rb))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d matches, want 1", len(results))
	}
	mr := results[0]
	if mr.CaptureNodes == nil || mr.AnchorNode == nil {
		t.Fatalf("capture nodes not retained: %+v", mr.CaptureNodes)
	}

	facts := MatchToFacts(mr, pf.Patterns[0].Facts, &ExtractContext{})

	var filterReg, regOnly []factpipe.Fact
	for _, f := range facts {
		switch f.Pred {
		case "filter_reg":
			filterReg = append(filterReg, f)
		case "reg_only":
			regOnly = append(regOnly, f)
		}
	}

	if len(filterReg) != 1 {
		t.Fatalf("filter_reg: %d facts, want 1", len(filterReg))
	}
	fr := filterReg[0]
	if fr.Args[0].Str != "UsersController" {
		t.Errorf("filter_reg klass = %q, want UsersController", fr.Args[0].Str)
	}
	if fr.Args[1].Str != "authenticate" {
		t.Errorf("filter_reg callback = %q, want authenticate", fr.Args[1].Str)
	}
	if fr.Args[2].Str != "before" {
		t.Errorf("filter_reg kind = %q, want before", fr.Args[2].Str)
	}
	if fr.Args[3].Kind != factpipe.AtomInt || fr.Args[3].Int != 0 {
		t.Errorf("filter_reg private = %+v, want int 0", fr.Args[3])
	}
	if fr.Origin.Kind != factpipe.OriginPattern || fr.Origin.Pattern != "before_action" {
		t.Errorf("filter_reg origin = %+v", fr.Origin)
	}

	if len(regOnly) != 2 {
		t.Fatalf("reg_only: %d facts, want 2", len(regOnly))
	}
	if regOnly[0].Args[1].Str != "show" || regOnly[1].Args[1].Str != "edit" {
		t.Errorf("reg_only actions = %q,%q want show,edit", regOnly[0].Args[1].Str, regOnly[1].Args[1].Str)
	}
	for _, f := range regOnly {
		if f.Args[0].Str != "authenticate" {
			t.Errorf("reg_only callback = %q, want authenticate", f.Args[0].Str)
		}
	}
}

func strptr(s string) *string { return &s }

func TestHasKeywordAndListCSV(t *testing.T) {
	rb := `class C
  before_action :auth, only: %i[show edit], if: :logged_in?
  before_action :plain
end
`
	root := parseTS(t, "ruby", rb)
	ec := &ExtractContext{Src: []byte(rb), Grammar: "ruby", File: "c.rb"}

	var withOpts, plain *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			if firstOfType(n, "hash") != nil || firstOfType(n, "pair") != nil {
				withOpts = n
			} else if plain == nil {
				plain = n
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)

	if got := runVerb("has_keyword(if,unless)", withOpts, ec)[0].atom().Value(); got != "true" {
		t.Errorf("has_keyword on if: filter = %q, want true", got)
	}
	if got := runVerb("has_keyword(if,unless)", plain, ec)[0].atom().Value(); got != "" {
		t.Errorf("has_keyword on plain filter = %q, want empty", got)
	}
	only := keywordArg(withOpts, "only", ec.Src)
	if got := listCSV(only, ec.Src); got != "show,edit" {
		t.Errorf("list_csv(only) = %q, want show,edit", got)
	}
}
