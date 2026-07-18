package parser

import (
	"strings"
	"testing"
)

// Regression: the body of an ERB comment tag (<%# ... %>) must be blanked in
// the virtualRuby view. Blanking only the '#' marker left the comment body
// live, so commented-out helpers (<%# link_to ... %>) minted phantom nav
// nodes and edges.
func TestSplitERB_CommentTagIsNotLiveRuby(t *testing.T) {
	src := []byte(`<div><%# link_to "Reports", reports_path %></div>
<p><%= link_to "Live", live_path %></p>
<%#
  form_with url: hidden_path do |f|
  end
%>`)
	_, virtualRuby := splitERB(src)
	rb := string(virtualRuby)

	if strings.Contains(rb, "reports_path") || strings.Contains(rb, "hidden_path") {
		t.Fatalf("ERB comment content leaked into virtualRuby:\n%s", rb)
	}
	if !strings.Contains(rb, `link_to "Live", live_path`) {
		t.Fatalf("live ERB output tag was lost from virtualRuby:\n%s", rb)
	}
	// Line count must be preserved (byte offsets drive node line numbers).
	if strings.Count(rb, "\n") != strings.Count(string(src), "\n") {
		t.Fatalf("newline count changed: %d != %d", strings.Count(rb, "\n"), strings.Count(string(src), "\n"))
	}
}
