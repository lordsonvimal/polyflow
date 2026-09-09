package valuegraph

import (
	"context"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
)

func TestEmbeddedSpecRoundTrips(t *testing.T) {
	for _, lang := range []string{"javascript", "typescript", "tsx"} {
		s, err := EmbeddedSpec(lang)
		if err != nil {
			t.Fatalf("EmbeddedSpec(%q): %v", lang, err)
		}
		if s.Language != "javascript" {
			t.Fatalf("EmbeddedSpec(%q).Language = %q", lang, s.Language)
		}
		if len(s.Literals) == 0 || len(s.Bindings) == 0 || len(s.Scopes) == 0 {
			t.Fatalf("EmbeddedSpec(%q) is missing rule sections: %+v", lang, s)
		}
	}
	if _, err := EmbeddedSpec("go"); err == nil {
		t.Fatalf("EmbeddedSpec(\"go\") should fail — there is no embedded Go spec")
	}
}

func TestLoadSpecRejectsUnknownTopLevelKey(t *testing.T) {
	_, err := LoadSpec([]byte("language: x\nbinding:\n  - node: foo\n"))
	if err == nil {
		t.Fatal("a misspelled top-level key must be an error, not a silent no-op")
	}
}

func TestLoadSpecRejectsIncompleteBinding(t *testing.T) {
	_, err := LoadSpec([]byte("language: x\nbindings:\n  - node: variable_declarator\n    name: name\n"))
	if err == nil {
		t.Fatal("a binding with no value must be rejected")
	}
	_, err = LoadSpec([]byte("language: x\nbindings:\n  - node: variable_declarator\n    value: value\n"))
	if err == nil {
		t.Fatal("a binding with no name must be rejected")
	}
}

// The single-unparenthesised-parameter arrow has no formal_parameters node —
// the identifier hangs off the `parameter` field. Without params_alt the engine
// would miss that createUrl is a parameter and resolve it to an outer binding.
// This has bitten before; a regression would otherwise be invisible.
func TestSingleParamArrowResolvesThroughParamsAlt(t *testing.T) {
	spec, err := EmbeddedSpec("tsx")
	if err != nil {
		t.Fatal(err)
	}

	src := []byte(`
const createUrl = "/outer/wrong";
const g = createUrl => go(createUrl);
`)
	root, err := sitter.ParseCtx(context.Background(), src, tsxsitter.GetLanguage())
	if err != nil {
		t.Fatal(err)
	}
	expr := findJSProbeArg(root, src)
	if expr == nil {
		t.Fatal("fixture has no go(...) probe call")
	}

	e := New(spec, nil, Options{})
	v := e.Resolve(Query{File: "probe.tsx", Src: src, Root: root, Expr: expr})

	origins := v.Origins()
	if len(origins) != 1 || origins[0].Reason != ReasonParam {
		t.Fatalf("createUrl is a parameter — want a single Opaque(param), got %s with %+v", v.String(), origins)
	}
}

func findJSProbeArg(n *sitter.Node, src []byte) *sitter.Node {
	var found *sitter.Node
	var walk func(*sitter.Node)
	walk = func(cur *sitter.Node) {
		if cur == nil || found != nil {
			return
		}
		if cur.Type() == "call_expression" {
			fn := cur.ChildByFieldName("function")
			args := cur.ChildByFieldName("arguments")
			if fn != nil && fn.Content(src) == "go" && args != nil && args.NamedChildCount() == 1 {
				found = args.NamedChild(0)
				return
			}
		}
		for i := 0; i < int(cur.ChildCount()); i++ {
			walk(cur.Child(i))
		}
	}
	walk(n)
	return found
}

func TestJavaScriptSpecUnder120Lines(t *testing.T) {
	raw, err := specFS.ReadFile("javascript.yaml")
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(string(raw), "\n")
	if n > 120 {
		t.Fatalf("javascript.yaml is %d lines; the spec vocabulary has absorbed idiom-specific knowledge — re-partition", n)
	}
}
