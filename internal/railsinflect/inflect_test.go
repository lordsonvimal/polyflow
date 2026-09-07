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
		"people":    "person",
		"children":  "child",
		"media":     "medium",
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

// TestPluralize covers the one caller that needs it: mapping a singular
// `resource :x` declaration onto the plural controller Rails routes it to.
// Every case below is a shape cedar's config/routes.rb actually declares.
func TestPluralize(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"home":                 "homes",
		"session":              "sessions",
		"schedule":             "schedules",
		"widget":               "widgets",
		"current_organization": "current_organizations",
		"widget_set":           "widget_sets",
		// y → ies only after a consonant; "day" is not "daies".
		"category": "categories",
		"day":      "days",
		// Sibilants take "es".
		"box":     "boxes",
		"branch":  "branches",
		"class":   "classes",
		"address": "addresses",
		"status":  "statuses",
		// Already plural: identity. Not hypothetical — one of cedar's singular
		// `resource` declarations is spelled plural already, and inflecting it
		// a second time would look for a controller that does not exist.
		"sessions": "sessions",
		"settings": "settings",
		"people":   "people",
		"media":    "media",
		// Irregulars come from Singularize's own table, read backwards, so the
		// two can never disagree about a word.
		"person": "people",
		"child":  "children",
		"":       "",
	} {
		if got := Pluralize(in); got != want {
			t.Errorf("Pluralize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPluralizeIdempotent is the property that keeps the identity case honest:
// pluralizing an already-pluralized word must not compound. Without it a rule
// that reaches "homes" from "home" is free to reach "homeses" from "homes",
// and the route resolver would look for a controller nobody ever wrote.
func TestPluralizeIdempotent(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"home", "session", "category", "day", "box", "branch", "class",
		"address", "status", "widget_set", "current_organization", "person",
		"child", "media", "sessions", "settings",
	} {
		once := Pluralize(in)
		if twice := Pluralize(once); twice != once {
			t.Errorf("Pluralize(Pluralize(%q)) = %q, want %q", in, twice, once)
		}
	}
}
