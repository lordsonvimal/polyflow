package railsinflect

import "testing"

// TestSingularize covers the inflections used for nested-resource parameters,
// route names, ActiveRecord association targets, and Devise scope-to-model
// inflection. The irregulars matter more for the latter three: a parameter
// name normalizes to a wildcard when matched, but a view's `person_path` or
// an association's `Person` class name is looked up verbatim and either hits
// or does not.
func TestSingularize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"folders":  "folder",
		"files":    "file",
		"studies":  "study",
		"boxes":    "box",
		"branches": "branch",
		"classes":  "class",
		"status":   "status",
		"user":     "user",
		// "-se" nouns (single s before the final "es") must lose only the
		// trailing "s", not "es" — found live via `load_and_authorize_resource`
		// on UserLicensesController singularizing to "user_licens".
		"licenses":  "license",
		"houses":    "house",
		"phases":    "phase",
		"responses": "response",
		"people":   "person",
		"children": "child",
		"media":    "medium",
		// Deliberately not inflected: the -ves rule that would give "leaf"
		// gives "archif" and "mof" for the names real apps actually use.
		"archives": "archive",
		"moves":    "move",
	} {
		if got := Singularize(in); got != want {
			t.Errorf("Singularize(%q) = %q, want %q", in, got, want)
		}
	}
}
