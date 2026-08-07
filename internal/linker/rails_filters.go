package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkRailsFilters turns the Rails filter chain into edges.
//
// `before_action :ensure_valid_token` is close to the most common line in a
// Rails controller and it used to produce nothing walkable: the pattern matcher
// minted a *function* node labelled `before_action`, and the two roles it
// captured (action_callback_type, action_callback_name) were read by no code at
// all. So the authorization that runs before every request in the file was
// absent from the graph — a trace rooted at an action showed the render, the
// queue names and the S3 call, and never the token check that gates them.
//
// This pass emits the call the registration stands for:
//
//	AgentsController          --calls--> ensure_valid_token   scope=class
//	AgentsController#register --calls--> ensure_valid_token   scope=action
//
// Both, because they answer different questions. The class edge is what the
// source literally says and is the only edge a base controller can produce (it
// declares filters and defines no actions of its own). The action edges are what
// a request actually does, and they are the ones `only:`/`except:` narrow — the
// whole point of those options is that the filter chain differs per action.
//
// # Why this re-parses instead of reading the pattern node
//
// The pattern captured one symbol and no options. A registration routinely names
// several callbacks and restricts them (`before_action :a, :b, only: %i[create]`),
// a third of them name a lambda instead of a symbol, the callback is usually
// defined in a superclass or a concern rather than the controller, and deciding
// which methods are *actions* needs the `private` marker — none of which
// survives into a MatchResult. Re-parsing the controller tree is what
// LinkRubyTypeRelations and LinkRailsViews already do for the same reason.
//
// # Inheritance
//
// Filters bind to the actions of the class that declares them *and* of every
// subclass, because that is where almost all of them land: orion's
// authentication, session check and breadcrumbs are six lines in
// ApplicationController and SecuredController, and not one of the actions they
// guard is in either file. An inherited edge carries `inherited_from` so the
// subclass's file, which says nothing about the filter, still points at where to
// read it, and is never better than `inferred` because the superclass chain is
// reconstructed from constant names.
//
// Inheritance is what finally makes `skip_before_action` mean something. It is
// the only thing standing between `before_action :authenticate_user!` and an
// edge onto every action in the app, including the login form and the public
// landing page — the endpoints where being wrong about authentication matters
// most. Skips are collected down the same chain, and a skip carrying
// `only:`/`except:` retracts per action rather than wholesale.
//
// Only the superclass chain propagates. A module cannot legally declare a filter
// in its body — the spelling is `included do`, which is ledgered — so `include`
// contributes methods to look up, never registrations to inherit.
func LinkRailsFilters(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames) // bug-class #2: map order must never reach output

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	for _, svc := range svcNames {
		files := controllerFiles(serviceFiles[svc])
		if len(files) == 0 {
			continue // not a Rails app
		}
		ix := newFilterIndex(nodes, svc, files)
		e, u := ix.link()
		edges = append(edges, e...)
		unresolved = append(unresolved, u...)
	}
	return edges, unresolved
}

// controllerFiles returns every Ruby file under an app/controllers tree, sorted.
//
// Not just `*_controller.rb`: app/controllers/concerns holds the modules that
// define the callbacks, and their `include`s are needed to walk the ancestor
// chain that resolves them.
func controllerFiles(files []string) []string {
	var out []string
	for _, f := range files {
		if filepath.Ext(f) != ".rb" {
			continue
		}
		if !strings.Contains(filepath.ToSlash(f), "/app/controllers/") {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// parsed shape
// ---------------------------------------------------------------------------

// filterReg is one `before_action`-family registration.
type filterReg struct {
	kind        string // before_action, around_action, ...
	line        int
	callbacks   []string // every bare symbol argument, in source order
	only        []string
	except      []string
	conditional bool // carries if:/unless:, so it may not run
	inline      bool // callbacks came from a block/lambda body, not a symbol
}

// ctrlAction is a public instance method — a request-dispatchable action.
type ctrlAction struct {
	name string
	line int
}

// ctrlClass is one class body in a controller file.
type ctrlClass struct {
	name string
	ns   []string // enclosing module/class names, outermost first
	file string
	line int

	prepends []string // prepend Foo, source order
	includes []string // include/extend Foo, source order
	super    string   // last component, for the simple-name method table
	superRef string   // as written: `ClientApi::V1::ApiBaseController`
	actions  []ctrlAction
	filters  []filterReg
	skips    []filterReg // skip_before_action &c — only meaningful once filters inherit
}

// qualified is the constant path Ruby knows this class by.
func (c *ctrlClass) qualified() string {
	if len(c.ns) == 0 {
		return c.name
	}
	return strings.Join(c.ns, "::") + "::" + c.name
}

// ancestorNames is the class's own lookup order for a method it does not
// define: prepended modules beat the class, later includes beat earlier ones,
// and the superclass is last. This mirrors Ruby's ancestor list closely enough
// that the common shapes (one superclass, a couple of concerns) resolve exactly.
func (c *ctrlClass) ancestorNames() []string {
	var out []string
	for i := len(c.prepends) - 1; i >= 0; i-- {
		out = append(out, c.prepends[i])
	}
	for i := len(c.includes) - 1; i >= 0; i-- {
		out = append(out, c.includes[i])
	}
	if c.super != "" {
		out = append(out, c.super)
	}
	return out
}

// ---------------------------------------------------------------------------
// index
// ---------------------------------------------------------------------------

type filterIndex struct {
	svc string

	classes  []*ctrlClass            // every class body, sorted by file+line
	byName   map[string][]*ctrlClass // simple name → declarations (collision-aware)
	byQual   map[string][]*ctrlClass // full constant path → declarations
	methodQN map[string][]string     // "Class#method" → method node IDs
	classID  map[string]string       // file\x00Name\x00line → class node ID

	// filter calls found in a file, keyed file\x00line, minus the ones a class
	// body claimed — what is left is ledgered rather than dropped.
	strayFilters map[string]int
}

func newFilterIndex(nodes []graph.Node, svc string, files []string) *filterIndex {
	ix := &filterIndex{
		svc:          svc,
		byName:       map[string][]*ctrlClass{},
		byQual:       map[string][]*ctrlClass{},
		methodQN:     map[string][]string{},
		classID:      map[string]string{},
		strayFilters: map[string]int{},
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Service != svc || n.Language != "ruby" || n.Meta[graph.MetaIsTest] == "true" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			if qn := n.Meta["qualified_name"]; qn != "" && strings.Contains(qn, "#") {
				ix.methodQN[qn] = append(ix.methodQN[qn], n.ID)
			}
		case graph.NodeTypeClass:
			key := fmt.Sprintf("%s\x00%s\x00%d", n.File, n.Label, n.Line)
			if _, dup := ix.classID[key]; !dup {
				ix.classID[key] = n.ID
			}
		}
	}
	for qn := range ix.methodQN {
		sort.Strings(ix.methodQN[qn])
	}

	for _, f := range files {
		ix.scanFile(f)
	}
	sort.SliceStable(ix.classes, func(i, j int) bool {
		if ix.classes[i].file != ix.classes[j].file {
			return ix.classes[i].file < ix.classes[j].file
		}
		return ix.classes[i].line < ix.classes[j].line
	})
	for _, c := range ix.classes {
		ix.byName[c.name] = append(ix.byName[c.name], c)
		ix.byQual[c.qualified()] = append(ix.byQual[c.qualified()], c)
	}
	return ix
}

// resolveSuper finds the class a `< Super` reference names, the way Ruby does:
// innermost enclosing namespace first, then outward, then top level.
//
// Simple-name keying is not good enough here and the failure is silent.
// orion has two FilesResourcesControllers — one top level under
// RepositoryController, one under ClientApi::V1 — and sorting by file put the
// namespaced one first, so `FilesController < FilesResourcesController` walked
// into ClientApi::V1's hierarchy and never reached SecuredController. Every
// action in the file lost `authenticate_user!` and gained a chain it does not
// have, with nothing anywhere reporting a problem.
func (ix *filterIndex) resolveSuper(c *ctrlClass) []*ctrlClass {
	ref := c.superRef
	if ref == "" {
		return nil
	}
	for i := len(c.ns); i >= 0; i-- {
		key := ref
		if i > 0 {
			key = strings.Join(c.ns[:i], "::") + "::" + ref
		}
		if decls := ix.byQual[key]; len(decls) > 0 {
			return decls
		}
	}
	// A superclass this pass never parsed (a gem, or a controller outside
	// app/controllers). Falling back to the simple name is what produced the
	// FilesController mix-up, so it is only taken when the name is unambiguous.
	if decls := ix.byName[lastConstComponent(ref)]; len(decls) == 1 {
		return decls
	}
	return nil
}

// ---------------------------------------------------------------------------
// parse
// ---------------------------------------------------------------------------

// filterKinds are the registrations that add a filter.
var filterKinds = map[string]bool{
	"before_action":         true,
	"around_action":         true,
	"after_action":          true,
	"prepend_before_action": true,
	"prepend_around_action": true,
	"prepend_after_action":  true,
	"append_before_action":  true,
	"append_around_action":  true,
	"append_after_action":   true,
}

// skipKinds remove an inherited filter. They only mean anything now that
// filters are inherited: a skip is the *only* thing standing between
// `before_action :authenticate_user!` in ApplicationController and an edge onto
// every action in the app, including the login form that must stay reachable
// without a session. Reading the registration and ignoring the retraction would
// assert precisely the wrong thing about the endpoints that matter most.
var skipKinds = map[string]bool{
	"skip_before_action":   true,
	"skip_around_action":   true,
	"skip_after_action":    true,
	"skip_action_callback": true,
}

// filterFamily reduces a registration or retraction to the chain it acts on, so
// `skip_before_action :x` cancels `prepend_before_action :x`.
func filterFamily(kind string) string {
	for _, p := range []string{"skip_", "prepend_", "append_"} {
		kind = strings.TrimPrefix(kind, p)
	}
	if kind == "action_callback" {
		return "before_action" // Rails 4 spelling, before/after undifferentiated
	}
	return kind
}

func (ix *filterIndex) scanFile(file string) {
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return
	}
	defer tree.Close()

	// Every filter call in the file, so the ones no class body claims can be
	// ledgered (bug-class #12) rather than silently lost. The shape that reaches
	// here is `included do before_action :x end` inside a concern, where the
	// registration belongs to whatever later includes the module.
	var markStray func(n *sitter.Node)
	markStray = func(n *sitter.Node) {
		if n.Type() == "call" {
			if mn := n.ChildByFieldName("method"); mn != nil && filterKinds[mn.Content(src)] {
				ix.strayFilters[fmt.Sprintf("%s\x00%d", file, int(n.StartPoint().Row)+1)] = int(n.StartPoint().Row) + 1
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			markStray(n.NamedChild(i))
		}
	}
	markStray(tree.RootNode())

	var walk func(n *sitter.Node, ns []string)
	walk = func(n *sitter.Node, ns []string) {
		inner := ns
		// Modules matter as much as classes here. orion's chain to
		// `can_manage_task_for_study?` runs CategoriesController → SecuredController
		// → `include SecurityChecks` → `include TaskSecurityChecks`, and the middle
		// link is a module: without it the walk stops one concern short and six
		// controllers report a callback that is plainly defined in the repo.
		if t := n.Type(); t == "class" || t == "module" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				// `class ClientApi::V1::Base` declares one class inside a
				// namespace it names inline, so the prefix joins the nesting.
				parts := strings.Split(nameNode.Content(src), "::")
				outer := append(append([]string{}, ns...), parts[:len(parts)-1]...)
				name := parts[len(parts)-1]
				ix.collectClass(n, name, outer, file, src, t == "module")
				inner = append(outer, name)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), inner)
		}
	}
	walk(tree.RootNode(), nil)
}

func (ix *filterIndex) collectClass(node *sitter.Node, name string, ns []string, file string, src []byte, isModule bool) {
	c := &ctrlClass{
		name: name,
		ns:   ns,
		file: file,
		line: int(node.StartPoint().Row) + 1,
	}
	if sup := node.ChildByFieldName("superclass"); sup != nil {
		for i := 0; i < int(sup.NamedChildCount()); i++ {
			ch := sup.NamedChild(i)
			if ch.Type() == "constant" || ch.Type() == "scope_resolution" {
				c.superRef = ch.Content(src)
				c.super = lastConstComponent(c.superRef)
				break
			}
		}
	}

	body := node.ChildByFieldName("body")
	if body == nil {
		ix.classes = append(ix.classes, c)
		return
	}

	// Visibility is positional: a bare `private` switches every following `def`
	// in the body. Only public methods are actions — `def set_user` under
	// `private` is a helper the router will never dispatch to.
	public := true
	for i := 0; i < int(body.NamedChildCount()); i++ {
		stmt := body.NamedChild(i)
		switch stmt.Type() {
		case "method":
			// A module has no actions: nothing routes to it directly, and its
			// methods become actions only of whatever includes it — which that
			// class's own body already reports.
			if public && !isModule {
				if nn := stmt.ChildByFieldName("name"); nn != nil {
					c.actions = append(c.actions, ctrlAction{nn.Content(src), int(stmt.StartPoint().Row) + 1})
				}
			}
		case "identifier":
			// Bare `private` / `protected` on its own line.
			switch stmt.Content(src) {
			case "private", "protected":
				public = false
			}
		case "call":
			mn := stmt.ChildByFieldName("method")
			if mn == nil {
				continue
			}
			switch name := mn.Content(src); {
			case name == "private" || name == "protected":
				// `private def foo` / `private :foo` mark one method without
				// switching the section, so the methods after them stay public.
				continue
			case name == "include" || name == "extend":
				c.includes = append(c.includes, constArgs(stmt, src)...)
			case name == "prepend":
				c.prepends = append(c.prepends, constArgs(stmt, src)...)
			case filterKinds[name]:
				// A bare `before_action` in a module body would raise at load
				// time; the legal spelling is inside `included do`, which belongs
				// to the includer and is ledgered instead.
				if isModule {
					continue
				}
				if reg, ok := parseFilterCall(stmt, name, src); ok {
					c.filters = append(c.filters, reg)
					delete(ix.strayFilters, fmt.Sprintf("%s\x00%d", file, reg.line))
				}
			case skipKinds[name]:
				if isModule {
					continue
				}
				// A skip names a symbol or it names nothing this pass can act
				// on; `skip_before_action :x, raise: false` is the common form.
				if reg, ok := parseFilterCall(stmt, name, src); ok && !reg.inline {
					c.skips = append(c.skips, reg)
				}
			}
		}
	}
	ix.classes = append(ix.classes, c)
}

// parseFilterCall reads the callback symbols and the only:/except: restriction
// off one registration.
//
// The inline form takes no symbol at all:
//
//	before_action -> { require_study_access(@study) }
//	before_action(only: %i[index edit]) { agent }
//
// and it is not a rarity — it is 123 of orion's registrations, more than a
// third of them. The lambda body still names a method of the controller, so the
// callbacks are read out of it; what is lost is only that the filter is one
// level of indirection away, which the `form` meta records.
func parseFilterCall(call *sitter.Node, kind string, src []byte) (filterReg, bool) {
	reg := filterReg{kind: kind, line: int(call.StartPoint().Row) + 1}

	args := call.ChildByFieldName("arguments")
	if args == nil {
		// `before_action { agent }` — no argument list at all, only a block.
		reg.callbacks = blockCallbacks(call, src)
		reg.inline = len(reg.callbacks) > 0
		return reg, reg.inline
	}
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			a := n.NamedChild(i)
			switch a.Type() {
			case "simple_symbol":
				reg.callbacks = append(reg.callbacks, symbolName(a.Content(src)))
			case "hash":
				visit(a) // trailing options sometimes arrive wrapped
			case "pair":
				key := ""
				if k := a.ChildByFieldName("key"); k != nil {
					key = symbolName(strings.TrimSuffix(k.Content(src), ":"))
				}
				val := a.ChildByFieldName("value")
				switch key {
				case "only":
					reg.only = append(reg.only, symbolList(val, src)...)
				case "except":
					reg.except = append(reg.except, symbolList(val, src)...)
				case "if", "unless":
					reg.conditional = true
				}
			}
		}
	}
	visit(args)

	if len(reg.callbacks) == 0 {
		// Options but no symbol: `before_action(only: %i[index edit]) { agent }`,
		// or a lambda sitting in the argument list.
		reg.callbacks = blockCallbacks(call, src)
		for i := 0; i < int(args.NamedChildCount()); i++ {
			if a := args.NamedChild(i); a.Type() == "lambda" {
				reg.callbacks = append(reg.callbacks, blockCallbacks(a, src)...)
			}
		}
		reg.inline = len(reg.callbacks) > 0
		return reg, reg.inline
	}
	return reg, true
}

// blockCallbacks reads the method names a filter's block or lambda body calls.
//
// Only receiverless calls count. `-> { require_study_access(@study) }` invokes a
// method on the controller and is the callback in everything but spelling;
// `-> { Rails.logger.info(x) }` invokes something on a constant and says nothing
// about the controller, so it contributes nothing rather than a wrong name.
func blockCallbacks(n *sitter.Node, src []byte) []string {
	body := n.ChildByFieldName("block")
	if body == nil {
		body = n.ChildByFieldName("body")
	}
	if body == nil {
		// A `do … end` / `{ … }` block is a plain child on some call shapes.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if t := n.NamedChild(i).Type(); t == "block" || t == "do_block" {
				body = n.NamedChild(i)
				break
			}
		}
	}
	if body == nil {
		return nil
	}

	var out []string
	var walk func(x *sitter.Node)
	walk = func(x *sitter.Node) {
		switch x.Type() {
		case "call":
			if x.ChildByFieldName("receiver") == nil {
				if mn := x.ChildByFieldName("method"); mn != nil {
					out = append(out, mn.Content(src))
				}
			}
		case "identifier":
			// A bare `agent` in a filter block is a method call on self; a local
			// variable would have had to be assigned somewhere in the same block,
			// and these bodies are one expression long.
			if p := x.Parent(); p != nil {
				switch p.Type() {
				case "block_body", "body_statement", "block", "do_block", "argument_list":
					out = append(out, x.Content(src))
				}
			}
		}
		for i := 0; i < int(x.NamedChildCount()); i++ {
			walk(x.NamedChild(i))
		}
	}
	walk(body)
	return out
}

// symbolList reads `:create`, `[:create, :update]` and `%i[create update]`.
func symbolList(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	switch n.Type() {
	case "simple_symbol":
		return []string{symbolName(n.Content(src))}
	case "array", "symbol_array":
		var out []string
		for i := 0; i < int(n.NamedChildCount()); i++ {
			ch := n.NamedChild(i)
			switch ch.Type() {
			case "simple_symbol":
				out = append(out, symbolName(ch.Content(src)))
			case "bare_symbol", "string": // %i[…] elements
				out = append(out, symbolName(strings.Trim(ch.Content(src), `"'`)))
			}
		}
		return out
	}
	return nil
}

func symbolName(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), ":") }

// constArgs returns the constant arguments of `include Foo, Bar`.
func constArgs(call *sitter.Node, src []byte) []string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		if a.Type() == "constant" || a.Type() == "scope_resolution" {
			out = append(out, lastConstComponent(a.Content(src)))
		}
	}
	return out
}

// lastConstComponent reduces `ClientApi::V1::Base` to `Base`, matching how the
// rest of the Ruby linker keys its class tables.
func lastConstComponent(s string) string {
	if i := strings.LastIndex(s, "::"); i >= 0 {
		return s[i+2:]
	}
	return s
}

// ---------------------------------------------------------------------------
// inheritance
// ---------------------------------------------------------------------------

// effFilter is a registration as it applies to one class: the class's own, or
// one inherited from a superclass, which is where most of them come from.
// ApplicationController and SecuredController between them declare the filters
// that guard almost every action orion serves, and none of those actions are
// in either file.
type effFilter struct {
	reg   filterReg
	owner *ctrlClass // the class whose body declares it
}

// effectiveFilters is the filter chain a request to this class actually runs:
// the class's own registrations, then each superclass's, nearest first, minus
// anything a skip along the chain retracts.
//
// Superclasses only. A module cannot legally declare a filter in its body — the
// spelling is `included do`, which is ledgered — so `include` contributes
// methods to look up, never registrations to inherit.
func (ix *filterIndex) effectiveFilters(c *ctrlClass) []effFilter {
	skips := ix.chainSkips(c)

	var out []effFilter
	for _, reg := range c.filters {
		out = append(out, effFilter{reg, c})
	}
	ix.eachSuperclass(c, func(decl *ctrlClass) {
		for _, reg := range decl.filters {
			if !retracted(skips, reg) {
				out = append(out, effFilter{reg, decl})
			}
		}
	})
	return out
}

// eachSuperclass walks the superclass chain, nearest first, calling fn once per
// declaration. Cycle-safe: a constant that resolves back to a class already
// seen ends the walk.
func (ix *filterIndex) eachSuperclass(c *ctrlClass, fn func(*ctrlClass)) {
	visited := map[string]bool{c.qualified(): true}
	cur := c
	for depth := 0; depth < 32; depth++ {
		decls := ix.resolveSuper(cur)
		if len(decls) == 0 {
			return
		}
		var next *ctrlClass
		for _, decl := range decls {
			if visited[decl.qualified()] {
				continue
			}
			visited[decl.qualified()] = true
			fn(decl)
			if next == nil {
				next = decl
			}
		}
		if next == nil {
			return
		}
		cur = next
	}
}

// chainSkips collects the retractions in force for a class: its own, and every
// superclass's, since a skip applies to the class that writes it and everything
// below.
func (ix *filterIndex) chainSkips(c *ctrlClass) []filterReg {
	out := append([]filterReg{}, c.skips...)
	ix.eachSuperclass(c, func(decl *ctrlClass) {
		out = append(out, decl.skips...)
	})
	return out
}

// retracted reports whether a skip cancels a registration outright. A skip
// carrying only:/except: cancels it for some actions only, which is decided per
// action in skippedFor — the filter itself survives.
func retracted(skips []filterReg, reg filterReg) bool {
	for _, s := range skips {
		if len(s.only) > 0 || len(s.except) > 0 {
			continue
		}
		if filterFamily(s.kind) != filterFamily(reg.kind) {
			continue
		}
		for _, sc := range s.callbacks {
			for _, rc := range reg.callbacks {
				if sc == rc {
					return true
				}
			}
		}
	}
	return false
}

// skippedFor reports whether a partial skip removes one callback for one action.
func skippedFor(skips []filterReg, reg filterReg, cb, action string) bool {
	for _, s := range skips {
		if filterFamily(s.kind) != filterFamily(reg.kind) {
			continue
		}
		if !s.appliesTo(action) {
			continue
		}
		for _, sc := range s.callbacks {
			if sc == cb {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// resolve + emit
// ---------------------------------------------------------------------------

func (ix *filterIndex) link() ([]graph.Edge, []graph.UnresolvedRef) {
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	seenEdge := map[string]bool{}
	seenMiss := map[string]bool{}

	add := func(from, to, label, conf string, meta map[string]string) {
		if from == "" || to == "" || from == to {
			return
		}
		id := fmt.Sprintf("calls:%s->%s:%s", from, to, meta["filter"])
		if seenEdge[id] {
			return
		}
		seenEdge[id] = true
		edges = append(edges, graph.Edge{
			ID: id, From: from, To: to,
			Type: graph.EdgeTypeCalls, Label: label,
			Meta: meta, Confidence: conf,
		})
	}

	for _, c := range ix.classes {
		classNodeID := ix.classID[fmt.Sprintf("%s\x00%s\x00%d", c.file, c.name, c.line)]
		skips := ix.chainSkips(c)

		for _, ef := range ix.effectiveFilters(c) {
			reg, owner := ef.reg, ef.owner
			inherited := owner != c
			for _, cb := range reg.callbacks {
				// Resolution runs from the declaring class: `before_action :x` in
				// ApplicationController names ApplicationController's `x`, even
				// when the subclass this edge hangs off defines an `x` of its own.
				targets, depth := ix.resolveCallback(owner, cb)
				if len(targets) == 0 {
					if inherited {
						continue // the owner already ledgered this miss
					}
					key := fmt.Sprintf("%s\x00%s\x00%s", c.file, c.name, cb)
					if !seenMiss[key] {
						seenMiss[key] = true
						unresolved = append(unresolved, graph.UnresolvedRef{
							Service: ix.svc, File: c.file, Line: reg.line,
							Name: cb, Kind: "rails_filter_unresolved",
						})
					}
					continue
				}
				// More than one class in the service defines the callback at the
				// same point in the ancestor chain: every one is a candidate,
				// naming one would be the fan-out bug (phases.md #1).
				conf := graph.ConfidenceStatic
				if depth > 0 {
					conf = graph.ConfidenceInferred
				}
				if len(targets) > 1 {
					conf = graph.ConfidencePartial
					if !inherited {
						key := fmt.Sprintf("ambiguous\x00%s\x00%s\x00%s", c.file, c.name, cb)
						if !seenMiss[key] {
							seenMiss[key] = true
							unresolved = append(unresolved, graph.UnresolvedRef{
								Service: ix.svc, File: c.file, Line: reg.line,
								Name: cb, Kind: "rails_filter_ambiguous",
							})
						}
					}
				}

				base := map[string]string{
					"via":    "rails_filter",
					"filter": reg.kind,
				}
				if inherited {
					// The subclass's file contains no trace of this filter, so the
					// edge has to say where to go read it. The superclass chain is
					// reconstructed from constant names, so an inherited edge is
					// never better than inferred.
					base["inherited_from"] = owner.name
					if conf == graph.ConfidenceStatic {
						conf = graph.ConfidenceInferred
					}
				}
				if reg.conditional {
					base["conditional"] = "true"
				}
				if reg.inline {
					// The registration named a block, not this method — the call
					// is one level of indirection away from the line.
					base["form"] = "block"
				}
				if len(reg.only) > 0 {
					base["only"] = strings.Join(reg.only, ",")
				}
				if len(reg.except) > 0 {
					base["except"] = strings.Join(reg.except, ",")
				}

				for _, t := range targets {
					m := copyMeta(base)
					m["scope"] = "class"
					add(classNodeID, t, reg.kind+" :"+cb, conf, m)
				}
				for _, a := range c.actions {
					if !reg.appliesTo(a.name) {
						continue
					}
					if inherited && skippedFor(skips, reg, cb, a.name) {
						continue
					}
					for _, aid := range ix.methodQN[c.name+"#"+a.name] {
						for _, t := range targets {
							m := copyMeta(base)
							m["scope"] = "action"
							m["action"] = a.name
							add(aid, t, reg.kind+" :"+cb, conf, m)
						}
					}
				}
			}
		}
	}

	// Registrations no class body claimed: a concern's `included do` block, or a
	// block whose only call has a receiver and so names nothing on the class.
	strayKeys := make([]string, 0, len(ix.strayFilters))
	for k := range ix.strayFilters {
		strayKeys = append(strayKeys, k)
	}
	sort.Strings(strayKeys)
	for _, k := range strayKeys {
		file, _, _ := strings.Cut(k, "\x00")
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: ix.svc, File: file, Line: ix.strayFilters[k],
			Name: "before_action", Kind: "rails_filter_unattributed",
		})
	}

	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	sort.SliceStable(unresolved, func(i, j int) bool {
		a, b := unresolved[i], unresolved[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
	return edges, unresolved
}

// appliesTo applies only:/except:. A filter with neither runs for every action.
func (r *filterReg) appliesTo(action string) bool {
	if len(r.only) > 0 {
		for _, a := range r.only {
			if a == action {
				return true
			}
		}
		return false
	}
	for _, a := range r.except {
		if a == action {
			return false
		}
	}
	return true
}

// resolveCallback finds the method a callback symbol names, walking the class's
// own body first and then its ancestors. depth is how far up the chain the hit
// was: 0 means the class defines it outright, which is the only case that is
// certain — anything further depends on an ancestor chain reconstructed from
// constant names.
func (ix *filterIndex) resolveCallback(c *ctrlClass, cb string) (ids []string, depth int) {
	visited := map[string]bool{}
	queue := []string{c.name}
	for depth = 0; len(queue) > 0 && depth < 16; depth++ {
		var next []string
		var hits []string
		for _, name := range queue {
			if visited[name] {
				continue
			}
			visited[name] = true
			hits = append(hits, ix.methodQN[name+"#"+cb]...)
			for _, decl := range ix.byName[name] {
				next = append(next, decl.ancestorNames()...)
			}
		}
		if len(hits) > 0 {
			sort.Strings(hits)
			return dedupeStrings(hits), depth
		}
		queue = next
	}
	return nil, 0
}

func dedupeStrings(in []string) []string {
	out := in[:0]
	var last string
	for i, s := range in {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}

func copyMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
