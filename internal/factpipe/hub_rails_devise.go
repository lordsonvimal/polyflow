package factpipe

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// hub_rails_devise.go is the "rails_devise" hub provider (see hub.go) — the
// Tier FX FX.8.28 migration of internal/linker/rails_devise.go's retired
// LinkDeviseDefaultRoutes (Phase DV.2).
//
// The roster's original caveat called this "actually node-minting" and
// disqualified it outright — that framing predates HubProvider (FX.8.44).
// Deciding Devise's default (non-overridden, non-skipped) route set needs a
// cross-file join no single tree-sitter pattern reaches — the model's
// `devise :module, ...` declaration lives in app/models/*.rb, a different
// file than routes.rb's `devise_for` call — real Go re-parsing both files,
// exactly what a hub is for. Ported near-verbatim (`rd`-prefixed).
//
// A synthesized node's controller_module is ALWAYS an explicit empty string
// (never absent) — LinkRailsRouteActions' successor, rails_route_actions.dl,
// distinguishes "no in-repo controller, but the module IS known" (moduleKnown
// = true, explicitModule = "") from "not derivable at all" (the meta key
// absent) — the former still resolves to a resource lookup and ledgers
// honestly; the latter falls through to path-segment namespace inference,
// which silently fails for a devise default route (its Meta["resource"] is
// the devise SCOPE, e.g. "sessions", never a literal path segment like
// "users"). `mint:`'s Meta overlay used to treat every resolved-empty value
// as "omit the key" (the "Go link passes build their meta maps
// conditionally" discipline) — buildNode was extended (FX.8.28) to keep a
// LITERAL "" (an author's explicit choice, not a column that happened to
// resolve empty) instead of dropping it, so this hub's `controller_module: ""`
// in the YAML actually reaches the node.
func init() { RegisterHub("rails_devise", railsDeviseHub) }

// deviseModuleForScope maps a Devise route scope to the model-declaration
// module symbol that enables it, ported verbatim.
var rdModuleForScope = map[string]string{
	"sessions":      "database_authenticatable",
	"registrations": "registerable",
	"passwords":     "recoverable",
	"confirmations": "confirmable",
	"unlocks":       "lockable",
}

const rdRouteMintPred = "rd_route_mint" // (ID, Label, Svc, File, Line, EndLine, Path, Method, Action, Resource)

func railsDeviseHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	if len(nodes) == 0 {
		return nil
	}
	svc := nodes[0].Service

	var routesFile string
	for _, f := range files {
		if rdIsDeviseRoutesFile(f) {
			routesFile = f
			break
		}
	}
	if routesFile == "" {
		return nil
	}
	mappings := rdParseDeviseForMappings(routesFile)
	if len(mappings) == 0 {
		return nil
	}
	modelModules := rdScanDeviseModelModules(files)

	scopes := make([]string, 0, len(rdModuleForScope))
	for s := range rdModuleForScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)

	var out []Fact
	seen := map[string]bool{}
	for _, m := range mappings {
		modelName := rdModelNameForScope(m.scopeArg)
		enabled := modelModules[modelName]
		if len(enabled) == 0 {
			continue
		}
		for _, scopeName := range scopes {
			if m.controllers[scopeName] || m.skip[scopeName] {
				continue
			}
			if !enabled[rdModuleForScope[scopeName]] {
				continue
			}
			for _, a := range railsinflect.DeviseScopeActions[scopeName] {
				path := strings.Replace(a.Path, "%s", m.scopeArg, 1)
				key := a.Method + " " + path
				if seen[key] {
					continue
				}
				seen[key] = true
				id := svc + ":" + routesFile + ":" + string(graph.NodeTypeHTTPHandler) + ":devise_default:" + key + ":" + strconv.Itoa(m.line)
				out = append(out, Fact{
					Pred: rdRouteMintPred,
					Args: []Atom{
						Node(id), Str(key), Str(svc), Str(routesFile), Int(int64(m.line)),
						Str(path), Str(a.Method), Str(a.Name), Str(scopeName),
					},
					Origin: Origin{Kind: OriginPrimitive, File: routesFile, Line: m.line, Pattern: rdRouteMintPred},
				})
			}
		}
	}
	return out
}

func rdModelNameForScope(scopeArg string) string {
	s := railsinflect.Singularize(scopeArg)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func rdIsDeviseRoutesFile(file string) bool {
	base := filepath.Base(file)
	if base == "routes.rb" {
		return true
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	return strings.HasSuffix(dir, "/config/routes") || strings.Contains(dir, "/config/routes/")
}

type rdDeviseForMapping struct {
	scopeArg    string
	line        int
	controllers map[string]bool
	skip        map[string]bool
}

func rdParseDeviseForMappings(file string) []rdDeviseForMapping {
	src, root, release, ok := rdReadAndParseRuby(file)
	if !ok {
		return nil
	}
	defer release()

	var out []rdDeviseForMapping
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "call" {
			if mn := n.ChildByFieldName("method"); mn != nil && mn.Content(src) == "devise_for" {
				if scope, ok := rdDeviseForScopeArg(n, src); ok {
					out = append(out, rdDeviseForMapping{
						scopeArg:    scope,
						line:        int(n.StartPoint().Row) + 1,
						controllers: rdDeviseForControllerKeys(n, src),
						skip:        rdDeviseForSkipKeys(n, src),
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

func rdDeviseForScopeArg(call *sitter.Node, src []byte) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	first := args.NamedChild(0)
	if first.Type() != "simple_symbol" {
		return "", false
	}
	return rdSymbolName(first.Content(src)), true
}

func rdDeviseForControllerKeys(call *sitter.Node, src []byte) map[string]bool {
	out := map[string]bool{}
	hash := rdKeywordValue(call, src, "controllers")
	if hash == nil || hash.Type() != "hash" {
		return out
	}
	for i := 0; i < int(hash.NamedChildCount()); i++ {
		p := hash.NamedChild(i)
		if p == nil || p.Type() != "pair" {
			continue
		}
		if key := p.ChildByFieldName("key"); key != nil {
			out[rdHashKeySymbolText(key, src)] = true
		}
	}
	return out
}

func rdDeviseForSkipKeys(call *sitter.Node, src []byte) map[string]bool {
	out := map[string]bool{}
	v := rdKeywordValue(call, src, "skip")
	if v == nil {
		return out
	}
	for _, s := range rdSymbolList(v, src) {
		out[s] = true
	}
	return out
}

func rdKeywordValue(call *sitter.Node, src []byte, want string) *sitter.Node {
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
		if rdHashKeySymbolText(key, src) == want {
			return val
		}
	}
	return nil
}

func rdHashKeySymbolText(n *sitter.Node, src []byte) string {
	return strings.TrimSuffix(strings.TrimPrefix(n.Content(src), ":"), ":")
}

func rdScanDeviseModelModules(files []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, f := range files {
		if filepath.Ext(f) != ".rb" || !strings.Contains(filepath.ToSlash(f), "/app/models/") {
			continue
		}
		func() {
			src, root, release, ok := rdReadAndParseRuby(f)
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
						mods := rdDeviseModuleSymbols(n.ChildByFieldName("arguments"), src)
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

func rdDeviseModuleSymbols(args *sitter.Node, src []byte) []string {
	if args == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if c := args.NamedChild(i); c.Type() == "simple_symbol" {
			out = append(out, rdSymbolName(c.Content(src)))
		}
	}
	return out
}

func rdSymbolName(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), ":") }

func rdSymbolList(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	switch n.Type() {
	case "simple_symbol":
		return []string{rdSymbolName(n.Content(src))}
	case "array", "symbol_array":
		var out []string
		for i := 0; i < int(n.NamedChildCount()); i++ {
			ch := n.NamedChild(i)
			switch ch.Type() {
			case "simple_symbol":
				out = append(out, rdSymbolName(ch.Content(src)))
			case "bare_symbol", "string":
				out = append(out, rdSymbolName(strings.Trim(ch.Content(src), `"'`)))
			}
		}
		return out
	}
	return nil
}

// rdReadAndParseRuby reads file off disk and parses it with the ruby
// tree-sitter grammar. release must be called once done with root.
func rdReadAndParseRuby(file string) (src []byte, root *sitter.Node, release func(), ok bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, func() {}, false
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, data)
	if err != nil || tree == nil {
		return nil, nil, func() {}, false
	}
	return data, tree.RootNode(), func() { tree.Close() }, true
}
