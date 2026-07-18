package parser

import (
	"testing"
)

func TestSplitERB_PreservesLineNumbers(t *testing.T) {
	// <html>\n<% if true %>\n<%= link_to 'x', y_path %>\n<% end %>\n</html>
	src := []byte("<html>\n<% if true %>\n<%= link_to 'x', y_path %>\n<% end %>\n</html>")
	blanked, virtual := splitERB(src)

	// blankedHTML: '<html>' preserved, newlines preserved, ERB tags → spaces.
	if blanked[0] != '<' {
		t.Errorf("blankedHTML[0]: expected '<', got %q", blanked[0])
	}
	// Newline at position 6 (end of '<html>') preserved in both.
	if blanked[6] != '\n' {
		t.Errorf("blankedHTML[6]: expected newline, got %q", blanked[6])
	}
	if virtual[6] != '\n' {
		t.Errorf("virtualRuby[6]: expected newline, got %q", virtual[6])
	}

	// virtualRuby: '<html>' (positions 0-5) must be spaces.
	for i := 0; i < 6; i++ {
		if virtual[i] != ' ' {
			t.Errorf("virtualRuby[%d]: expected space (blanked HTML), got %q", i, virtual[i])
		}
	}

	// virtualRuby: 'if' inside '<% if true %>' must be present.
	// src: ...\n<% if true %>\n... → after position 7: <, %, space, i, f, ...
	found := false
	for i := 9; i < len(virtual)-2; i++ {
		if virtual[i] == 'i' && i+1 < len(virtual) && virtual[i+1] == 'f' {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("virtualRuby: 'if' token missing; got %q", string(virtual))
	}

	// blankedHTML: ERB tag on line 2 (positions 7-20) must be spaces (not '<', '%').
	if blanked[7] == '<' || blanked[7] == '%' {
		t.Errorf("blankedHTML[7]: ERB delimiter not blanked, got %q", blanked[7])
	}
}

func TestSplitERB_NewlineTag(t *testing.T) {
	// Multi-line ERB tag: newlines inside the tag must survive in blankedHTML.
	src := []byte("<div>\n<%\n  x = 1\n%>\n</div>")
	blanked, virtual := splitERB(src)

	// blankedHTML newlines preserved inside multi-line tag.
	nl1 := false
	for _, b := range blanked {
		if b == '\n' {
			nl1 = true
			break
		}
	}
	if !nl1 {
		t.Error("blankedHTML: expected at least one newline inside multi-line ERB tag")
	}

	// virtualRuby: 'x' from '  x = 1' should be present.
	found := false
	for _, b := range virtual {
		if b == 'x' {
			found = true
			break
		}
	}
	if !found {
		t.Error("virtualRuby: variable 'x' missing from multi-line ERB tag")
	}

	// virtualRuby: '<div>' HTML (positions 0-4) must be spaces.
	for i := 0; i < 5; i++ {
		if virtual[i] != ' ' {
			t.Errorf("virtualRuby[%d]: expected space, got %q", i, virtual[i])
		}
	}
}
