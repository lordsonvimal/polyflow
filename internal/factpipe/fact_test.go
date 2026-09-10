package factpipe

import (
	"reflect"
	"testing"
)

func TestAtomRoundTrip(t *testing.T) {
	cases := []struct {
		atom Atom
		kind AtomKind
		val  string
	}{
		{Str("Authenticate"), AtomStr, "Authenticate"},
		{Str(""), AtomStr, ""}, // empty string is a legal value
		{Node("svc\x00f.go:12"), AtomNode, "svc\x00f.go:12"},
		{Int(0), AtomInt, "0"},
		{Int(-3), AtomInt, "-3"},
		{Int(42), AtomInt, "42"},
	}
	for _, c := range cases {
		if c.atom.Kind != c.kind {
			t.Errorf("%+v: kind = %d, want %d", c.atom, c.atom.Kind, c.kind)
		}
		if got := c.atom.Value(); got != c.val {
			t.Errorf("%+v: Value() = %q, want %q", c.atom, got, c.val)
		}
	}
}

func TestFactSetByFileIsolation(t *testing.T) {
	s := NewFactSet()
	s.Add(Fact{Pred: "gin_mw_use", Args: []Atom{Str("r"), Str("Authenticate")},
		Origin: Origin{Kind: OriginPattern, File: "a.go", Line: 10, Pattern: "gin_mw_use"}})
	s.Add(Fact{Pred: "gin_group", Args: []Atom{Str("api"), Str("r")},
		Origin: Origin{Kind: OriginPattern, File: "a.go", Line: 3, Pattern: "gin_group"}})
	s.Add(Fact{Pred: "calls_edge", Args: []Atom{Node("fn"), Str("Authenticate"), Node("tgt")},
		Origin: Origin{Kind: OriginGraph, File: "b.go", Pattern: "calls"}})

	if s.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", s.Len())
	}
	if got := len(s.ByFile("a.go")); got != 2 {
		t.Errorf("ByFile(a.go) = %d facts, want 2", got)
	}
	if got := len(s.ByFile("b.go")); got != 1 {
		t.Errorf("ByFile(b.go) = %d facts, want 1", got)
	}
	if got := len(s.ByFile("missing.go")); got != 0 {
		t.Errorf("ByFile(missing.go) = %d facts, want 0", got)
	}
	// ByFile preserves insertion order within a file.
	af := s.ByFile("a.go")
	if af[0].Pred != "gin_mw_use" || af[1].Pred != "gin_group" {
		t.Errorf("ByFile(a.go) order = [%s %s], want [gin_mw_use gin_group]", af[0].Pred, af[1].Pred)
	}
}

func TestFactSetAllIsInsertionOrder(t *testing.T) {
	s := NewFactSet()
	preds := []string{"c", "a", "b", "a"}
	for _, p := range preds {
		s.Add(Fact{Pred: p, Origin: Origin{File: "x"}})
	}
	got := make([]string, 0, len(preds))
	for _, f := range s.All() {
		got = append(got, f.Pred)
	}
	if !reflect.DeepEqual(got, preds) {
		t.Errorf("All() order = %v, want %v", got, preds)
	}
}

func TestFilesSorted(t *testing.T) {
	s := NewFactSet()
	for _, f := range []string{"z.go", "a.go", "m.go", "a.go"} {
		s.Add(Fact{Pred: "p", Origin: Origin{File: f}})
	}
	got := Files(s)
	want := []string{"a.go", "m.go", "z.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
}
