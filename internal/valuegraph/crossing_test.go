package valuegraph

import (
	"context"
	"sort"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
)

// A crossing is the one rule shape the engine cannot exercise against a
// synthetic grammar the way engine_test.go does: it needs a real tree with an
// attribute, an element tag and a call site. These tests use the embedded spec
// and a real tsx parse, and they assert the two directions separately — the
// whole reason the shipped passes were worth distinguishing is that a suspect
// edge has to be attributable to one rule or the other.

// fakeSource is a FileSource over in-memory sources, plus the owner map a
// crossing needs. The linker's adapter supplies the same two facts from the
// graph; here they are written by hand.
type fakeSource struct {
	src    map[string]string
	owners map[string][]string // file → owners defined in it
}

func (f *fakeSource) Files() []string {
	out := make([]string, 0, len(f.src))
	for k := range f.src {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *fakeSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	text, ok := f.src[file]
	if !ok {
		return nil, nil, false
	}
	src := []byte(text)
	root, err := sitter.ParseCtx(context.Background(), src, tsxsitter.GetLanguage())
	if err != nil || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

func (f *fakeSource) OwnersIn(file string) []string { return f.owners[file] }

func (f *fakeSource) FilesForOwner(owner string) []string {
	var out []string
	for file, owners := range f.owners {
		for _, o := range owners {
			if o == owner {
				out = append(out, file)
			}
		}
	}
	sort.Strings(out)
	return out
}

// resolveProbe parses file, finds its `go(<expr>)` probe call and resolves the
// argument. Everything a caller must decide — which expression is the URL —
// stays outside the engine, so the fixtures name it explicitly.
func resolveCrossProbe(t *testing.T, fs *fakeSource, file string) Value {
	t.Helper()
	spec, err := EmbeddedSpec("tsx")
	if err != nil {
		t.Fatal(err)
	}
	src, root, ok := fs.Parse(file)
	if !ok {
		t.Fatalf("fixture %s does not parse", file)
	}
	expr := findJSProbeArg(root, src)
	if expr == nil {
		t.Fatalf("fixture %s has no go(...) probe call", file)
	}
	e := New(spec, fs, Options{MaxFiles: 16})
	return e.Resolve(Query{File: file, Src: src, Root: root, Expr: expr})
}

func mustStrings(t *testing.T, v Value) []string {
	t.Helper()
	got, ok := v.Strings(0)
	if !ok {
		t.Fatalf("value did not resolve: %s (origins %+v)", v.String(), v.Origins())
	}
	return got
}

// The worked example from the plan: the parent computes the endpoint from a
// local const and passes it down; the child reads it off a destructured prop.
func TestCrossForwardResolvesJSXProp(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"Child.tsx": `export default class Child extends React.Component {
  save() { const { createUrl } = this.props; go(createUrl); }
}`,
			"Parent.tsx": `export function Parent({ kind }) {
  const base = "/api/v1/games";
  return <Child createUrl={` + "`${base}/${kind}`" + `} />;
}`,
		},
		owners: map[string][]string{"Child.tsx": {"Child"}, "Parent.tsx": {"Parent"}},
	}

	v := resolveCrossProbe(t, fs, "Child.tsx")
	got := mustStrings(t, v)
	if len(got) != 1 || got[0] != "/api/v1/games/*" {
		t.Fatalf("strings = %v, want [/api/v1/games/*]", got)
	}
	srcs := v.Sources()
	if len(srcs) != 1 || srcs[0].Reason != "jsx_attribute" || srcs[0].File != "Parent.tsx" {
		t.Fatalf("sources = %+v, want one jsx_attribute site in Parent.tsx", srcs)
	}
}

// The same prop read as a qualified member rather than destructured. The spec
// stops at a member expression; a declared root is what makes this one a
// binding instead.
func TestCrossForwardResolvesQualifiedPropRead(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"Child.tsx": `export default class Child extends React.Component {
  save() { go(this.props.createUrl); }
}`,
			"Parent.tsx": `export function Parent() { return <Child createUrl="/api/widgets" />; }`,
		},
		owners: map[string][]string{"Child.tsx": {"Child"}, "Parent.tsx": {"Parent"}},
	}
	if got := mustStrings(t, resolveCrossProbe(t, fs, "Child.tsx")); len(got) != 1 || got[0] != "/api/widgets" {
		t.Fatalf("strings = %v, want [/api/widgets]", got)
	}
}

// A reusable modal rendered from three sites has three create URLs. The engine
// returns all three, each knowing which site produced it: a caller that mints
// one node per URL needs the alternatives kept apart, and one that abstains on
// disagreement needs to see that there was disagreement. Neither decision is
// made here.
func TestCrossForwardKeepsEveryRenderSite(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"Modal.tsx": `export default class Modal extends React.Component {
  save() { const { createUrl } = this.props; go(createUrl); }
}`,
			"A.tsx": `export function A() { return <Modal createUrl="/api/a" />; }`,
			"B.tsx": `export function B() { return <Modal createUrl="/api/b" />; }`,
			"C.tsx": `export function C() { return <Modal createUrl="/api/c" />; }`,
		},
		owners: map[string][]string{
			"Modal.tsx": {"Modal"}, "A.tsx": {"A"}, "B.tsx": {"B"}, "C.tsx": {"C"},
		},
	}

	v := resolveCrossProbe(t, fs, "Modal.tsx")
	got := mustStrings(t, v)
	if len(got) != 3 || got[0] != "/api/a" || got[2] != "/api/c" {
		t.Fatalf("strings = %v, want the three render sites", got)
	}
	alts := v.Alternatives()
	if len(alts) != 3 {
		t.Fatalf("alternatives = %d, want 3", len(alts))
	}
	for _, a := range alts {
		if a.Src.File == "" || a.Src.Reason != "jsx_attribute" {
			t.Errorf("alternative %q has no producer site: %+v", a.String(), a.Src)
		}
	}
}

// The mirror rule: the parent owns the transport and its URL is a parameter;
// the child supplies the argument at its own call site.
func TestCrossReverseResolvesCallbackArgument(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"Parent.tsx": `export default class Parent extends React.Component {
  postToServer = (transport, data, updateURL) => go(updateURL);
  render() { return <Grid postToServer={this.postToServer} />; }
}`,
			"Grid.tsx": `export default class Grid extends React.Component {
  save(data) { this.props.postToServer(this.props.transport, data, "/api/things/7"); }
}`,
		},
		owners: map[string][]string{"Parent.tsx": {"Parent"}, "Grid.tsx": {"Grid"}},
	}

	v := resolveCrossProbe(t, fs, "Parent.tsx")
	got := mustStrings(t, v)
	if len(got) != 1 || got[0] != "/api/things/7" {
		t.Fatalf("strings = %v, want [/api/things/7]", got)
	}
	srcs := v.Sources()
	if len(srcs) != 1 || srcs[0].Reason != "jsx_attribute_callback" || srcs[0].File != "Grid.tsx" {
		t.Fatalf("sources = %+v, want one jsx_attribute_callback site in Grid.tsx", srcs)
	}
}

// A call that supplies no argument at that position is not "no URL here": it is
// a site the ledger has to be able to name, so it stops with its own reason.
func TestCrossReverseReportsArity(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"Parent.tsx": `export default class Parent extends React.Component {
  postToServer = (transport, data, updateURL) => go(updateURL);
  render() { return <Grid postToServer={this.postToServer} />; }
}`,
			"Grid.tsx": `export default class Grid extends React.Component {
  save(data) { this.props.postToServer(this.props.transport, data); }
}`,
		},
		owners: map[string][]string{"Parent.tsx": {"Parent"}, "Grid.tsx": {"Grid"}},
	}

	v := resolveCrossProbe(t, fs, "Parent.tsx")
	origins := v.Origins()
	if len(origins) != 1 || origins[0].Reason != ReasonArity {
		t.Fatalf("origins = %+v, want one Opaque(arity)", origins)
	}
}

// An engine whose FileSource is not a CrossSource has no crossings at all. The
// shipped intraprocedural callers depend on this: it is what keeps VG.3's
// resolution from silently acquiring VG.4's reach.
func TestCrossingsInertWithoutACrossSource(t *testing.T) {
	spec, err := EmbeddedSpec("tsx")
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`class Child { save() { const { createUrl } = this.props; go(createUrl); } }`)
	root, err := sitter.ParseCtx(context.Background(), src, tsxsitter.GetLanguage())
	if err != nil {
		t.Fatal(err)
	}
	e := New(spec, filesOnly{}, Options{})
	v := e.Resolve(Query{File: "Child.tsx", Src: src, Root: root, Expr: findJSProbeArg(root, src)})
	if v.Kind != KindOpaque {
		t.Fatalf("want Opaque without a CrossSource, got %s", v.String())
	}
}

// filesOnly implements FileSource and deliberately not CrossSource.
type filesOnly struct{}

func (filesOnly) Files() []string { return nil }
func (filesOnly) Parse(string) ([]byte, *sitter.Node, bool) {
	return nil, nil, false
}

// Two components that pass the same prop to each other terminate, and say why:
// a value that stopped on a cycle and one that stopped on the depth cap are
// different facts about the code.
func TestCrossingCycleTerminates(t *testing.T) {
	fs := &fakeSource{
		src: map[string]string{
			"A.tsx": `export default class A extends React.Component {
  render() { const { url } = this.props; go(url); return <B url={this.props.url} />; }
}`,
			"B.tsx": `export default class B extends React.Component {
  render() { return <A url={this.props.url} />; }
}`,
		},
		owners: map[string][]string{"A.tsx": {"A"}, "B.tsx": {"B"}},
	}
	v := resolveCrossProbe(t, fs, "A.tsx")
	if v.Kind != KindOpaque && !v.IsDynamic() {
		t.Fatalf("a prop cycle must stop, got %s", v.String())
	}
}
