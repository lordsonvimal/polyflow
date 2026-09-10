package factpipe

import (
	"reflect"
	"testing"
)

func sassResolve() CompiledResolve {
	return CompiledResolve{spec: ResolveSpec{
		Relation:      "sass_target",
		From:          "sass_import",
		OwnDirFirst:   true,
		Roots:         []string{"app/assets/stylesheets", "vendor/assets/stylesheets"},
		Extensions:    []string{".scss", ".css"},
		PartialPrefix: "_",
		IndexFiles:    []string{"index"},
	}}
}

func sprocketsResolve() CompiledResolve {
	return CompiledResolve{spec: ResolveSpec{
		Relation:    "sprockets_target",
		From:        "sprockets_ref",
		OwnDirFirst: true,
		Roots: []string{
			"app/assets/javascripts",
			"vendor/assets/javascripts",
			"lib/assets/javascripts",
		},
		Extensions: []string{".js"},
	}}
}

func TestResolveCandidatesSassPartial(t *testing.T) {
	got := sassResolve().candidates("app/assets/stylesheets/application.scss", "colors")
	want := []string{
		"app/assets/stylesheets/colors.scss",
		"app/assets/stylesheets/_colors.scss",
		"app/assets/stylesheets/colors.css",
		"app/assets/stylesheets/_colors.css",
		"app/assets/stylesheets/colors/index.scss",
		"app/assets/stylesheets/colors/_index.scss",
		"app/assets/stylesheets/colors/index.css",
		"app/assets/stylesheets/colors/_index.css",
		"vendor/assets/stylesheets/colors.scss",
		"vendor/assets/stylesheets/_colors.scss",
		"vendor/assets/stylesheets/colors.css",
		"vendor/assets/stylesheets/_colors.css",
		"vendor/assets/stylesheets/colors/index.scss",
		"vendor/assets/stylesheets/colors/_index.scss",
		"vendor/assets/stylesheets/colors/index.css",
		"vendor/assets/stylesheets/colors/_index.css",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates:\n got %#v\nwant %#v", got, want)
	}
}

func TestResolveCandidatesRelativeUp(t *testing.T) {
	got := sassResolve().candidates("app/assets/stylesheets/pages/home.scss", "../base/colors")
	// own dir resolves ../ ; the first candidate must land in base/.
	if got[0] != "app/assets/stylesheets/base/colors.scss" {
		t.Errorf("got[0] = %q, want app/assets/stylesheets/base/colors.scss", got[0])
	}
	if got[1] != "app/assets/stylesheets/base/_colors.scss" {
		t.Errorf("got[1] = %q, want the _partial form", got[1])
	}
}

func TestResolveCandidatesEscapeDropped(t *testing.T) {
	for _, c := range sassResolve().candidates("a.scss", "../../../etc/passwd") {
		if c == "../../etc/passwd" || c == "etc/passwd" {
			t.Fatalf("escaping candidate not dropped: %q", c)
		}
	}
}

func TestResolveCandidatesVerbatimExtension(t *testing.T) {
	got := sprocketsResolve().candidates("app/assets/javascripts/application.js", "lib/jquery.min.js")
	if got[0] != "app/assets/javascripts/lib/jquery.min.js" {
		t.Errorf("got[0] = %q, want the verbatim path (spec already has an ext)", got[0])
	}
	if got[1] != "app/assets/javascripts/lib/jquery.min.js.js" {
		t.Errorf("got[1] = %q, want the +ext fallback", got[1])
	}
}

func TestResolveFirstHitByPrecedence(t *testing.T) {
	// both a root file and a vendor file exist; own-dir/first-root wins.
	rank := fileRank([]string{
		"vendor/assets/stylesheets/_colors.scss",
		"app/assets/stylesheets/_colors.scss",
	})
	tgt, _, ok := sassResolve().resolve("app/assets/stylesheets/application.scss", "colors", rank)
	if !ok || tgt != "app/assets/stylesheets/_colors.scss" {
		t.Fatalf("resolve = (%q, %v), want app/assets/stylesheets/_colors.scss", tgt, ok)
	}
}

func TestResolveMiss(t *testing.T) {
	rank := fileRank([]string{"app/assets/stylesheets/other.scss"})
	if _, _, ok := sassResolve().resolve("app/assets/stylesheets/a.scss", "colors", rank); ok {
		t.Fatal("resolve reported ok for an absent target")
	}
}

func TestCompileResolveSpecs(t *testing.T) {
	if _, err := CompileResolveSpecs([]ResolveSpec{{From: "x", Extensions: []string{".js"}}}); err == nil {
		t.Error("missing relation should fail")
	}
	if _, err := CompileResolveSpecs([]ResolveSpec{{Relation: "x", Extensions: []string{".js"}}}); err == nil {
		t.Error("missing from should fail")
	}
	if _, err := CompileResolveSpecs([]ResolveSpec{{Relation: "x", From: "x"}}); err == nil {
		t.Error("from == relation should fail")
	}
	if _, err := CompileResolveSpecs([]ResolveSpec{{Relation: "y", From: "x", Extensions: []string{".js"}}}); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
}

func TestApplyResolvesAddsFacts(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "sass_import", Args: []Atom{
		Str("app/assets/stylesheets/application.scss"), Int(3), Str("colors"),
	}})
	fs.Add(Fact{Pred: "sass_import", Args: []Atom{
		Str("app/assets/stylesheets/application.scss"), Int(4), Str("nonexistent"),
	}})

	ApplyResolves([]CompiledResolve{sassResolve()},
		[]string{"app/assets/stylesheets/_colors.scss", "app/assets/stylesheets/application.scss"}, fs)

	var got []Fact
	for _, f := range fs.All() {
		if f.Pred == "sass_target" {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("sass_target: %d facts, want 1 (the miss produces nothing)", len(got))
	}
	a := got[0].Args
	if a[0].Str != "app/assets/stylesheets/application.scss" || a[1].Int != 3 ||
		a[2].Str != "colors" || a[3].Str != "app/assets/stylesheets/_colors.scss" ||
		a[4].Kind != AtomInt {
		t.Errorf("sass_target args = %#v", a)
	}
	if got[0].Origin.Kind != OriginPrimitive {
		t.Errorf("origin kind = %d, want OriginPrimitive", got[0].Origin.Kind)
	}
}

func TestApplyResolvesNoBlockIsNoop(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "sass_import", Args: []Atom{Str("a.scss"), Int(1), Str("x")}})
	ApplyResolves(nil, []string{"x.scss"}, fs)
	if fs.Len() != 1 {
		t.Errorf("Len = %d, want 1 (no resolve block ⇒ inert)", fs.Len())
	}
}
