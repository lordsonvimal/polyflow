package valuegraph

import (
	"reflect"
	"testing"
)

func opaque(reason string) Value { return Opaque(Origin{Reason: reason, Text: reason}) }

func TestStrings(t *testing.T) {
	cases := []struct {
		name string
		v    Value
		max  int
		want []string
		ok   bool
	}{
		{
			name: "literal",
			v:    Literal("/api/v1/games"),
			want: []string{"/api/v1/games"},
			ok:   true,
		},
		{
			name: "concat of literals merges",
			v:    Concat(Literal("/api"), Literal("/v1"), Literal("/games")),
			want: []string{"/api/v1/games"},
			ok:   true,
		},
		{
			name: "opaque part becomes a wildcard",
			v:    Concat(Literal("/api/games/"), opaque(ReasonMember)),
			want: []string{"/api/games/*"},
			ok:   true,
		},
		{
			name: "wholly opaque is not ok",
			v:    opaque(ReasonNoBinding),
			want: []string{"*"},
			ok:   false,
		},
		{
			name: "adjacent wildcards collapse",
			v:    Concat(opaque(ReasonMember), opaque(ReasonParam), Literal("/x")),
			want: []string{"*/x"},
			ok:   true,
		},
		{
			name: "union enumerates both arms",
			v:    Union(Literal("/api/a"), Literal("/api/b")),
			want: []string{"/api/a", "/api/b"},
			ok:   true,
		},
		{
			name: "union multiplies out through a concat",
			v: Concat(
				Union(Literal("/a"), Literal("/b")),
				Union(Literal("/1"), Literal("/2")),
			),
			want: []string{"/a/1", "/a/2", "/b/1", "/b/2"},
			ok:   true,
		},
		{
			name: "a union with one readable arm is still ok",
			v:    Union(Literal("/api/a"), opaque(ReasonCall)),
			want: []string{"*", "/api/a"},
			ok:   true,
		},
		{
			name: "truncation is not ok",
			v:    Union(Literal("/a"), Literal("/b"), Literal("/c")),
			max:  2,
			want: []string{"/a", "/b"},
			ok:   false,
		},
		{
			name: "empty literal carries no content",
			v:    Literal("  "),
			want: []string{"  "},
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.v.Strings(tc.max)
			if !reflect.DeepEqual(got, tc.want) || ok != tc.ok {
				t.Fatalf("Strings(%d) = %q, %v; want %q, %v", tc.max, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestConcatFlattensAndMerges(t *testing.T) {
	v := Concat(Concat(Literal("/a"), Literal("/b")), Literal(""), Concat(Literal("/c")))
	if v.Kind != KindLiteral || v.Text != "/a/b/c" {
		t.Fatalf("nested concat of literals should reduce to one literal, got %+v", v)
	}

	v = Concat(Literal("/a"), opaque(ReasonParam), Literal("/b"), Literal("/c"))
	if v.Kind != KindConcat || len(v.Parts) != 3 {
		t.Fatalf("want 3 parts (literal, opaque, merged literal), got %+v", v)
	}
	if v.Parts[2].Text != "/b/c" {
		t.Fatalf("adjacent literals should merge, got %q", v.Parts[2].Text)
	}
}

func TestUnionIsOrderIndependentAndDeduped(t *testing.T) {
	a := Union(Literal("/a"), Literal("/b"), Literal("/a"))
	b := Union(Literal("/b"), Literal("/a"))
	if a.String() != b.String() {
		t.Fatalf("union membership must not depend on order: %q vs %q", a.String(), b.String())
	}
	if len(a.Parts) != 2 {
		t.Fatalf("want 2 deduped alternatives, got %d", len(a.Parts))
	}
	if one := Union(Literal("/a"), Literal("/a")); one.Kind != KindLiteral {
		t.Fatalf("a union of one distinct value is that value, got %+v", one)
	}
	if none := Union(); none.Kind != KindOpaque {
		t.Fatalf("a union of nothing is a stopped resolution, got %+v", none)
	}
}

func TestIsDynamicAndOrigins(t *testing.T) {
	v := Concat(Literal("/api/"), Union(opaque(ReasonMember), Literal("x")), opaque(ReasonCall))
	if !v.IsDynamic() {
		t.Fatal("a value with an opaque part is dynamic")
	}
	if Concat(Literal("/a"), Literal("/b")).IsDynamic() {
		t.Fatal("a value of literals is not dynamic")
	}

	var reasons []string
	for _, o := range v.Origins() {
		reasons = append(reasons, o.Reason)
	}
	want := []string{ReasonMember, ReasonCall}
	if !reflect.DeepEqual(reasons, want) {
		t.Fatalf("Origins() = %v; want %v", reasons, want)
	}
}

func TestOpaqueAlwaysHasAReason(t *testing.T) {
	v := Opaque(Origin{Text: "whatever"})
	if v.Origin.Reason != ReasonUnsupported {
		t.Fatalf("an Opaque with no reason must not stay empty, got %q", v.Origin.Reason)
	}
}
