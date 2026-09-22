package scopefold

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
)

// This file proves the primitives added for Phase 2 (Resets, Compose: "top",
// EmitArg.Fallback, Contribution.Literal, the slash-split verbs, and
// inflect:collection_name) against toy grammars, before any of them are used
// against a real Rails grammar. Each was added because a real
// internal/parser/ruby_route_paths.go shape needed it — see
// docs/scope-fold-engine-plan.md — but every test here stays framework-free,
// same discipline as engine_test.go.

// TestFold_ResetClearsStack proves Resets: a scope declaring
// resets: [name] starts that stack's accumulation over, rather than
// inheriting the parent's value — Rails' namespace/scope ending the
// enclosing resource's naming claim on "singular"/"onScope" without
// touching its "path"/"module" claim.
func TestFold_ResetClearsStack(t *testing.T) {
	src := `outer do
  flagged do
    inner do
      leaf "x" do
      end
    end
  end
end
`
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}

	g := &Grammar{
		Stacks: []string{"flag"},
		Scopes: []ScopeSpec{
			{Match: "outer_scope", Recurse: "block"},
			{
				Match:   "flagged_scope",
				Recurse: "block",
				Contributes: map[string]Contribution{
					"flag": {Literal: "on"},
				},
			},
			{
				// inner resets flag before recursing — the leaf beneath it
				// must not see "on" even though flagged_scope is still a
				// lexical ancestor.
				Match:   "inner_scope",
				Recurse: "block",
				Resets:  []string{"flag"},
			},
		},
		Leaves: []LeafSpec{
			{
				Match: "leaf_route",
				Emit: EmitSpec{
					Pred: "flag_seen",
					Args: []EmitArg{{Stack: "flag", Compose: "top"}},
				},
			},
		},
	}

	matches := []Match{
		{PatternName: "outer_scope", Line: lineOf(t, src, "outer do")},
		{PatternName: "flagged_scope", Line: lineOf(t, src, "flagged do")},
		{PatternName: "inner_scope", Line: lineOf(t, src, "inner do")},
		{PatternName: "leaf_route", Line: lineOf(t, src, `leaf "x" do`)},
	}

	facts := Fold(tree.RootNode(), matches, g)
	seen := findFacts(facts, "flag_seen")
	if len(seen) != 1 {
		t.Fatalf("flag_seen count = %d, want 1: %+v", len(seen), seen)
	}
	if got := factStrs(seen[0])[0]; got != "" {
		t.Fatalf("flag_seen = %q, want empty (reset should have cleared it)", got)
	}
}

// TestFold_FallbackAndTop proves EmitArg.Fallback: a leaf's own keyword
// capture wins when present, and only falls back to a lexically pushed
// stack's top when it's empty — Rails' on: keyword-or-position duality for
// member/collection routes.
func TestFold_FallbackAndTop(t *testing.T) {
	src := `member_wrap do
  leaf "a", on: "explicit" do
  end
  leaf "b" do
  end
end
`
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}

	g := &Grammar{
		Stacks: []string{"on_scope"},
		Scopes: []ScopeSpec{
			{
				Match:   "member_scope",
				Recurse: "block",
				Contributes: map[string]Contribution{
					"on_scope": {Literal: "member"},
				},
			},
		},
		Leaves: []LeafSpec{
			{
				Match: "leaf_route",
				Emit: EmitSpec{
					Pred: "on_resolved",
					Args: []EmitArg{
						{
							Capture: "on",
							Extract: "segment",
							Fallback: &EmitArg{
								Stack:   "on_scope",
								Compose: "top",
							},
						},
					},
				},
			},
		},
	}

	matches := []Match{
		{PatternName: "member_scope", Line: lineOf(t, src, "member_wrap do")},
		{PatternName: "leaf_route", Line: lineOf(t, src, `leaf "a", on: "explicit" do`), Captures: map[string]string{"on": `"explicit"`}},
		{PatternName: "leaf_route", Line: lineOf(t, src, `leaf "b" do`), Captures: map[string]string{}},
	}

	facts := Fold(tree.RootNode(), matches, g)
	resolved := findFacts(facts, "on_resolved")
	if len(resolved) != 2 {
		t.Fatalf("on_resolved count = %d, want 2: %+v", len(resolved), resolved)
	}
	got := map[int]string{}
	for i, r := range resolved {
		got[i] = factStrs(r)[0]
	}
	if got[0] != "explicit" {
		t.Errorf("route a: on = %q, want %q (own keyword must win)", got[0], "explicit")
	}
	if got[1] != "member" {
		t.Errorf("route b: on = %q, want %q (must fall back to lexical scope)", got[1], "member")
	}
}

// TestVerbs_SlashAndCollisionName proves the generic string verbs added for
// Rails' controller:/to:/devise controllers: overrides, and the
// ActionDispatch collision_name rule.
func TestVerbs_SlashAndCollisionName(t *testing.T) {
	cases := []struct {
		verb, in, want string
	}{
		{"before_last_slash", "users/sessions", "users"},
		{"before_last_slash", "sessions", ""},
		{"after_last_slash", "users/sessions", "sessions"},
		{"after_last_slash", "sessions", "sessions"},
		{"before_hash", "sessions#create", "sessions"},
		{"after_hash", "sessions#create", "create"},
		{"inflect:collection_name", "studies", "studies"},
		{"inflect:collection_name", "sso", "sso_index"},
	}
	for _, c := range cases {
		if got := applyOneVerb(c.verb, c.in); got != c.want {
			t.Errorf("applyOneVerb(%q, %q) = %q, want %q", c.verb, c.in, got, c.want)
		}
	}
}
