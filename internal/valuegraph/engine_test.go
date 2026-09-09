package valuegraph

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	pythonsitter "github.com/smacker/go-tree-sitter/python"
)

// The engine is language-agnostic, so its tests must be too. They drive it with
// a spec for a language none of the tier's passes use: if a rule the engine
// needs had leaked into Go instead of the spec, these tests would be the ones
// to fail.

func testSpec() *Spec {
	i0, i1 := 0, 1
	return &Spec{
		Language: "probe",
		Literals: []LiteralRule{{Node: "string", Text: TextTemplate}},
		Concat:   []ConcatRule{{Node: "binary_operator", Operator: "+", Parts: []string{"left", "right"}}},
		Union:    []UnionRule{{Node: "list"}},
		Bindings: []BindingRule{
			{Node: "assignment", Name: "left", Value: "right"},
			{Node: "pair", NameChild: &i0, ValueChild: &i1},
		},
		Scopes: []ScopeRule{{Node: "function_definition", Params: "parameters"}},
		Opaque: []OpaqueRule{
			{Node: "call"},
			{Node: "attribute", Reason: ReasonMember},
		},
	}
}

// resolveProbe parses src, finds the single argument of the trailing `go(...)`
// call, and resolves it.
func resolveProbe(t *testing.T, spec *Spec, opts Options, src string) Value {
	t.Helper()
	root, err := sitter.ParseCtx(context.Background(), []byte(src), pythonsitter.GetLanguage())
	if err != nil || root == nil {
		t.Fatalf("parse: %v", err)
	}
	expr := findProbeArg(root, []byte(src))
	if expr == nil {
		t.Fatalf("fixture has no go(...) probe call:\n%s", src)
	}
	e := New(spec, nil, opts)
	return e.Resolve(Query{File: "probe.py", Src: []byte(src), Root: root, Expr: expr})
}

func findProbeArg(n *sitter.Node, src []byte) *sitter.Node {
	var found *sitter.Node
	var walk func(*sitter.Node)
	walk = func(cur *sitter.Node) {
		if cur == nil {
			return
		}
		if cur.Type() == "call" {
			if fn := cur.ChildByFieldName("function"); fn != nil && fn.Content(src) == "go" {
				if args := cur.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() == 1 {
					found = args.NamedChild(0)
				}
			}
		}
		for i := 0; i < int(cur.ChildCount()); i++ {
			walk(cur.Child(i))
		}
	}
	walk(n)
	return found
}

func wantStrings(t *testing.T, v Value, ok bool, want ...string) {
	t.Helper()
	got, gotOK := v.Strings(0)
	if !reflect.DeepEqual(got, want) || gotOK != ok {
		t.Fatalf("Strings() = %q, %v; want %q, %v (value %s)", got, gotOK, want, ok, v.String())
	}
}

func wantOpaque(t *testing.T, v Value, reason string) {
	t.Helper()
	origins := v.Origins()
	if len(origins) != 1 || origins[0].Reason != reason {
		t.Fatalf("want a single Opaque(%s), got %s with origins %+v", reason, v.String(), origins)
	}
	if origins[0].File != "probe.py" || origins[0].Line == 0 {
		t.Fatalf("an Origin must locate itself, got %+v", origins[0])
	}
}

func TestResolveLiteralAndConcat(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    base = "/api/v1"
    url = base + "/games"
    go(url)
`)
	wantStrings(t, v, true, "/api/v1/games")
}

func TestResolveInterpolation(t *testing.T) {
	// The hole is a parameter, so the segment is unresolved and renders as the
	// wildcard the route normalizers already consume.
	v := resolveProbe(t, testSpec(), Options{}, `
def f(kind):
    base = "/api/v1"
    url = f"{base}/games/{kind}"
    go(url)
`)
	wantStrings(t, v, true, "/api/v1/games/*")
	if len(v.Origins()) != 1 || v.Origins()[0].Reason != ReasonParam {
		t.Fatalf("the hole should stop with ReasonParam, got %+v", v.Origins())
	}
}

func TestResolveBranchesToAUnion(t *testing.T) {
	// Two writes in two arms are not an ambiguity: they are two real requests
	// written at one site. Both must survive.
	v := resolveProbe(t, testSpec(), Options{}, `
def f(kind):
    url = "/api/a"
    if kind:
        url = "/api/b"
    go(url)
`)
	wantStrings(t, v, true, "/api/a", "/api/b")
}

func TestResolveWalksOutwardToModuleScope(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{}, `
base = "/api/v1"

def f():
    go(base)
`)
	wantStrings(t, v, true, "/api/v1")
}

func TestBindingInANestedScopeIsNotThisCallsBinding(t *testing.T) {
	// The callback's own `url` must not resolve this call's.
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    def cb():
        url = "/not/mine"
    go(url)
`)
	wantOpaque(t, v, ReasonNoBinding)
}

func TestBindingAfterTheUseIsNotVisible(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    go(url)
    url = "/api/later"
`)
	wantOpaque(t, v, ReasonNoBinding)
}

func TestSpecDeclaredStops(t *testing.T) {
	spec := testSpec()
	t.Run("call", func(t *testing.T) {
		wantOpaque(t, resolveProbe(t, spec, Options{}, `
def f():
    url = fetch_url()
    go(url)
`), ReasonCall)
	})
	t.Run("member reason overrides the derived one", func(t *testing.T) {
		wantOpaque(t, resolveProbe(t, spec, Options{}, `
def f(self):
    url = self.props
    go(url)
`), ReasonMember)
	})
	t.Run("parameter", func(t *testing.T) {
		wantOpaque(t, resolveProbe(t, spec, Options{}, `
def f(url):
    go(url)
`), ReasonParam)
	})
}

func TestSelfReferentialBindingTerminates(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    a = b
    b = a
    go(b)
`)
	wantOpaque(t, v, ReasonCycle)
}

func TestDepthCap(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{MaxDepth: 2}, `
def f():
    a = "/api"
    b = a
    c = b
    d = c
    go(d)
`)
	wantOpaque(t, v, ReasonDepth)
}

func TestUnionWidthCap(t *testing.T) {
	src := `
def f(kind):
    url = "/api/a"
    if kind:
        url = "/api/b"
    go(url)
`
	if v := resolveProbe(t, testSpec(), Options{MaxUnionWidth: 2}, src); v.Kind != KindUnion {
		t.Fatalf("two alternatives are within a width of 2, got %s", v.String())
	}
	wantOpaque(t, resolveProbe(t, testSpec(), Options{MaxUnionWidth: 1}, src), ReasonWidth)
}

// stubSource is a FileSource that never parses anything; the engine must treat
// that as Opaque rather than as fatal, and must charge the file budget for the
// attempt.
type stubSource struct {
	files  []string
	opened []string
}

func (s *stubSource) Files() []string { return s.files }

func (s *stubSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	s.opened = append(s.opened, file)
	return nil, nil, false
}

func TestFileCap(t *testing.T) {
	fs := &stubSource{files: []string{"a.py", "b.py"}}
	e := New(testSpec(), fs, Options{MaxFiles: 1})
	c := &ctx{e: e, file: "a.py", opened: map[string]parsedFile{}}

	if _, ok, capped := c.open("a.py"); ok || capped {
		t.Fatalf("an unparseable file is not a cap breach: ok=%v capped=%v", ok, capped)
	}
	if _, _, capped := c.open("a.py"); capped {
		t.Fatal("re-opening the same file must not charge the budget twice")
	}
	if _, _, capped := c.open("b.py"); !capped {
		t.Fatal("the second distinct file exceeds MaxFiles=1 and must report the cap")
	}
	if len(fs.opened) != 1 {
		t.Fatalf("the capped open must not reach the FileSource, got %v", fs.opened)
	}

	// End to end: a Query with no Src asks the FileSource, and a cap or a
	// failure there is an Origin, not a panic and not an empty result.
	root, err := sitter.ParseCtx(context.Background(), []byte("go(url)\n"), pythonsitter.GetLanguage())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := e.Resolve(Query{File: "a.py", Expr: findProbeArg(root, []byte("go(url)\n"))})
	if v.Kind != KindOpaque || v.Origin.Reason != ReasonUnsupported {
		t.Fatalf("an unreadable file resolves to Opaque(unsupported), got %s", v.String())
	}
}

func TestResolveNeverReturnsAZeroValueOrPanics(t *testing.T) {
	e := New(testSpec(), nil, Options{})
	if v := e.Resolve(Query{File: "probe.py"}); v.Kind != KindOpaque {
		t.Fatalf("a query with no expression is Opaque, got %+v", v)
	}
	// A spec that claims nothing must not resolve anything either — and must
	// not crash trying.
	v := resolveProbe(t, &Spec{Language: "empty"}, Options{}, `
def f():
    url = "/api/a"
    go(url)
`)
	if v.Kind != KindOpaque {
		t.Fatalf("an empty spec resolves nothing, got %s", v.String())
	}
}

func TestBindingByNamedChildIndex(t *testing.T) {
	// Some grammars expose no fields on the node that matters, so a rule may
	// address named children by index instead.
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    opts = {"url": "/api/from/pair"}
    go(url)
`)
	wantStrings(t, v, true, "/api/from/pair")
}

func TestUnionRuleNode(t *testing.T) {
	v := resolveProbe(t, testSpec(), Options{}, `
def f():
    go(["/api/a", "/api/b"])
`)
	wantStrings(t, v, true, "/api/a", "/api/b")
}

func TestInnerTextMode(t *testing.T) {
	spec := testSpec()
	spec.Literals = []LiteralRule{{Node: "string", Text: TextInner}}
	v := resolveProbe(t, spec, Options{}, `
def f():
    go("/api/inner")
`)
	wantStrings(t, v, true, "/api/inner")
}

func TestSpecValidate(t *testing.T) {
	if err := testSpec().Validate(); err != nil {
		t.Fatalf("the probe spec must be valid: %v", err)
	}
	cases := map[string]*Spec{
		"no language":       {Literals: []LiteralRule{{Node: "string", Text: TextInner}}},
		"unknown text mode": {Language: "probe", Literals: []LiteralRule{{Node: "string", Text: "sideways"}}},
		"binding with no name": {Language: "probe", Bindings: []BindingRule{
			{Node: "assignment", Value: "right"},
		}},
		"binding with no value": {Language: "probe", Bindings: []BindingRule{
			{Node: "assignment", Name: "left"},
		}},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if err := (*Spec)(nil).Validate(); err == nil {
		t.Fatal("a nil spec is an error, not a valid one")
	}
}

func TestDerivedOpaqueReason(t *testing.T) {
	cases := map[string]string{
		"call_expression":      ReasonCall,
		"member_expression":    ReasonMember,
		"subscript_expression": "subscript",
		"attribute":            "attribute",
		"":                     ReasonUnsupported,
	}
	for node, want := range cases {
		if got := derivedReason(node); got != want {
			t.Errorf("derivedReason(%q) = %q; want %q", node, got, want)
		}
	}
}

// The engine may not know the language of the tier that motivated it. A grammar
// name or one of its node types appearing here would mean a fact that belongs
// in a binding spec had been written into Go instead — which is the failure the
// whole pilot exists to test for.
func TestNoLanguageKnowledgeInTheEngine(t *testing.T) {
	banned := []string{
		"javascript", "typescript", "jsx", "tsx",
		"template_string", "variable_declarator", "arrow_function", "jsx_attribute",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lower := strings.ToLower(string(body))
		for _, b := range banned {
			if strings.Contains(lower, b) {
				t.Errorf("%s mentions %q; that belongs in a binding spec, not in the engine", name, b)
			}
		}
	}
}
