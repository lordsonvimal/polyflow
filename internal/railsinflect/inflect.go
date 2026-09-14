// Package railsinflect holds Rails naming-convention data with no source of
// its own to parse: the handful of English inflections Rails' own defaults
// cover, and Devise's per-scope default route table.
//
// It is a leaf package on purpose, the same reason internal/railsview is one
// (see that package's doc comment): internal/linker must never import
// internal/parser, because internal/parser's own in-package tests already
// import internal/linker. Before this package existed, three call sites —
// internal/parser/ruby_route_paths.go's route-name composition,
// internal/linker/rails_devise.go's Devise default-route synthesis, and
// internal/linker/ruby_associations.go's has_many singularization — each
// carried their own byte-for-byte copy of the same inflection table because
// there was nowhere neutral to put it. Anything here must stay free of
// dependencies on parser or linker so it can sit below both.
package railsinflect

import (
	"strings"
	"unicode"
)

// railsIrregularSingulars are the plurals no suffix rule reaches. They matter
// more than a purely cosmetic wart would suggest: a route *name* or an
// ActiveRecord association's class name is looked up verbatim
// (`person_path`, `has_many :people` → `Person`), so an inflection miss is a
// missing link, not a rendering nit.
var railsIrregularSingulars = map[string]string{
	"people": "person", "men": "man", "women": "woman", "children": "child",
	"mice": "mouse", "oxen": "ox", "teeth": "tooth", "feet": "foot",
	"geese": "goose", "data": "datum", "criteria": "criterion", "media": "medium",
}

// Singularize applies the handful of English inflections Rails' own defaults
// cover, for route parameters/names, ActiveRecord association targets, and
// Devise scope-to-model inflection. It is deliberately not a full inflector:
// the "-ves → -f" rule the general case would want (leaves → leaf) is a net
// loss on real resource names, where "archives" and "moves" are common and
// "leaves" is not.
func Singularize(s string) string {
	if irr, ok := railsIrregularSingulars[s]; ok {
		return irr
	}
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	// Rails' own rule here is `(x|ch|ss|sh)es$` — note "ss" (double s), not a
	// bare "s": "classes"/"glasses" double their final consonant before "es"
	// and need both stripped, but "licenses"/"houses"/"phases"/"responses"
	// already end their singular in a single "se" and need only the plain
	// trailing-"s" rule below (found live: "user_licenses" was singularizing
	// to "user_licens", not "user_license" — this collapsed a whole class of
	// "-se" nouns, not just that one word). "buses" is the one real word this
	// loses (Rails hardcodes it as an irregular; not worth one for a name no
	// repo here uses).
	case strings.HasSuffix(s, "sses"), strings.HasSuffix(s, "xes"),
		strings.HasSuffix(s, "zes"), strings.HasSuffix(s, "ches"),
		strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "us"):
		// Already singular: status, campus, bonus.
		return s
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return s[:len(s)-1]
	}
	return s
}

// railsIrregularPlurals is railsIrregularSingulars read the other way. It is
// not a second table to keep in sync — it is derived from the first one, so a
// word can never pluralize and singularize inconsistently.
var railsIrregularPlurals = func() map[string]string {
	out := make(map[string]string, len(railsIrregularSingulars))
	for plural, singular := range railsIrregularSingulars {
		out[singular] = plural
	}
	return out
}()

// Pluralize is Singularize's inverse over the same narrow set of rules Rails'
// own defaults cover. It exists for one job: Rails maps the *singular*
// `resource :session` declaration onto the *plural* SessionsController, so a
// route resolver holding the declaration's name has to reach the plural
// spelling to find the controller on disk.
//
// Already-plural input is returned unchanged, because a routes file may
// legitimately write `resource :settings` for a singleton whose controller is
// SettingsController. "Already plural" is decided by asking Singularize: a
// word it changes was plural to begin with.
//
// Like Singularize this is deliberately not a full inflector. A word it gets
// wrong ("analysis" → "analysiss", not "analyses") produces a controller path
// that matches nothing on disk, so the caller falls through to its unresolved
// ledger — an inflection miss costs a missing edge, never a wrong one. Resist
// growing an irregular-noun table here until a real corpus demands one.
//
//	Pluralize("home")     == "homes"
//	Pluralize("category") == "categories"
//	Pluralize("box")      == "boxes"
//	Pluralize("sessions") == "sessions"   // already plural, identity
func Pluralize(s string) string {
	if s == "" {
		return s
	}
	if p, ok := railsIrregularPlurals[s]; ok {
		return p
	}
	if Singularize(s) != s {
		return s // already plural
	}
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !isVowel(s[len(s)-2]):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"),
		strings.HasSuffix(s, "z"), strings.HasSuffix(s, "ch"),
		strings.HasSuffix(s, "sh"):
		return s + "es"
	}
	return s + "s"
}

// Underscore is ActiveSupport's underscore() for the shapes a class name can
// actually take: it demodulizes (Admin::Report → Report — Rails serves it from
// `reports` unless the module declares a table_name_prefix, an explicit
// declaration read separately), then converts CamelCase to snake_case with the
// usual acronym handling (APIKey → api_key).
func Underscore(className string) string {
	if i := strings.LastIndex(className, "::"); i >= 0 {
		className = className[i+2:]
	}
	rs := []rune(className)
	var b strings.Builder
	for i, r := range rs {
		if !unicode.IsUpper(r) {
			b.WriteRune(r)
			continue
		}
		prevLowerOrDigit := i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]))
		endsAcronym := i > 0 && unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if prevLowerOrDigit || endsAcronym {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Classify converts a snake_case name to the PascalCase class name Rails'
// naming convention pairs it with (`deliverable` → `Deliverable`,
// `lyra_batch_job` → `LyraBatchJob`). The inverse direction of Underscore,
// for an ActiveRecord association's bare symbol
// (`has_many :deliverables` → singularize, then classify) or an explicit
// `class_name:`-free belongs_to/has_one target.
func Classify(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// TableNameCandidates returns the conventional table names for a class name,
// most specific first — the regular pluralization forms only, for a caller
// that validates each against the declared table set (an irregular plural like
// Person → people produces no hit and the caller ledgers, which is the correct
// outcome: an inflection miss costs a missing edge, never a wrong one). This is
// ActiveRecord's `tableize` restricted to the forms Rails' default pluralizer
// reaches without an irregular-noun table.
func TableNameCandidates(className string) []string {
	base := Underscore(className)
	if base == "" {
		return nil
	}
	switch {
	case strings.HasSuffix(base, "y") && len(base) > 1 && !isVowel(base[len(base)-2]):
		return []string{base[:len(base)-1] + "ies"}
	case strings.HasSuffix(base, "s"), strings.HasSuffix(base, "x"),
		strings.HasSuffix(base, "z"), strings.HasSuffix(base, "ch"),
		strings.HasSuffix(base, "sh"):
		return []string{base + "es"}
	case strings.HasSuffix(base, "fe"):
		return []string{base[:len(base)-2] + "ves", base + "s"}
	case strings.HasSuffix(base, "f"):
		return []string{base[:len(base)-1] + "ves", base + "s"}
	}
	return []string{base + "s"}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

// DeviseAction is one action `devise_for` implicitly routes for a given scope
// (sessions, registrations, ...). Unlike a plain REST resource's actions, the
// path is not derivable from a generic member/collection shape — Devise names
// its own routes (`/users/sign_in`, `/users/password/new`) independent of
// REST convention — so each scope carries its own literal path template, `%s`
// standing in for the mapping's scope argument (`:users`).
type DeviseAction struct {
	Name   string
	Method string
	Path   string // %s == scope arg, e.g. "users"
}

// DeviseScopeActions is Devise's per-scope default action/path table (see
// docs/rails-devise-gem-plan.md's Pinned Interfaces, verified against 5.0.4
// route-generation behavior). It also carries `invitations` (devise_invitable)
// and `password_expired` (devise-security's password_expirable) — not Devise
// core, but named directly in orion's `controllers:` override hash, so
// DV.1 must be able to route them the same way it routes core scopes (see
// the plan's Non-goals section: their *own* default/non-overridden route set
// is out of scope, only resolving an explicit controllers: override is).
//
// Shared by DV.1 (internal/parser/ruby_route_paths.go's emitDeviseRoutes,
// controllers: overrides) and DV.2 (internal/linker/rails_devise.go's
// LinkDeviseDefaultRoutes, the default non-overridden scopes) so the two
// phases can never drift into synthesizing different paths for the same
// scope.
var DeviseScopeActions = map[string][]DeviseAction{
	"sessions": {
		{"new", "GET", "/%s/sign_in"},
		{"create", "POST", "/%s/sign_in"},
		{"destroy", "DELETE", "/%s/sign_out"},
	},
	"registrations": {
		{"new", "GET", "/%s/sign_up"},
		{"create", "POST", "/%s"},
		{"edit", "GET", "/%s/edit"},
		{"update", "PATCH", "/%s"},
		{"destroy", "DELETE", "/%s"},
		{"cancel", "GET", "/%s/cancel"},
	},
	"passwords": {
		{"new", "GET", "/%s/password/new"},
		{"create", "POST", "/%s/password"},
		{"edit", "GET", "/%s/password/edit"},
		{"update", "PATCH", "/%s/password"},
	},
	"confirmations": {
		{"new", "GET", "/%s/confirmation/new"},
		{"create", "POST", "/%s/confirmation"},
		{"show", "GET", "/%s/confirmation"},
	},
	"unlocks": {
		{"new", "GET", "/%s/unlock/new"},
		{"create", "POST", "/%s/unlock"},
		{"show", "GET", "/%s/unlock"},
	},
	"invitations": {
		{"new", "GET", "/%s/invitation/new"},
		{"create", "POST", "/%s/invitation"},
		{"edit", "GET", "/%s/invitation/accept"},
		{"update", "PUT", "/%s/invitation"},
	},
	"password_expired": {
		{"edit", "GET", "/%s/password_expired/edit"},
		{"update", "PUT", "/%s/password_expired"},
	},
}
