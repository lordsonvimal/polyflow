package pluginloader

import (
	"strings"
	"testing"
)

const coverNotesJSON = `[
  {"Plugin": "django", "Component": "orm", "Reason": "django: resolved version \"3.2.0\" outside version_range \">=4.0\" for service web"}
]`

func TestRenderCoverage_Table(t *testing.T) {
	out := RenderCoverage(coverNotesJSON)
	for _, want := range []string{
		"Plugin coverage:",
		"1 out-of-range component/service pair(s)",
		"django/orm: django: resolved version",
		"3.2.0",
		">=4.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderCoverage_Unstamped(t *testing.T) {
	for _, in := range []string{"", "  "} {
		out := RenderCoverage(in)
		if !strings.Contains(out, "unstamped") {
			t.Errorf("input %q: want unstamped notice, got:\n%s", in, out)
		}
	}
}

func TestRenderCoverage_NoNotes(t *testing.T) {
	out := RenderCoverage("[]")
	if !strings.Contains(out, "no out-of-range") {
		t.Errorf("empty notes: want no-out-of-range notice, got:\n%s", out)
	}
}

func TestRenderCoverage_TwoRunDeterminism(t *testing.T) {
	first := RenderCoverage(coverNotesJSON)
	second := RenderCoverage(coverNotesJSON)
	if first != second {
		t.Errorf("two runs differ:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}
