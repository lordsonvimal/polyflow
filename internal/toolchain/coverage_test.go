package toolchain

import (
	"strings"
	"testing"
)

const coverProfilesJSON = `{
  "web": {
    "templ":    {"profile": "templ-v0.3",  "version": "0.3.1020"},
    "datastar": {"profile": "datastar-v1", "version": "2.0.0", "inferred": true},
    "go":       {"profile": "go-v1",       "version": "1.22"}
  },
  "api": {
    "ruby": {"profile": "ruby-v3", "version": "3.3.5"}
  }
}`

const coverNotesJSON = `[
  {"service": "web", "tool": "datastar", "requested_version": "2.0.0", "used_profile": "datastar-v1"},
  {"service": "api", "tool": "templ", "requested_version": "0.2.0", "used_profile": "in-process", "note": "sidecar missing"}
]`

func TestRenderVersionCoverage_Table(t *testing.T) {
	out := RenderVersionCoverage(coverProfilesJSON, coverNotesJSON)

	for _, want := range []string{
		"Versioning coverage:",
		"service", "tool", "version", "profile", "inferred",
		"templ-v0.3", "0.3.1020",
		"datastar-v1", "2.0.0", "yes",
		"ruby-v3", "3.3.5",
		"2 fallback selection(s)",
		"api/templ requested=0.2.0 used=in-process  (sidecar missing)",
		"web/datastar requested=2.0.0 used=datastar-v1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// api sorts before web (rows sorted by service, then tool).
	if strings.Index(out, "api") > strings.Index(out, "web") {
		t.Errorf("rows not sorted by service:\n%s", out)
	}
}

// Two-run determinism (bug-class rule 2): the renderer iterates parsed maps —
// byte-identical output across runs proves no map order reaches the table.
func TestRenderVersionCoverage_TwoRunDeterminism(t *testing.T) {
	first := RenderVersionCoverage(coverProfilesJSON, coverNotesJSON)
	second := RenderVersionCoverage(coverProfilesJSON, coverNotesJSON)
	if first != second {
		t.Errorf("two runs differ:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// A pre-V.2 graph has no toolchain_profiles meta: report unstamped, never an
// empty table (V.2 outcome note: doctor must treat absence as unstamped).
func TestRenderVersionCoverage_Unstamped(t *testing.T) {
	for _, in := range []string{"", "  ", "{}"} {
		out := RenderVersionCoverage(in, "")
		if !strings.Contains(out, "unstamped") {
			t.Errorf("input %q: want unstamped notice, got:\n%s", in, out)
		}
	}
}

func TestRenderVersionCoverage_NoNotes(t *testing.T) {
	out := RenderVersionCoverage(coverProfilesJSON, "[]")
	if strings.Contains(out, "fallback selection") {
		t.Errorf("no notes → no fallback section:\n%s", out)
	}
}
