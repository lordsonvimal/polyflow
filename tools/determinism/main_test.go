package main

import (
	"strings"
	"testing"
)

func TestFirstDiff_Identical(t *testing.T) {
	dump := "N {\"id\":\"a\"}\nE {\"id\":\"e1\"}\n"
	if dump != dump { //nolint:gocritic // mirrors main's byte-equality gate
		t.Fatal("identical dumps compared unequal")
	}
	if got := firstDiff(dump, dump); !strings.Contains(got, "no line-level difference") {
		t.Fatalf("expected no line diff, got %q", got)
	}
}

func TestFirstDiff_OneEdgeDiffers(t *testing.T) {
	a := "N {\"id\":\"a\"}\nE {\"id\":\"e1\",\"to\":\"x\"}\n"
	b := "N {\"id\":\"a\"}\nE {\"id\":\"e1\",\"to\":\"y\"}\n"
	if a == b {
		t.Fatal("dumps that differ by one edge compared equal")
	}
	got := firstDiff(a, b)
	if !strings.Contains(got, "line 2") {
		t.Fatalf("expected diff at line 2, got %q", got)
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("x\ny\nz")
	if len(got) != 3 || got[2] != "z" {
		t.Fatalf("splitLines: %#v", got)
	}
}
