package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// extractRubyVariables is the structural variable-tracking pass for Ruby:
// constants, classes (with methods and attr_* accessors), and instance /
// class variables with reads/writes edges from the enclosing method. All
// edges are inferred confidence — Ruby's dynamism rules out certainty.
// Block-capture tracking is deliberately skipped in v1: blocks are so
// pervasive in Ruby that lexical capture edges would be mostly noise.
func extractRubyVariables(file, service string, src []byte) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil, nil
	}
	defer tree.Close()

	ex := &rubyExtractor{
		file: file, service: service, src: src,
		ivarDecl:           map[string]int{},
		classTable:         map[string]string{},
		nodeSeen:           map[string]bool{},
		edgeSeen:           map[string]bool{},
		methodsByClassName: map[string]string{},
		methodsByName:      map[string][]string{},
		locals:             map[string]map[string]bool{},
	}
	// Pre-collect class names for same-file constant resolution, method
	// definitions (class-scoped and flat) so calls to a method defined later
	// in the file still resolve (forward references), and local-variable
	// names per method so a bare identifier read of a local isn't
	// misattributed as a call to a same-named method (Tier BC).
	ex.preCollectRubyClasses(tree.RootNode())
	ex.preCollectRubyMethods(tree.RootNode(), "")
	ex.preCollectRubyLocals(tree.RootNode(), "")
	ex.walk(tree.RootNode(), "", "", "")

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges, ex.unresolved
}

type rubyExtractor struct {
	file, service string
	src           []byte

	ivarDecl   map[string]int    // "@name" (class-qualified) → first-seen line
	classTable map[string]string // class/module name → nodeID (same-file)

	// methodsByClassName/methodsByName index same-file method definitions for
	// bare-call resolution (implicit-self calls): "class\x00name" → nodeID,
	// and name → every nodeID sharing that name (used only when the
	// class-scoped lookup misses and there is exactly one file-wide
	// candidate, so an ambiguous name never misattributes a call).
	methodsByClassName map[string]string
	methodsByName      map[string][]string

	// locals maps methodID → set of names that are local variables (assigned,
	// a parameter, or bound by a for/pattern-match/rescue) anywhere in that
	// method. A bare identifier read whose name is in this set is a variable
	// read, never a call — see resolveBareCall / preCollectRubyLocals.
	locals map[string]map[string]bool

	nodes      []graph.Node
	edges      []graph.Edge
	unresolved []graph.UnresolvedRef
	nodeSeen   map[string]bool
	edgeSeen   map[string]bool
}

func (ex *rubyExtractor) addNode(n graph.Node) {
	if !ex.nodeSeen[n.ID] {
		ex.nodeSeen[n.ID] = true
		ex.nodes = append(ex.nodes, n)
	}
}

func (ex *rubyExtractor) addEdge(typ graph.EdgeType, from, to string, meta map[string]string) {
	id := fmt.Sprintf("rbvar:%s:%s->%s", typ, from, to)
	if ex.edgeSeen[id] {
		return
	}
	ex.edgeSeen[id] = true
	ex.edges = append(ex.edges, graph.Edge{
		ID: id, From: from, To: to, Type: typ,
		Confidence: graph.ConfidenceInferred, Meta: meta,
	})
}

func rbLine(n *sitter.Node) int { return int(n.StartPoint().Row) + 1 }

func rbEndLine(n *sitter.Node) int { return int(n.EndPoint().Row) + 1 }

// ivarNode materialises the variable node for an instance/class variable the
// first time it is seen and returns its ID.
func (ex *rubyExtractor) ivarNode(name, class string, ln int) string {
	key := class + "\x00" + name
	declLine, seen := ex.ivarDecl[key]
	if !seen {
		declLine = ln
		ex.ivarDecl[key] = ln
	}
	scope := "instance"
	if strings.HasPrefix(name, "@@") {
		scope = "class"
	}
	id := fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, declLine)
	ex.addNode(graph.Node{
		ID: id, Type: graph.NodeTypeVariable, Label: name,
		Service: ex.service, File: ex.file, Line: declLine, EndLine: declLine, Language: "ruby",
		Meta: map[string]string{
			"kind": "var", "scope": scope, "mutable": "true",
			"class": class,
		},
	})
	return id
}

func (ex *rubyExtractor) methodNodeID(method string, ln int) string {
	return fmt.Sprintf("%s:%s:function:%s:%d", ex.service, ex.file, method, ln)
}

// preCollectRubyClasses scans the AST recursively to build classTable:
// className → nodeID for all class/module declarations in the file.
func (ex *rubyExtractor) preCollectRubyClasses(node *sitter.Node) {
	t := node.Type()
	if t == "class" || t == "module" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			id := fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node))
			ex.classTable[name] = id
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyClasses(node.NamedChild(i))
	}
}

// preCollectRubyMethods scans the AST recursively to build methodsByClassName
// and methodsByName: every method/singleton_method definition, keyed by its
// enclosing class (bare name, matching classTable's simplification) and by
// its bare name alone. Runs before walk so a call to a method defined later
// in the same file still resolves.
func (ex *rubyExtractor) preCollectRubyMethods(node *sitter.Node, class string) {
	switch node.Type() {
	case "class", "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			class = nameNode.Content(ex.src)
		}
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			id := ex.methodNodeID(name, rbLine(node))
			if class != "" {
				ex.methodsByClassName[class+"\x00"+name] = id
			}
			ex.methodsByName[name] = append(ex.methodsByName[name], id)
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyMethods(node.NamedChild(i), class)
	}
}

// addLocal records name as a local variable of methodID. A no-op for
// methodID == "" (top-level/class-body code, where bare identifiers are
// never attributed as calls either — see walk's case "identifier").
func (ex *rubyExtractor) addLocal(methodID, name string) {
	if methodID == "" {
		return
	}
	if ex.locals[methodID] == nil {
		ex.locals[methodID] = map[string]bool{}
	}
	ex.locals[methodID][name] = true
}

// collectParamNames records every parameter name in a method_parameters or
// block_parameters list. A plain positional param is a bare `identifier`
// child; every other shape (optional/splat/keyword/hash-splat/block param)
// wraps its name in a `name`-field child — confirmed against the grammar,
// see docs/ruby-bare-identifier-call-plan.md. A default-value expression on
// an optional/keyword parameter is deliberately NOT excluded here (only the
// `name` field is), so `def foo(x = default_val)` still lets `default_val`
// resolve as a bare call.
func (ex *rubyExtractor) collectParamNames(params *sitter.Node, methodID string) {
	for i := 0; i < int(params.NamedChildCount()); i++ {
		c := params.NamedChild(i)
		if c.Type() == "identifier" {
			ex.addLocal(methodID, c.Content(ex.src))
			continue
		}
		if nameNode := c.ChildByFieldName("name"); nameNode != nil && nameNode.Type() == "identifier" {
			ex.addLocal(methodID, nameNode.Content(ex.src))
		}
	}
}

// collectAssignTargets records the plain-identifier target(s) of an
// assignment's left-hand side: a single `identifier`, or a multi-assign
// `left_assignment_list` (possibly containing a `rest_assignment` for a
// splat target, e.g. `a, *b = arr`) — recursed into. Non-identifier targets
// (instance/class variable, constant) aren't ambiguous with a call and are
// left alone.
func (ex *rubyExtractor) collectAssignTargets(node *sitter.Node, methodID string) {
	switch node.Type() {
	case "identifier":
		ex.addLocal(methodID, node.Content(ex.src))
	case "left_assignment_list", "rest_assignment":
		for i := 0; i < int(node.NamedChildCount()); i++ {
			ex.collectAssignTargets(node.NamedChild(i), methodID)
		}
	}
}

// collectPatternIdentifiers records every identifier found anywhere inside a
// pattern-match (`case/in`) pattern or a `rescue => e` exception variable.
// Both bind local names but nest arbitrarily (array/hash/find patterns, an
// `as_pattern`'s `=> name`), so this walks the whole subtree rather than
// enumerating shapes — any identifier reachable from a pattern node is a
// binding, never a call.
func (ex *rubyExtractor) collectPatternIdentifiers(node *sitter.Node, methodID string) {
	if node.Type() == "identifier" {
		ex.addLocal(methodID, node.Content(ex.src))
		return
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.collectPatternIdentifiers(node.NamedChild(i), methodID)
	}
}

// preCollectRubyLocals walks the whole tree recording, per method, every
// name that is a local variable somewhere in that method — conservative in
// the false-negative direction: a name assigned/bound anywhere in the method
// (even after a later bare-identifier use) shadows a same-named method for
// the entire method body, matching preCollectRubyMethods's forward-reference
// pre-pass shape. Blocks (do...end, {}) are NOT scope boundaries: Ruby
// blocks read/write the enclosing method's locals, so methodID is carried
// through unchanged into block_parameters. `for`, `case/in`, and
// `rescue => e` bindings are covered explicitly (see Risks in the plan doc)
// since they are also local-variable sites a bare later use must not
// misattribute as a call.
func (ex *rubyExtractor) preCollectRubyLocals(node *sitter.Node, methodID string) {
	switch node.Type() {
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			methodID = ex.methodNodeID(nameNode.Content(ex.src), rbLine(node))
		}
		if params := node.ChildByFieldName("parameters"); params != nil {
			ex.collectParamNames(params, methodID)
		}
	case "assignment", "operator_assignment":
		if methodID != "" {
			if left := node.ChildByFieldName("left"); left != nil {
				ex.collectAssignTargets(left, methodID)
			}
		}
	case "block_parameters":
		ex.collectParamNames(node, methodID)
	case "for":
		if pattern := node.ChildByFieldName("pattern"); pattern != nil {
			ex.collectAssignTargets(pattern, methodID)
		}
	case "in_clause":
		if pattern := node.ChildByFieldName("pattern"); pattern != nil {
			ex.collectPatternIdentifiers(pattern, methodID)
		}
	case "rescue":
		if v := node.ChildByFieldName("variable"); v != nil {
			ex.collectPatternIdentifiers(v, methodID)
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyLocals(node.NamedChild(i), methodID)
	}
}

// isRubyBareCallExcluded reports whether an `identifier` node is structurally
// something other than a bare/implicit-self call read: a definition name, a
// parameter name, a `call` node's method/receiver field (already handled by
// case "call"), or an assignment target. See Recognition in
// docs/ruby-bare-identifier-call-plan.md for the grammar verification behind
// each branch.
func isRubyBareCallExcluded(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return true
	}
	switch parent.Type() {
	case "method", "singleton_method":
		return parent.ChildByFieldName("name") == node
	case "method_parameters", "block_parameters":
		return true
	case "optional_parameter", "splat_parameter", "keyword_parameter", "hash_splat_parameter", "block_parameter":
		return parent.ChildByFieldName("name") == node
	case "call":
		return parent.ChildByFieldName("method") == node || parent.ChildByFieldName("receiver") == node
	case "assignment", "operator_assignment":
		return parent.ChildByFieldName("left") == node
	case "left_assignment_list":
		return true
	}
	return false
}

// resolveBareCall is the shared bare/implicit-self call resolution logic
// used by both case "call" (a `helper(x)`/`self.foo` shape, which already
// has a receiver-derived lookupClass) and case "identifier" (a fully bare
// `category` shape, which always looks up against the enclosing class since
// there is no receiver by construction). ledgerUnresolved is false for the
// identifier path: see Ledger policy in the plan doc — an unresolved bare
// identifier has no guarantee it was ever a call at all (it may be a local
// this pass's conservative scope tracking missed), unlike an unresolved
// case "call", which the parser structurally knows is a real call.
func (ex *rubyExtractor) resolveBareCall(mname, lookupClass, class, methodID string, srcLine int, ledgerUnresolved bool) {
	targetID := ""
	if lookupClass != "" {
		targetID = ex.methodsByClassName[lookupClass+"\x00"+mname]
	}
	selfScoped := lookupClass == class
	if targetID == "" && selfScoped {
		if ids := ex.methodsByName[mname]; len(ids) == 1 {
			targetID = ids[0]
		}
	}
	if targetID != "" {
		ex.addEdge(graph.EdgeTypeCalls, methodID, targetID, nil)
	} else if ledgerUnresolved && !isRubyBuiltinCall(mname, ex.file) && selfScoped {
		ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
			Service: ex.service, File: ex.file,
			Line: srcLine, Name: mname, Kind: "call_ref",
		})
	}
}

// walk descends the AST carrying the enclosing class name, class nodeID, and method nodeID.
func (ex *rubyExtractor) walk(node *sitter.Node, class, classID, methodID string) {
	switch node.Type() {
	case "class", "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			ex.collectClass(node, name)
			class = name
			classID = fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node))

			// Superclass → inherits edge.
			if superNode := node.ChildByFieldName("superclass"); superNode != nil {
				// superNode is a `superclass` AST node; its first named child is the constant.
				var superConst *sitter.Node
				for i := 0; i < int(superNode.NamedChildCount()); i++ {
					c := superNode.NamedChild(i)
					if c.Type() == "constant" || c.Type() == "scope_resolution" {
						superConst = c
						break
					}
				}
				if superConst != nil {
					superName := ""
					if superConst.Type() == "constant" {
						superName = superConst.Content(ex.src)
					} else if superConst.Type() == "scope_resolution" {
						// Foo::Bar — use last component only for table lookup.
						if last := superConst.ChildByFieldName("name"); last != nil {
							superName = last.Content(ex.src)
						}
					}
					if superName != "" {
						if parentID, ok := ex.classTable[superName]; ok {
							ex.addEdge(graph.EdgeTypeInherits, classID, parentID,
								map[string]string{"via": "superclass"})
						} else {
							ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
								Service: ex.service, File: ex.file,
								Line: rbLine(superNode), Name: superName, Kind: "inherits_unresolved",
							})
						}
					}
				}
			}
		}
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			methodID = ex.methodNodeID(name, rbLine(node))
			meta := map[string]string{"class": class}
			// end_line lets comm-node enclosing attribution (linkRubyEnclosingCalls)
			// bound this method's body by line range rather than nearest-preceding.
			meta["end_line"] = fmt.Sprintf("%d", int(node.EndPoint().Row)+1)
			// X.2: qualified_name is the <Type>#<method> join key delayed_job's
			// dj_target (matcher.go) and jobs.yaml's contract rules match against.
			if class != "" {
				meta["qualified_name"] = class + "#" + name
			} else {
				meta["qualified_name"] = name
			}
			ex.addNode(graph.Node{
				ID: methodID, Type: graph.NodeTypeFunction, Label: name,
				Service: ex.service, File: ex.file, Line: rbLine(node), EndLine: rbEndLine(node), Language: "ruby",
				Meta: meta,
			})
		}
	case "assignment", "operator_assignment":
		left := node.ChildByFieldName("left")
		if left != nil {
			switch left.Type() {
			case "constant":
				// Top-level or class-level constant definition.
				if methodID == "" {
					name := left.Content(ex.src)
					ex.addNode(graph.Node{
						ID:   fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, rbLine(node)),
						Type: graph.NodeTypeVariable, Label: name,
						Service: ex.service, File: ex.file, Line: rbLine(node), EndLine: rbEndLine(node), Language: "ruby",
						Meta: map[string]string{
							"kind": "const", "scope": "module", "mutable": "false",
							"class": class,
						},
					})
				}
			case "instance_variable", "class_variable":
				id := ex.ivarNode(left.Content(ex.src), class, rbLine(node))
				if methodID != "" {
					ex.addEdge(graph.EdgeTypeWrites, methodID, id, map[string]string{"op": "assign"})
				}
			}
		}
	case "instance_variable", "class_variable":
		// A read unless it is the left side of an assignment (handled above).
		if parent := node.Parent(); parent != nil {
			pt := parent.Type()
			if (pt == "assignment" || pt == "operator_assignment") && parent.ChildByFieldName("left") == node {
				break
			}
			if methodID != "" {
				id := ex.ivarNode(node.Content(ex.src), class, rbLine(node))
				ex.addEdge(graph.EdgeTypeReads, methodID, id, nil)
			}
		}
	case "call":
		mn := node.ChildByFieldName("method")
		if mn == nil {
			break
		}
		mname := mn.Content(ex.src)
		switch mname {
		case "include", "extend", "prepend":
			// Mixin calls inside a class body (no receiver or self-implicit).
			receiver := node.ChildByFieldName("receiver")
			if classID != "" && methodID == "" && (receiver == nil || receiver.Content(ex.src) == "self") {
				if args := node.ChildByFieldName("arguments"); args != nil {
					for i := 0; i < int(args.NamedChildCount()); i++ {
						a := args.NamedChild(i)
						if a.Type() != "constant" {
							continue
						}
						modName := a.Content(ex.src)
						if modID, ok := ex.classTable[modName]; ok {
							ex.addEdge(graph.EdgeTypeInherits, classID, modID,
								map[string]string{"via": "mixin", "mixin": mname})
						} else {
							ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
								Service: ex.service, File: ex.file,
								Line: rbLine(a), Name: modName, Kind: "inherits_unresolved",
							})
						}
					}
				}
			}
		case "new":
			// Foo.new → instantiates from enclosing method.
			if methodID != "" {
				receiver := node.ChildByFieldName("receiver")
				if receiver != nil && receiver.Type() == "constant" {
					clsName := receiver.Content(ex.src)
					if clsID, ok := ex.classTable[clsName]; ok {
						edgeKey := fmt.Sprintf("instantiates:%s->%s", methodID, clsID)
						if !ex.edgeSeen[edgeKey] {
							ex.edgeSeen[edgeKey] = true
							ex.edges = append(ex.edges, graph.Edge{
								ID:         edgeKey,
								From:       methodID,
								To:         clsID,
								Type:       graph.EdgeTypeInstantiates,
								Confidence: graph.ConfidenceInferred,
								Meta:       map[string]string{"count": "1"},
							})
						}
					} // cross-file Foo.new resolved by LinkRubyTypeRelations
				}
			}
		default:
			// Bare/implicit-self method calls: helper(x), save, self.foo.
			// A `ClassName.method` receiver is also resolvable when the class
			// is declared in this file — unambiguous the same way `Foo.new`
			// is above, just against methodsByClassName instead of
			// classTable. Any other receiver-typed call (article.save) needs
			// static type inference Ruby's dynamism rules out, so it is left
			// alone (rule 9: only attribute a call when the target is
			// unambiguous).
			if methodID == "" {
				break
			}
			lookupClass := class
			if receiver := node.ChildByFieldName("receiver"); receiver != nil {
				switch {
				case receiver.Content(ex.src) == "self":
					// implicit self; lookupClass stays the enclosing class
				case receiver.Type() == "constant":
					lookupClass = receiver.Content(ex.src)
				default:
					// Any other receiver (article.save) needs static type
					// inference Ruby's dynamism rules out; a plain `break`
					// here would only exit this inner switch and fall through
					// to a resolution attempt, so bail out of the call
					// handling entirely instead.
					goto next
				}
			}
			// A framework or language builtin is not a blind spot: no pass
			// can ever resolve it, so ledgering it only inflates the
			// "verify N manually" footer agents are told to act on. An
			// unresolved ClassName.method miss isn't ledgered here either:
			// the class is very often a cross-file model (ActiveRecord
			// finders, etc.) that this same-file pass has no way to see,
			// so it is left for a future cross-file linker pass rather
			// than reported as a same-file miss it never was.
			ex.resolveBareCall(mname, lookupClass, class, methodID, rbLine(node), true)
		}
	case "identifier":
		// Bare, zero-arg, receiver-less, paren-less call/local-read ambiguity
		// (Tier BC): tree-sitter-ruby emits the same "identifier" node type
		// for `category` in both `@category = category` (a call to a private
		// helper) and `x` in `foo(x)` (a local-variable read). Resolve only
		// from within a method body, exactly like case "call" above; never
		// attribute a bare identifier as a call to a name preCollectRubyLocals
		// found assigned/bound anywhere in this method.
		if methodID == "" || isRubyBareCallExcluded(node) {
			break
		}
		mname := node.Content(ex.src)
		if ex.locals[methodID][mname] {
			break
		}
		ex.resolveBareCall(mname, class, class, methodID, rbLine(node), false)
	}

next:
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.walk(node.NamedChild(i), class, classID, methodID)
	}
}

// collectClass emits a class node with its method names and attr_* symbols.
func (ex *rubyExtractor) collectClass(node *sitter.Node, name string) {
	var methods, attrs []string
	if body := node.ChildByFieldName("body"); body != nil {
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch m.Type() {
			case "method":
				if mn := m.ChildByFieldName("name"); mn != nil {
					methods = append(methods, mn.Content(ex.src))
				}
			case "call":
				// attr_accessor :a, :b / attr_reader / attr_writer
				mn := m.ChildByFieldName("method")
				if mn == nil || !strings.HasPrefix(mn.Content(ex.src), "attr_") {
					continue
				}
				if args := m.ChildByFieldName("arguments"); args != nil {
					for j := 0; j < int(args.NamedChildCount()); j++ {
						a := args.NamedChild(j)
						if a.Type() == "simple_symbol" {
							attrs = append(attrs, strings.TrimPrefix(a.Content(ex.src), ":"))
						}
					}
				}
			}
		}
	}
	ex.addNode(graph.Node{
		ID:   fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node)),
		Type: graph.NodeTypeClass, Label: name,
		Service: ex.service, File: ex.file, Line: rbLine(node), EndLine: rbEndLine(node), Language: "ruby",
		Meta: map[string]string{
			"methods": strings.Join(methods, ","),
			"attrs":   strings.Join(attrs, ","),
			// end_line bounds the class body so linkRubyEnclosingCalls can attribute
			// a class-body DSL call (Sneakers `from_queue`) to the class that
			// declares it. Without a bound, nearest-preceding would be the only
			// option, and it is wrong: lib/tasks/vega_events.rake closes `module
			// Kicks` at line 20 and declares a queue at line 80, inside a rake task
			// block that the module does not contain.
			"end_line": fmt.Sprintf("%d", int(node.EndPoint().Row)+1),
		},
	})
}
