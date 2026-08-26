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

import "strings"

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
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"),
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
