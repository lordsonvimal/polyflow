package rgrammar_test

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/parser/rgrammar"
)

func TestRGrammarParses(t *testing.T) {
	p := sitter.NewParser()
	p.SetLanguage(rgrammar.GetLanguage())
	src := []byte("f <- function(x) {\n  x + 1\n}\n")
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if tree == nil {
		t.Fatal("nil tree")
	}
	root := tree.RootNode()
	if root.HasError() {
		t.Fatalf("tree has parse errors: %s", root.String())
	}
	t.Logf("root type=%s children=%d sexpr=%s", root.Type(), root.NamedChildCount(), root.String())
}
