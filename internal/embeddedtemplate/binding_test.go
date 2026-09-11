package embeddedtemplate_test

import (
	"context"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/embeddedtemplate"
	sitter "github.com/smacker/go-tree-sitter"
)

// TestGrammar pins the ABI compatibility this package exists for: the
// grammar's own module tags newer than v0.23.2 emit LANGUAGE_VERSION 15,
// which smacker/go-tree-sitter's bundled runtime (max compatible 14) rejects
// silently — ts_parser_set_language fails and every parse then errors
// ErrNoLanguage. go.mod pins v0.23.2 for exactly this reason; bumping the
// dependency past a runtime-incompatible tag would break this test instead of
// silently going inert.
func TestGrammar(t *testing.T) {
	src := []byte(`<div><%= javascript_include_tag "application" %></div><%# skip %>`)
	root, err := sitter.ParseCtx(context.Background(), src, embeddedtemplate.GetLanguage())
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	want := "(template (content) (output_directive (code)) (content) (comment_directive (comment)))"
	if got := root.String(); got != want {
		t.Errorf("tree = %q, want %q", got, want)
	}
}
