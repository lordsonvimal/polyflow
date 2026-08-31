package linker

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// deviseModuleForScope maps a Devise route scope to the model-declaration
// module symbol that enables it (docs/rails-devise-gem-plan.md's Pinned
// Interfaces table). Only Devise-core scopes: devise_invitable's
// `invitations` and devise-security's `password_expired` are explicitly out
// of scope for DV.2's default-route synthesis (the plan's Non-goals section —
// their *own* default action sets are unverified third-party gem behavior,
// not Devise's). DV.1 already covers both when named in `controllers:`,
// which is the only routing either needs in the fleet today.
var deviseModuleForScope = map[string]string{
	"sessions":      "database_authenticatable",
	"registrations": "registerable",
	"passwords":     "recoverable",
	"confirmations": "confirmable",
	"unlocks":       "lockable",
}

// LinkDeviseDefaultRoutes synthesizes http_handler nodes for Devise's default
// (non-overridden, non-skipped) scopes — Phase DV.2.
//
// Unlike DV.1 (a parser-level synthesis entirely self-contained in
// routes.rb's own `controllers:`/`skip:` hash — see
// internal/parser/ruby_route_paths.go's emitDeviseRoutes), the default scope
// set genuinely depends on which modules the mapped model includes: a model
// without `:recoverable` gets no password routes from `devise_for`, override
// or not. That is a cross-file join — the model's `devise :module1, ...`
// declaration lives in app/models/*.rb, a different file than routes.rb — and
// internal/parser's Parse runs one file at a time, so it cannot see both
// while walking either one. This runs as a linker pass instead, re-reading
// both files directly off disk, the same reason LinkRailsFilters already
// re-parses controller/model files rather than depending on structural parse
// output alone.
//
// A synthesized node here always carries an empty controller_module and a
// resource name with no in-repo controller behind it —
// LinkRailsRouteActions correctly falls through to
// UnresolvedRailsRouteAction for it, the same honest "exists in the graph,
// unresolved in this codebase" state docs/django-filter-chain-plan.md's DJ.1
// established for a route to framework/stdlib code this codebase never
// parses.
func LinkDeviseDefaultRoutes(serviceFiles map[string][]string) []graph.Node {
	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames) // map order must never reach output

	var out []graph.Node
	for _, svc := range svcNames {
		out = append(out, synthesizeServiceDeviseDefaults(svc, serviceFiles[svc])...)
	}
	return out
}

func synthesizeServiceDeviseDefaults(svc string, files []string) []graph.Node {
	var routesFile string
	for _, f := range files {
		if isDeviseRoutesFile(f) {
			routesFile = f
			break
		}
	}
	if routesFile == "" {
		return nil
	}
	mappings := parseDeviseForMappings(routesFile)
	if len(mappings) == 0 {
		return nil
	}
	modelModules := scanDeviseModelModules(files)

	scopes := make([]string, 0, len(deviseModuleForScope))
	for s := range deviseModuleForScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	var out []graph.Node
	seen := map[string]bool{}
	for _, m := range mappings {
		modelName := modelNameForScope(m.scopeArg)
		enabled := modelModules[modelName]
		if len(enabled) == 0 {
			continue
		}
		for _, scopeName := range scopes {
			if m.controllers[scopeName] || m.skip[scopeName] {
				continue // DV.1's territory, or explicitly dropped
			}
			if !enabled[deviseModuleForScope[scopeName]] {
				continue
			}
			for _, a := range railsinflect.DeviseScopeActions[scopeName] {
				path := strings.Replace(a.Path, "%s", m.scopeArg, 1)
				key := a.Method + " " + path
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, graph.Node{
					ID:       svc + ":" + routesFile + ":" + string(graph.NodeTypeHTTPHandler) + ":devise_default:" + key + ":" + strconv.Itoa(m.line),
					Type:     graph.NodeTypeHTTPHandler,
					Label:    key,
					Service:  svc,
					File:     routesFile,
					Line:     m.line,
					EndLine:  m.line,
					Language: "ruby",
					Meta: map[string]string{
						"pattern":           "devise_default_route",
						"path":              path,
						"full_path":         path,
						"method":            a.Method,
						"action":            a.Name,
						"resource":          scopeName,
						"controller_module": "",
					},
				})
			}
		}
	}
	return out
}

// modelNameForScope inflects `devise_for`'s scope argument to the model
// constant name Rails' own convention derives it from: `:users` → "User".
// Reuses railsinflect.Singularize rather than a fresh inflector — per the
// plan's own risk note, this shortcut ("s"-stripping) is not a general
// inflector, only sufficient for both live repos, where the scope name
// matches the model name exactly (see the plan's Non-goals: no class_name:
// override support).
func modelNameForScope(scopeArg string) string {
	s := railsinflect.Singularize(scopeArg)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// isDeviseRoutesFile mirrors internal/parser's isRailsRoutesFile gate
// (unexported there, so restated here rather than exported solely for one
// caller): config/routes.rb itself, or any file under config/routes/.
func isDeviseRoutesFile(file string) bool {
	base := filepath.Base(file)
	if base == "routes.rb" {
		return true
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	return strings.HasSuffix(dir, "/config/routes") || strings.Contains(dir, "/config/routes/")
}

// deviseForMapping is one `devise_for` call's scope argument plus the parts
// of it DV.1 already claims — the override hash's keys and the skip list —
// so DV.2 knows which scopes are NOT its territory.
type deviseForMapping struct {
	scopeArg    string
	line        int
	controllers map[string]bool
	skip        map[string]bool
}

// parseDeviseForMappings re-parses routes.rb and collects every `devise_for`
// call's scope/controllers/skip. Deliberately does not track the
// namespace/scope prefix stack internal/parser's routeWalker does — a
// default-scope node's controller_module is always "" per this phase's own
// contract (there is no in-repo controller to namespace it against).
func parseDeviseForMappings(file string) []deviseForMapping {
	src, root, release, ok := rubyParse(file)
	if !ok {
		return nil
	}
	defer release()

	var out []deviseForMapping
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "call" {
			if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(src) == "devise_for" {
				if scope, ok := deviseForScopeArg(n, src); ok {
					out = append(out, deviseForMapping{
						scopeArg:    scope,
						line:        int(n.StartPoint().Row) + 1,
						controllers: deviseForControllerKeys(n, src),
						skip:        deviseForSkipKeys(n, src),
					})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// deviseForScopeArg reads `devise_for`'s first positional argument — the
// mapping symbol (`:users`).
func deviseForScopeArg(call *sitter.Node, src []byte) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	first := args.NamedChild(0)
	if first.Type() != "simple_symbol" {
		return "", false
	}
	return symbolName(first.Content(src)), true
}

// deviseForControllerKeys reads the *keys* of `controllers: { scope: "..." }`
// — DV.2 only needs to know which scopes DV.1 already claims, not the
// override target itself.
func deviseForControllerKeys(call *sitter.Node, src []byte) map[string]bool {
	out := map[string]bool{}
	hash := deviseKeywordValue(call, src, "controllers")
	if hash == nil || hash.Type() != "hash" {
		return out
	}
	for i := 0; i < int(hash.NamedChildCount()); i++ {
		p := hash.NamedChild(i)
		if p == nil || p.Type() != "pair" {
			continue
		}
		if key := p.ChildByFieldName("key"); key != nil {
			out[hashKeySymbolText(key, src)] = true
		}
	}
	return out
}

// deviseForSkipKeys reads `skip:` — a bare symbol or an array of symbols
// naming scopes `devise_for` generates no route for at all.
func deviseForSkipKeys(call *sitter.Node, src []byte) map[string]bool {
	out := map[string]bool{}
	v := deviseKeywordValue(call, src, "skip")
	if v == nil {
		return out
	}
	for _, s := range symbolList(v, src) {
		out[s] = true
	}
	return out
}

// deviseKeywordValue returns the raw value node of a top-level keyword
// argument (`key: value`) on call, or nil when absent.
func deviseKeywordValue(call *sitter.Node, src []byte, want string) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() != "pair" {
			continue
		}
		key, val := c.ChildByFieldName("key"), c.ChildByFieldName("value")
		if key == nil || val == nil {
			continue
		}
		if hashKeySymbolText(key, src) == want {
			return val
		}
	}
	return nil
}

// hashKeySymbolText reads a hash-pair key's bare name, regardless of which of
// Ruby's two equivalent spellings tree-sitter produced: `sessions: "x"`
// parses the key as a colon-less hash_key_symbol, `:sessions => "x"` as a
// simple_symbol carrying the leading colon — trimming both ends of ":"
// normalizes either to the bare name.
func hashKeySymbolText(n *sitter.Node, src []byte) string {
	return strings.TrimSuffix(strings.TrimPrefix(n.Content(src), ":"), ":")
}

// scanDeviseModelModules reads every app/models/*.rb file's `devise
// :module1, :module2, ...` class-body macro call, recording the enabled
// module symbols against the model's simple class name. Keyed by simple name
// rather than a fully-qualified constant path: neither live repo namespaces
// its Devise-mapped models (see the plan's Non-goals — no class_name:/module:
// override support needed), so the ambiguity a qualified-name lookup guards
// against (rails_filters.go's ctrlClass.qualified) does not arise here.
func scanDeviseModelModules(files []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, f := range files {
		if filepath.Ext(f) != ".rb" || !strings.Contains(filepath.ToSlash(f), "/app/models/") {
			continue
		}
		func() {
			src, root, release, ok := rubyParse(f)
			if !ok {
				return
			}
			defer release()

			var className string
			var walk func(n *sitter.Node)
			walk = func(n *sitter.Node) {
				if n == nil {
					return
				}
				switch n.Type() {
				case "class":
					if nameNode := n.ChildByFieldName("name"); nameNode != nil {
						parts := strings.Split(nameNode.Content(src), "::")
						className = parts[len(parts)-1]
					}
				case "call":
					if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(src) == "devise" && className != "" {
						mods := deviseModuleSymbols(n.ChildByFieldName("arguments"), src)
						if len(mods) > 0 {
							if out[className] == nil {
								out[className] = map[string]bool{}
							}
							for _, m := range mods {
								out[className][m] = true
							}
						}
					}
				}
				for i := 0; i < int(n.NamedChildCount()); i++ {
					walk(n.NamedChild(i))
				}
			}
			walk(root)
		}()
	}
	return out
}

// deviseModuleSymbols collects `devise`'s positional symbol arguments
// (`devise :database_authenticatable, :recoverable, ...`) — unlike
// symbolList's single-value shape (one bare symbol or one array), this is
// always multiple bare positional symbols side by side.
func deviseModuleSymbols(args *sitter.Node, src []byte) []string {
	if args == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if c := args.NamedChild(i); c.Type() == "simple_symbol" {
			out = append(out, symbolName(c.Content(src)))
		}
	}
	return out
}
