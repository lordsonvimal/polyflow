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
// # Scope
//
// Filters bind to the actions of the class that declares them. They are not
// propagated to subclasses: `before_action` in ApplicationController really does
// run before every action in every controller, but minting that would be one
// line producing thousands of edges, and the class-scope edge already records
// the fact. Callback *lookup* does walk ancestors, because that is where the
// method almost always lives.
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
	name     string
	file     string
	line     int
	prepends []string // prepend Foo, source order
	includes []string // include/extend Foo, source order
	super    string
	actions  []ctrlAction
	filters  []filterReg
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
	}
	return ix
}

// ---------------------------------------------------------------------------
// parse
// ---------------------------------------------------------------------------

// filterKinds are the registrations this pass understands. skip_* is absent on
// purpose: it removes an *inherited* filter, and since inherited filters are not
// propagated there is nothing for it to remove — treating it as a registration
// would invent a call that never happens.
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

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		// Modules matter as much as classes here. orion's chain to
		// `can_manage_task_for_study?` runs CategoriesController → SecuredController
		// → `include SecurityChecks` → `include TaskSecurityChecks`, and the middle
		// link is a module: without it the walk stops one concern short and six
		// controllers report a callback that is plainly defined in the repo.
		if t := n.Type(); t == "class" || t == "module" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				ix.collectClass(n, nameNode, file, src, t == "module")
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
}

func (ix *filterIndex) collectClass(node, nameNode *sitter.Node, file string, src []byte, isModule bool) {
	c := &ctrlClass{
		name: lastConstComponent(nameNode.Content(src)),
		file: file,
		line: int(node.StartPoint().Row) + 1,
	}
	if sup := node.ChildByFieldName("superclass"); sup != nil {
		for i := 0; i < int(sup.NamedChildCount()); i++ {
			ch := sup.NamedChild(i)
			if ch.Type() == "constant" || ch.Type() == "scope_resolution" {
				c.super = lastConstComponent(ch.Content(src))
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

		for _, reg := range c.filters {
			for _, cb := range reg.callbacks {
				targets, depth := ix.resolveCallback(c, cb)
				if len(targets) == 0 {
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
					key := fmt.Sprintf("ambiguous\x00%s\x00%s\x00%s", c.file, c.name, cb)
					if !seenMiss[key] {
						seenMiss[key] = true
						unresolved = append(unresolved, graph.UnresolvedRef{
							Service: ix.svc, File: c.file, Line: reg.line,
							Name: cb, Kind: "rails_filter_ambiguous",
						})
					}
				}

				base := map[string]string{
					"via":    "rails_filter",
					"filter": reg.kind,
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
