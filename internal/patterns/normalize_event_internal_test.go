package patterns

import (
	"testing"
)

// TestNormalizeEventName_VuePrefixes verifies the Vue @/v-on: prefix extensions
// and modifier stripping added for Phase M.1.
func TestNormalizeEventName_VuePrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		// Pre-existing prefixes must remain unchanged.
		{"onclick", "click"},
		{"onClick", "click"},
		{"on:click", "click"},
		{"oncapture:click", "click"},
		// New Vue @-shorthand prefix.
		{"@click", "click"},
		{"@submit", "submit"},
		// New v-on: long-form prefix.
		{"v-on:click", "click"},
		{"v-on:submit", "submit"},
		// Vue modifiers (test-pinned per spec).
		{"@submit.prevent", "submit"},
		{"@click.stop", "click"},
		{"@keydown.enter", "keydown"},
		{"v-on:submit.prevent", "submit"},
		// Modifier stripping must not affect standard on-prefixed names.
		{"onclick", "click"}, // no dot → unchanged stripping path
	}
	for _, c := range cases {
		got := normalizeEventName(c.in)
		if got != c.want {
			t.Errorf("normalizeEventName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
