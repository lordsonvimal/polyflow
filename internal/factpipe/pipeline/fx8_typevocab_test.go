package pipeline_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// deprecatedTypeVocabAliases are the FX.8.V fold's library-named
// NodeType/EdgeType constants: kept only so previously-persisted graphs
// still decode, never minted by current code. Each has a live domain
// replacement + meta.via carrying the framework flavour (see their doc
// comments in internal/graph/model.go).
var deprecatedTypeVocabAliases = map[string]bool{
	"NodeTypeClientRoute":     true,
	"NodeTypeTemplElement":    true,
	"EdgeTypeDatastarAction":  true,
	"EdgeTypeDatastarBind":    true,
	"EdgeTypeSidekiqEnqueue":  true,
	"EdgeTypeSidekiqPerform":  true,
	"EdgeTypePusherTrigger":   true,
	"EdgeTypePusherSubscribe": true,
}

// libraryNames are framework/library names that must never appear in a
// live (non-deprecated) NodeType/EdgeType string value — FX.8's point is
// that framework flavour lives in meta.via, not the type vocabulary.
var libraryNames = []string{
	"pusher", "datastar", "sidekiq", "rails", "sprockets",
	"gin", "express", "solid", "templ",
}

var typeConstRE = regexp.MustCompile(`^\s*(NodeType\w+|EdgeType\w+)\s+(?:NodeType|EdgeType)\s*=\s*"([^"]*)"`)

// TestFX8_TypeVocabHasNoLibraryName is FX.8.CIb: every live NodeType/EdgeType
// constant's string value must be framework-agnostic. A deprecated alias
// (deprecatedTypeVocabAliases) is exempt — it exists precisely to preserve a
// library-named string for graphs persisted before the FX.8.V fold.
func TestFX8_TypeVocabHasNoLibraryName(t *testing.T) {
	src, err := os.ReadFile("../../graph/model.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		m := typeConstRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, value := m[1], m[2]
		if deprecatedTypeVocabAliases[name] {
			continue
		}
		lower := strings.ToLower(value)
		for _, lib := range libraryNames {
			if strings.Contains(lower, lib) {
				t.Errorf("%s = %q contains library name %q — frameworks belong in meta.via, not the type vocabulary (FX.8.V)", name, value, lib)
			}
		}
	}
}
