package parser

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
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
		isView:             strings.EqualFold(filepath.Ext(file), ".erb"),
		ivarDecl:           map[string]int{},
		classTable:         map[string]string{},
		constTable:         map[string]string{},
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
	ex.preCollectRubyConstants(tree.RootNode(), "")
	ex.preCollectRubyLocals(tree.RootNode(), "")
	ex.walk(tree.RootNode(), "", "", "")

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges, ex.unresolved
}

type rubyExtractor struct {
	file, service string
	src           []byte

	// isView marks a `.erb` file's virtualRuby content (see erb.go): a view
	// runs with `self` bound to the view instance, mixing in every helper
	// module exactly like a class body would, but tree-sitter never sees an
	// enclosing `class`/`def` for that scope. DC.12 relaxes the two
	// methodID=="" bare-call guards below specifically for this case so a
	// view's bare/self call sites get ledgered as call_ref (for
	// LinkRubyMixinMethods/LinkRubyOverrideDispatch to resolve cross-file
	// against the service's helper modules) instead of silently vanishing —
	// scoped to .erb only so plain top-level Ruby script/DSL calls (rake
	// tasks, migrations, initializers) keep their existing zero-noise
	// behavior.
	isView bool

	ivarDecl   map[string]int    // "@name" (class-qualified) → first-seen line
	classTable map[string]string // class/module name → nodeID (same-file)
	constTable map[string]string // CONST name → nodeID (same-file, class/module-body scope only)

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

// railsCallbackBlockMethods are Rails callback-registration DSL methods
// (controller filters + ActiveRecord model callbacks — the same convention
// `internal/linker/rails_filters.go`'s `filterKinds` resolves for the
// `:symbol` form) that are also commonly called with a `do...end`/`{}` block
// instead of a symbol, e.g. `before_action do; log_audit_activity; end`.
// DC.28: that block has no enclosing `def`, so bare calls made directly
// inside it need the same synthetic-scope treatment DC.18 gave `task`/
// `namespace` blocks — see dslBlockScopeID.
var railsCallbackBlockMethods = map[string]bool{
	"before_action": true, "after_action": true, "around_action": true,
	"prepend_before_action": true, "prepend_around_action": true, "prepend_after_action": true,
	"append_before_action": true, "append_around_action": true, "append_after_action": true,
	"validate": true, "before_validation": true, "after_validation": true,
	"before_save": true, "around_save": true, "after_save": true,
	"before_create": true, "around_create": true, "after_create": true,
	"before_update": true, "around_update": true, "after_update": true,
	"before_destroy": true, "around_destroy": true, "after_destroy": true,
	"after_commit": true, "after_rollback": true,
}

// rubyScreamingConstRE matches Ruby's true-constant naming convention
// (SCREAMING_SNAKE_CASE), as opposed to a class/module name (CamelCase) —
// both share the same "constant" tree-sitter node type, so this is the only
// way to tell them apart without full type resolution. See DC.31's use in
// walk's case "constant".
var rubyScreamingConstRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// rubyCommonMethodNames denylists Ruby/Rails/ActiveSupport/Enumerable
// vocabulary so common it is never safe to bind by service-wide name
// uniqueness alone (DC.32, walk's case "call" default-receiver branch): even
// when a name like `to_json` or `scope` happens to have exactly one
// app-defined method with that name in the whole service, a call site is far
// more likely reaching the framework/gem's own method of the same name on
// some other receiver than it is reaching ours. Confirmed against orion:
// of 55 sole-service-definer names with a typed-receiver call site, `scope`,
// `to_json`, `with`, and `await` were the only false-positive risks — this
// list is deliberately narrow (that measured set plus the obvious rest of
// the same vocabulary), not an attempt to enumerate every gem method, since
// anything missed here just falls back to "left unresolved", not a wrong
// bind.
var rubyCommonMethodNames = map[string]bool{
	"each": true, "each_with_index": true, "each_with_object": true, "each_pair": true,
	"each_slice": true, "each_key": true, "each_value": true, "with_index": true,
	"map": true, "flat_map": true, "select": true, "filter": true, "reject": true,
	"find": true, "find_by": true, "find_all": true, "detect": true,
	"count": true, "size": true, "length": true, "empty?": true,
	"any?": true, "all?": true, "none?": true, "one?": true,
	"first": true, "last": true, "sum": true, "min": true, "max": true,
	"min_by": true, "max_by": true, "sort": true, "sort_by": true,
	"uniq": true, "compact": true, "flatten": true, "reverse": true,
	"merge": true, "merge!": true, "dig": true, "fetch": true,
	"keys": true, "values": true, "include?": true, "key?": true,
	"respond_to?": true, "is_a?": true, "kind_of?": true, "instance_of?": true,
	"nil?": true, "present?": true, "blank?": true, "class": true,
	"to_s": true, "to_i": true, "to_f": true, "to_a": true, "to_h": true,
	"to_sym": true, "to_json": true, "to_proc": true, "to_str": true,
	"freeze": true, "frozen?": true, "dup": true, "clone": true,
	"tap": true, "then": true, "yield_self": true, "call": true,
	"new": true, "with": true, "await": true, "scope": true,
	"save": true, "save!": true, "valid?": true, "invalid?": true,
	"destroy": true, "destroy!": true, "update": true, "update!": true,
	"update_attribute": true, "update_attributes": true, "create": true, "create!": true,
	"build": true, "new_record?": true, "persisted?": true, "reload": true,
	"where": true, "order": true, "limit": true, "offset": true,
	"joins": true, "includes": true, "group": true, "having": true,
	"pluck": true, "distinct": true, "exists?": true, "touch": true,
	"inspect": true, "hash": true, "send": true, "public_send": true,
	"try": true, "method": true, "methods": true, "instance_variable_get": true,
	"instance_variable_set": true, "class_eval": true, "instance_eval": true,
	"define_method": true, "method_missing": true, "respond_to_missing?": true,
	"gsub": true, "gsub!": true, "sub": true, "sub!": true, "split": true,
	"join": true, "slice": true, "start_with?": true, "end_with?": true,
	"match": true, "match?": true, "scan": true, "strip": true,
	"upcase": true, "downcase": true, "capitalize": true,
	"attributes": true, "errors": true, "as_json": true, "run": true,
	"start": true, "stop": true, "close": true, "open": true,
	"process": true, "handle": true, "perform": true, "render": true,
	"notify": true,
}

// dslBlockScopeID synthesizes a per-block scope ID for a DSL call's
// `do...end` (or `{}`) body (DC.18, widened by DC.28 to Rails callback
// blocks): the block has no enclosing `def` to key a methodID on, but calls
// made directly inside it are still self-scoped exactly like a method body,
// so a per-block ID derived from the block's own file position lets the
// existing same-file bare-call resolution machinery (resolveBareCall, case
// "identifier") apply unchanged — both preCollectRubyLocals and walk call
// this so the two passes agree on the ID. `kind` distinguishes a rake block
// from a Rails callback block for indexer.go's classifyRoot (see
// dslBlockNode) and keeps the two DSL families from colliding on the same ID
// if a file (implausibly) used both DSL names at the same line.
func (ex *rubyExtractor) dslBlockScopeID(kind string, block *sitter.Node) string {
	return fmt.Sprintf("%s:%s:%s:%d", ex.service, ex.file, kind, rbLine(block))
}

// dslBlockNode mints the node a dslBlockScopeID actually needs to exist as:
// DC.18 introduced the ID as a lookup key for scope-tracking maps, but every
// edge addEdge emits keyed on it (calls into the block body, ivar
// reads/writes) needs a real graph.Node on the From side too, or the upsert
// violates the nodes/edges foreign key — this was missing and broke a full
// reindex on any repo with a rake task/namespace block (confirmed on
// orion's lib/tasks/audited.rake). meta.kind is what indexer.go's
// classifyRoot keys on to stamp root_kind=entrypoint (mirroring main/init):
// both a rake task (`rake task_name`) and a Rails callback block (invoked by
// the framework when the action/model event fires) are reached only by an
// external/framework trigger, never a static in-repo call site, so without
// this it would show up as a fresh deadcode false positive the moment it
// became a real node — exactly the class of bug this plan exists to
// prevent. Not stamped as root_kind here directly: classifyRoot
// unconditionally overwrites every Function/Method node's root_kind later in
// the pipeline, so anything set at parse time would just be clobbered.
func (ex *rubyExtractor) dslBlockNode(kind string, call *sitter.Node, mname string, block *sitter.Node, class string) {
	id := ex.dslBlockScopeID(kind, block)
	ex.addNode(graph.Node{
		ID: id, Type: graph.NodeTypeFunction, Label: rakeBlockLabel(call, mname, ex.src),
		Service: ex.service, File: ex.file, Line: rbLine(block), EndLine: rbEndLine(block), Language: "ruby",
		Meta: map[string]string{"kind": kind, "class": class},
	})
}

// rakeBlockLabel derives a friendly label for a synthetic task/namespace
// block scope node: the DSL's first symbol/string argument (the task or
// namespace name), or the bare DSL method name if none is found.
func rakeBlockLabel(call *sitter.Node, mname string, src []byte) string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return mname
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch a.Type() {
		case "simple_symbol":
			return strings.TrimPrefix(a.Content(src), ":")
		case "string":
			return strings.Trim(a.Content(src), `"'`)
		}
	}
	return mname
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

// preCollectRubyConstants scans the AST recursively to build constTable:
// every class/module-body-scope constant assignment (`CONST = ...`), keyed by
// bare name → nodeID (same shape as the node walk's own case "assignment"
// mints at line ~550). Runs before walk so a constant referenced earlier in
// the file than its definition (or from a sibling method) still resolves,
// and so walk's own case "constant" (DC.30) has a table to mint reads edges
// against — without this, a constant used only as a plain value anywhere
// (a hash value, array element, method/macro argument, condition — anything
// but a superclass, mixin argument, or call receiver, the three contexts
// case "constant" already special-cased) never got a reads edge at all,
// making every such constant a 100%-false-positive deadcode candidate the
// instant DC.29's variable-node minting made it visible to the scanner.
// Confirmed live on orion: 660/2294 (29%) of remaining flags were
// SCREAMING_CASE constants like ContainerAttribute::VALID_DATA_TYPES, used
// on the very next line (`validates :data_type, inclusion: { in:
// VALID_DATA_TYPES }`) but with zero reads edges of any kind.
func (ex *rubyExtractor) preCollectRubyConstants(node *sitter.Node, methodID string) {
	switch node.Type() {
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			methodID = ex.methodNodeID(nameNode.Content(ex.src), rbLine(node))
		}
	case "assignment", "operator_assignment":
		if methodID == "" {
			if left := node.ChildByFieldName("left"); left != nil && left.Type() == "constant" {
				name := left.Content(ex.src)
				ex.constTable[name] = fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, rbLine(node))
			}
		}
	case "call":
		// A task/namespace/Rails-callback-registration DSL block (see walk's
		// "task", "namespace" and railsCallbackBlockMethods cases) creates its
		// own scope the same way a real method body does: walk never mints a
		// variable node for a `CONST = ...` inside one (methodID is non-empty
		// there via dslBlockScopeID). This pass must treat it identically —
		// otherwise it registers a constTable entry pointing at a node that
		// will never exist, and a same-block reference resolving it mints a
		// reads edge to nothing (FK violation), confirmed live on orion's
		// lib/tasks/infra/org_create_and_configure.rake.
		if methodID == "" {
			if mn := node.ChildByFieldName("method"); mn != nil {
				mname := mn.Content(ex.src)
				if mname == "task" || mname == "namespace" || railsCallbackBlockMethods[mname] {
					if block := node.ChildByFieldName("block"); block != nil {
						methodID = "dsl_block"
					}
				}
			}
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyConstants(node.NamedChild(i), methodID)
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
	case "call":
		// DC.18/DC.28: a `task`/`namespace` or Rails callback-registration
		// DSL block has no enclosing `def`, so give its body a synthetic
		// method-like scope the same way walk does below — see
		// dslBlockScopeID.
		if methodID == "" {
			if mn := node.ChildByFieldName("method"); mn != nil {
				mname := mn.Content(ex.src)
				switch {
				case mname == "task" || mname == "namespace":
					if block := node.ChildByFieldName("block"); block != nil {
						methodID = ex.dslBlockScopeID("rake_block", block)
					}
				case railsCallbackBlockMethods[mname]:
					if block := node.ChildByFieldName("block"); block != nil {
						methodID = ex.dslBlockScopeID("callback_block", block)
					}
				}
			}
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
	if ex.isView {
		// A view has no enclosing method/class node to attribute a same-file
		// edge from (methodID is "" by construction), and its methods live in
		// separate helper-module files this single-file pass cannot see.
		// Ledger only — LinkRubyMixinMethods resolves it cross-file.
		if ledgerUnresolved && !isRubyBuiltinCall(mname, ex.file) {
			ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
				Service: ex.service, File: ex.file,
				Line: srcLine, Name: mname, Kind: "call_ref",
			})
		}
		return
	}
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
	case "constant":
		// DC.30: a bare constant reference — anywhere but the left side of
		// its own definition (skipped below, same guard shape as
		// instance_variable/class_variable above). Superclass, mixin
		// argument, and call-receiver constants also reach this case via
		// walk's unconditional child recursion; constTable only holds actual
		// `CONST = value` assignments, so those three (class/module names)
		// simply miss the lookup and no-op, same as before.
		if parent := node.Parent(); parent != nil {
			pt := parent.Type()
			if (pt == "assignment" || pt == "operator_assignment") && parent.ChildByFieldName("left") == node {
				break
			}
		}
		name := node.Content(ex.src)
		if id, ok := ex.constTable[name]; ok {
			if methodID != "" {
				ex.addEdge(graph.EdgeTypeReads, methodID, id, nil)
			} else if classID != "" {
				ex.addEdge(graph.EdgeTypeReads, classID, id, nil)
			}
		} else if (methodID != "" || classID != "") && rubyScreamingConstRE.MatchString(name) {
			// DC.31: same-file miss. Unlike a bare method call, this is NOT
			// ledgered for every "constant"-typed node — that grammar node
			// covers class/module names too (`User.find`, `raise
			// SomeError`, `ActiveRecord::Base`), which vastly outnumber real
			// constants and would flood the ledger with names no pass could
			// ever resolve (mirrors the "framework or builtin is not a
			// blind spot" reasoning in the bare-call branch below). Ruby's
			// naming convention — SCREAMING_SNAKE_CASE for a true constant,
			// CamelCase for a class/module — is a grammar-level distinction
			// tree-sitter itself doesn't make but every Ruby style guide
			// enforces, so filtering on it is a structural narrowing, not a
			// name-list guess. Confirmed live on orion: `Messaging::
			// Constants`' MT_* constants are `include`d into
			// DataServerCommunicatorAmqp and referenced unqualified there —
			// same cross-file mixin shape as DC.6's call_ref, resolved by
			// LinkRubyMixinConstants.
			ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
				Service: ex.service, File: ex.file, Line: rbLine(node), Name: name, Kind: "const_ref",
			})
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
		case "task", "namespace":
			// DC.18: a `task`/`namespace` DSL block has no enclosing `def`,
			// so bare calls made directly inside it never had a methodID to
			// key scope tracking off. Synthesize one scoped to the block —
			// the generic per-node recursion below then carries it into the
			// block body exactly like a real method scope would.
			if methodID == "" {
				if block := node.ChildByFieldName("block"); block != nil {
					methodID = ex.dslBlockScopeID("rake_block", block)
					ex.dslBlockNode("rake_block", node, mname, block, class)
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
			if methodID == "" && railsCallbackBlockMethods[mname] {
				// DC.28: same shape as the "task"/"namespace" case above, for
				// a Rails callback-registration DSL method called with a
				// block instead of a `:symbol` (the `:symbol` form is
				// resolved separately by internal/linker/rails_filters.go).
				// Gated on methodID=="" (class-body level, where a bare name
				// this common — e.g. `validate`, `before_save` — could
				// otherwise collide with an application-defined method of the
				// same name called from inside a real method body, which must
				// still fall through to ordinary bare-call attribution below).
				if block := node.ChildByFieldName("block"); block != nil {
					methodID = ex.dslBlockScopeID("callback_block", block)
					ex.dslBlockNode("callback_block", node, mname, block, class)
					break
				}
			}
			// Bare/implicit-self method calls: helper(x), save, self.foo.
			// A `ClassName.method` receiver is also resolvable when the class
			// is declared in this file — unambiguous the same way `Foo.new`
			// is above, just against methodsByClassName instead of
			// classTable. Any other receiver-typed call (article.save) needs
			// static type inference Ruby's dynamism rules out, so it is left
			// alone (rule 9: only attribute a call when the target is
			// unambiguous).
			if methodID == "" && !ex.isView {
				break
			}
			lookupClass := class
			if receiver := node.ChildByFieldName("receiver"); receiver != nil {
				switch {
				case receiver.Content(ex.src) == "self":
					// implicit self; lookupClass stays the enclosing class
				case receiver.Type() == "constant" || receiver.Type() == "scope_resolution":
					// Namespaced (`A::B.method`) resolves the same as a bare
					// constant when the callee is declared compactly in this
					// file (`class A::B`) — classTable is keyed off the
					// declaration's literal name text, which matches for that
					// spelling. A `module A; class B` nesting spelling isn't
					// matched here (classTable holds "B", not "A::B"); that
					// shape is the linker's job — LinkRubyClassMethodCalls
					// resolves it cross-file via rubyTypeIndex, which is
					// namespace-nesting-aware.
					lookupClass = receiver.Content(ex.src)
				case receiver.Type() == "identifier":
					// DC.24: a bare identifier filling the *receiver* slot
					// (`foo.bar_method`) is itself a call/local-read
					// ambiguity one level up the tree — the same shape case
					// "identifier" below resolves for a standalone read, just
					// structurally excluded from reaching that case by
					// isRubyBareCallExcluded's `receiver == node` branch.
					// Resolve it here instead: a memoized accessor
					// (`foo ||= Bar.new`) called only via `foo.bar_method`
					// must still be attributed as called, even though
					// `bar_method` itself remains unresolvable
					// (receiver-typed dispatch, handled by the goto next
					// bail below).
					// DC.29: ledgerUnresolved follows the same methodID != ""
					// gate as the standalone-identifier case above, and for
					// the same reason (an ERB view's untracked scriptlet
					// locals) — see that case's comment.
					recvName := receiver.Content(ex.src)
					if !ex.locals[methodID][recvName] {
						ex.resolveBareCall(recvName, class, class, methodID, rbLine(receiver), methodID != "")
					}
					// DC.32: `bar_method` in `foo.bar_method` is exactly the
					// same receiver-typed-dispatch blind spot as the default
					// case below (foo's actual class is unknown here just as
					// article's is for article.save) — foo being a bare
					// identifier only explains foo itself, not what its
					// value's class defines. This is empirically the
					// dominant shape (a local var or method param as
					// receiver), not an edge case: `org.customized_product_name`
					// looks nothing like `default:`'s example on the
					// surface, but is structurally identical to it once the
					// receiver itself is resolved above.
					if methodID != "" && !rubyCommonMethodNames[mname] {
						ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
							Service: ex.service, File: ex.file, Line: rbLine(node), Name: mname, Kind: "typed_call_ref",
						})
					}
					goto next
				default:
					// Any other receiver (article.save) needs static type
					// inference Ruby's dynamism rules out; a plain `break`
					// here would only exit this inner switch and fall through
					// to a resolution attempt, so bail out of the call
					// handling entirely instead.
					//
					// DC.32: not every such call is unrecoverable. When the
					// method name is not one of Ruby/Rails' extremely common
					// vocabulary (rubyCommonMethodNames), ledger it as a
					// candidate for LinkRubySoleDefinerCalls: a linker pass
					// that binds it only when exactly one method in the
					// whole service has this name — safe by construction,
					// since the receiver's actual class is then irrelevant
					// (there is nothing else it could be). Never ledgered
					// outside a real method body (methodID == "" is a
					// class-body/top-level call, already excluded from
					// resolution above this switch), and always stripped
					// from the ledger regardless of outcome by that pass —
					// see its doc comment for why.
					if methodID != "" && !rubyCommonMethodNames[mname] {
						ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
							Service: ex.service, File: ex.file, Line: rbLine(node), Name: mname, Kind: "typed_call_ref",
						})
					}
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
		if (methodID == "" && !ex.isView) || isRubyBareCallExcluded(node) {
			break
		}
		mname := node.Content(ex.src)
		if ex.locals[methodID][mname] {
			break
		}
		// DC.29: ledgerUnresolved is now methodID != "" (not the
		// unconditional false this had before). Inside a real method body,
		// the name is by this point confirmed not a local var and not
		// structurally excluded, so in Ruby grammar terms it can only be a
		// call — exactly as certain as the identically-shaped `case "call":`
		// bare-call branch above, which already ledgers (line ~713). The
		// paren'd and paren-less spellings of the same call
		// (`helper_method()` vs `helper_method`) were, before this fix,
		// treated asymmetrically: the paren'd form's same-file-unresolved
		// miss reached the `call_ref` ledger for LinkRubyMixinMethods
		// (DC.6) to resolve cross-file against an included concern/
		// superclass; the paren-less form (the far more common Ruby idiom
		// for a zero-arg accessor/helper, e.g. a mixin method used as a
		// plain hash value or `.any?`'s bare receiver) was silently
		// dropped — no edge, no ledger entry, nothing for any pass to ever
		// pick up. Still gated to methodID != "" (real method bodies only):
		// an ERB view's top-level scriptlet scope also reaches this case
		// (methodID=="" but ex.isView true) and its locally-assigned
		// scriptlet variables are NOT tracked by addLocal (a no-op for
		// methodID==""), so the locals check two lines up can't tell a view
		// local from a call there the way it reliably can inside a method —
		// ledgering those would misreport genuine local reads as unresolved
		// calls (see TestRubyVariables_ERBViewLocalNotMistakenForCall).
		// Confirmed live on orion: `Organization#dss_org_info`'s
		// `distinguished_domain_names: distinguished_domain_names` calls
		// `OrganizationConfiguration#distinguished_domain_names` (a mixin
		// method) this way; before this fix it had zero inbound edges and
		// zero ledger rows of any kind.
		ex.resolveBareCall(mname, class, class, methodID, rbLine(node), methodID != "")
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
