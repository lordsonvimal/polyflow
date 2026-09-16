package patterns

import (
	"strings"
	"testing"
)

// stubAncestorMatching mirrors
// internal/pluginloader/testdata/verbplugin/main.go's ExtractVerb exactly —
// kept in sync manually since a real plugin subprocess (exercised by
// internal/pluginloader's own round-trip test) and this in-process package
// can't share code across the SDK boundary. Registering this stub (instead
// of spawning the real subprocess) lets this test assert FX.9's acceptance
// claim — "produces the same facts as the in-tree verb" — against the exact
// runVerb dispatch path a real plugin's registration wires into.
func stubAncestorMatching(node VerbNode, arg, _, _ string) ([]VerbResult, bool) {
	kind, predicate, _ := strings.Cut(arg, ",")
	kind = strings.TrimSpace(kind)
	predicate = strings.TrimSpace(predicate)
	found := ""
	for _, a := range node.Ancestors {
		if a.Type == kind && strings.Contains(a.Text, predicate) {
			found = "true"
			break
		}
	}
	return []VerbResult{{Str: found}}, true
}

// TestPluginVerb_AncestorMatching_MatchesInTreeEnclosing proves the FX.9
// reference verb (ancestor_matching(kind,predicate), registered exactly as
// internal/pluginloader.RegisterVerbs would wire a real plugin) agrees with
// the in-tree enclosing_name(class) verb on the same node: enclosing_name
// finds "UsersController" for sym, and ancestor_matching(class,
// UsersController) must independently answer "true" for the identical
// question asked a structurally different way (a substring predicate over
// the ancestor chain, rather than a field-name lookup on the matched
// ancestor).
func TestPluginVerb_AncestorMatching_MatchesInTreeEnclosing(t *testing.T) {
	RegisterVerbProvider("ancestor_matching", stubAncestorMatching)
	defer ResetVerbProviders()

	rb := `class Api::UsersController < ApplicationController
  helper_method :current_user
  private
  def current_user
    RestClient.get("https://x/#{id}")
  end
end
`
	src := []byte(rb)
	ec := &ExtractContext{Src: src, Grammar: "ruby", File: "users_controller.rb"}
	root := parseTS(t, "ruby", rb)
	sym := firstOfType(root, "simple_symbol")

	wantName := runVerb("enclosing_name(class)", sym, ec)[0].atom().Value()
	if wantName == "" {
		t.Fatalf("in-tree enclosing_name(class) found nothing — fixture broken")
	}

	got := runVerb("ancestor_matching(class,"+wantName+")", sym, ec)[0].atom().Value()
	if got != "true" {
		t.Errorf("ancestor_matching(class,%s) = %q, want %q (in-tree enclosing_name found %q)", wantName, got, "true", wantName)
	}

	// A predicate that cannot match must decline, same "" convention every
	// other boolean-as-string verb (has_keyword) uses.
	miss := runVerb("ancestor_matching(class,NoSuchClass)", sym, ec)[0].atom().Value()
	if miss != "" {
		t.Errorf("ancestor_matching(class,NoSuchClass) = %q, want empty", miss)
	}
}

// TestRunRegisteredVerb_Unregistered confirms the "unregistered == declined"
// convention RunRegisteredVerb documents.
func TestRunRegisteredVerb_Unregistered(t *testing.T) {
	if _, ok := RunRegisteredVerb("no_such_verb", VerbNode{}, "", "", ""); ok {
		t.Errorf("RunRegisteredVerb(unregistered) ok = true, want false")
	}
}
