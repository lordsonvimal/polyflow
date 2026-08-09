package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// nameScope is the *third* stack the route walker threads, alongside the URL
// prefix and the controller module. Rails' route name — the thing that becomes
// `study_deliverable_path` — is composed from yet another subset of the
// grouping constructs, and no two of the three subsets agree:
//
//	namespace :client_api    path ✓   module ✓   name ✓
//	scope "app"              path ✓   module ✗   name ✗
//	scope module: "admin"    path ✗   module ✓   name ✗
//	scope as: "admin"        path ✗   module ✗   name ✓
//	resources :studies (as parent of a nested resource)
//	                         path ✓   module ✗   name ✓ (singularized)
//
// That last row is why a name cannot be recovered from a path after the fact:
// a nested parent contributes `study` to the name but `studies/:study_id` to
// the URL, and `scope "app"` contributes to the URL and nothing else. Before
// this stack existed the helper map reconstructed `/folders/:id` for
// `folder_path` from the resource name alone — correct for a toy app, and
// wrong for all 549 of orion's routes, every one of which lives under
// `scope "app"`.
//
// singular/plural hold the *innermost enclosing resource*, kept apart from
// parent because Rails names a member route after the singular and a
// collection route after the plural of the very same resource:
//
//	resources :users do
//	  member     do get :sync end   → sync_user_path
//	  collection do get :recent end → recent_users_path
//	end
type nameScope struct {
	parent   []string // name segments above the innermost resource
	singular string   // innermost resource, singular form ("" if not in one)
	plural   string   // innermost resource, plural form ("" if not in one)
}

// descend enters a construct that contributes a literal name segment
// (`namespace :admin`, `scope as: "admin"`). Any enclosing resource is
// flattened into parent first: `resources :users do namespace :admin do
// resources :notes end end` names its innermost route `user_admin_notes_path`.
func (ns nameScope) descend(seg string) nameScope {
	out := nameScope{parent: ns.flattened()}
	if seg != "" {
		out.parent = append(out.parent, seg)
	}
	return out
}

// enterResource enters a `resources`/`resource` block. The declaration's own
// name base is read at the *call site* scope (see restHelperName); this is the
// scope its children see.
func (ns nameScope) enterResource(singular, plural string) nameScope {
	return nameScope{parent: ns.flattened(), singular: singular, plural: plural}
}

// flattened folds the innermost resource's singular form into parent, copying
// so sibling scopes cannot overwrite each other's segments (the same
// shared-backing-array trap appendSeg guards for the path stack).
func (ns nameScope) flattened() []string {
	out := make([]string, len(ns.parent), len(ns.parent)+1)
	copy(out, ns.parent)
	if ns.singular != "" {
		out = append(out, ns.singular)
	}
	return out
}

// memberBase is the name a member route is qualified by: the singular of the
// enclosing resource, under the full parent prefix. Empty when there is no
// enclosing resource, which makes the route unnamed rather than misnamed.
func (ns nameScope) memberBase() string {
	if ns.singular == "" {
		return ""
	}
	return joinName(append(ns.parentCopy(), ns.singular))
}

// collectionBase is the plural counterpart of memberBase.
func (ns nameScope) collectionBase() string {
	if ns.plural == "" {
		return ""
	}
	return joinName(append(ns.parentCopy(), ns.plural))
}

func (ns nameScope) parentCopy() []string {
	out := make([]string, len(ns.parent), len(ns.parent)+1)
	copy(out, ns.parent)
	return out
}

// joinName assembles Rails' underscore-joined route name, dropping empties so
// a scope that contributed nothing leaves no double underscore behind.
func joinName(segs []string) string {
	var kept []string
	for _, s := range segs {
		if s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, "_")
}

// restHelperName is the route name Rails generates for one of the seven REST
// actions of a `resources`/`resource` declaration, evaluated in the scope the
// declaration itself sits in.
//
//	scope "app" { resources :studies { resources :deliverables } }
//	  index  → study_deliverables    show → study_deliverable
//	  new    → new_study_deliverable  edit → edit_study_deliverable
//
// create shares index's name and update/destroy share show's, exactly as
// `rails routes` prints them.
func restHelperName(ns nameScope, action, singular, plural string) string {
	base := ns.flattened()
	switch action {
	case "index", "create":
		if plural == "" {
			return ""
		}
		return joinName(append(base, plural))
	case "new", "edit":
		if singular == "" {
			return ""
		}
		return action + "_" + joinName(append(base, singular))
	default: // show, update, destroy
		if singular == "" {
			return ""
		}
		return joinName(append(base, singular))
	}
}

// pathHelperName is Rails' auto-generated name for a string-literal route:
// `get "audit_logs"` inside `scope "app"` is `audit_logs_path`, not
// `app_audit_logs_path`, because the scope contributes no name.
//
// A path with a dynamic segment gets no auto-name from Rails (there is nothing
// to call it), so it yields "" here and the route stays nameless rather than
// acquiring an invented helper that some view might collide with.
func pathHelperName(ns nameScope, literal string) string {
	literal = strings.Trim(strings.Trim(literal, `"'`), "/")
	if literal == "" || strings.Contains(literal, ":") || strings.Contains(literal, "*") {
		return ""
	}
	segs := ns.flattened()
	segs = append(segs, strings.Split(literal, "/")...)
	return joinName(segs)
}

// keywordSegment reads a literal keyword argument off a call
// (`as: "collection"`, `as: :admin`), returning "" when it is absent or not a
// literal.
func keywordSegment(call *sitter.Node, src []byte, want string) string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.ChildCount()); i++ {
		c := args.Child(i)
		if c == nil || c.Type() != "pair" {
			continue
		}
		key, value := c.ChildByFieldName("key"), c.ChildByFieldName("value")
		if key == nil || value == nil {
			continue
		}
		k := strings.TrimSuffix(strings.TrimPrefix(string(src[key.StartByte():key.EndByte()]), ":"), ":")
		if k == want {
			return literalSegment(value, src)
		}
	}
	return ""
}
