package linker

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkRubyReceiverTypeCalls resolves method calls whose receiver is a local
// variable, a memoized instance variable, or a same-class zero-arg
// "memo-reader" method — `x = Product.new; x.save`, `@svc ||= Aws.new;
// @svc.upload`, `def aws; @aws ||= AwsFacade.new_instance; end` then
// `aws.complete_multipart_upload`. extractRubyVariables (same-file) and
// LinkRubyClassMethodCalls (cross-file) both explicitly stop at a literal
// constant receiver — "any other receiver... needs static type inference
// Ruby's dynamism rules out" — because in general that is true. This pass
// covers the syntactically-recoverable slice of that gap: a receiver whose
// type traces back, through at most one assignment or memoized-ivar hop, to
// a literal `Const.new(...)` (or a class-method call whose own body is
// itself just `Const.new(...)`, the common Ruby self-factory shape —
// `AwsFacade.new_instance` delegating to `AwsFacade.new` is exactly this).
//
// DC.5 extends this to three more shapes, all still syntactic (no arbitrary
// return-type inference): an implicit-self `new(...)` inside a singleton
// context (`class << self`/`def self.x`) resolves to the enclosing class;
// an inline `<new-expr>.method` chain with no intermediate variable
// resolves the same way an assigned-variable receiver does; and a
// classmethod literally named `call` whose body is exactly that inline
// chain (the "service object" convention: `SomeService.call(x)` delegating
// to `SomeService.new(x).call`) has its return type narrowly inferred as
// "the same class's instance" — not the instance `call` method's own return
// value, which stays out of scope as unbounded in general.
//
// Type inference is purely syntactic (constant name propagation) and does
// not depend on cross-file resolution, so it runs once per service over
// every file's AST before any call site is resolved. Call resolution then
// reuses ruby_class_method_calls.go's rubyTypeIndex/emitClassMethodCall
// machinery unchanged — an inferred receiver is, by the time it reaches
// that code, just another constant name to resolve.
//
// DC.6 wires emitClassMethodCall's downward override fan-out into this pass
// specifically (never into LinkRubyClassMethodCalls): an inferred receiver's
// true runtime type could be any subtype of the class traced here, so a
// call site this pass resolves to `A#foo` must also reach every `B#foo`
// where B is a descendant of A that overrides it — see
// ruby_override_dispatch.go.
func LinkRubyReceiverTypeCalls(nodes []graph.Node, edges []graph.Edge, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	byNameByService := make(map[string]map[string][]string)
	byDeclByService := make(map[string]map[string]string)
	fileByID := make(map[string]string)
	classTotal := 0
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass {
			continue
		}
		byName := byNameByService[n.Service]
		if byName == nil {
			byName = make(map[string][]string)
			byNameByService[n.Service] = byName
			byDeclByService[n.Service] = make(map[string]string)
		}
		byName[n.Label] = append(byName[n.Label], n.ID)
		byDeclByService[n.Service][declKey(n.File, n.Label, n.Line)] = n.ID
		fileByID[n.ID] = n.File
		classTotal++
	}
	if classTotal == 0 {
		return nil, nil
	}

	methodsByClass := buildMethodsByClass(nodes)

	// DC.6: an inferred receiver's true runtime type could be any subtype of
	// the class ruby_receiver_types.go traced it to (unlike a literal
	// `Const.method` call, which names an exact class) — see
	// ruby_override_dispatch.go's doc comment. Building on the same
	// rubyMixinIndex the mixin pass already builds from these same
	// `inherits` edges, not a second full scan.
	var ox *rubyOverrideIndex
	if mixinIx := newRubyMixinIndex(nodes, edges); len(mixinIx.ancestors) > 0 {
		ox = newRubyOverrideIndex(mixinIx)
	}

	svcNames := make([]string, 0, len(serviceFiles))
	for svcName := range serviceFiles {
		svcNames = append(svcNames, svcName)
	}
	sort.Strings(svcNames)

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for _, svcName := range svcNames {
		files := append([]string{}, serviceFiles[svcName]...)
		sort.Strings(files)
		var rubyFiles []string
		for _, f := range files {
			if isRubyFile(f) {
				rubyFiles = append(rubyFiles, f)
			}
		}

		asts := parseRubyFiles(rubyFiles)

		// Phases 1-2 (ivar types, then same-class zero-arg method return
		// types) run for a few rounds so a chained self-factory resolves
		// regardless of file processing order: round 1 discovers
		// `AwsFacade.new_instance` returns AwsFacade (direct `Const.new` in
		// its own body); round 2 then discovers `aws` returns AwsFacade too
		// (its body is `@aws ||= AwsFacade.new_instance`, only resolvable once
		// `new_instance`'s return type is known). A fixed 3 rounds is enough
		// for any chain this repo's evidence showed (at most one indirection
		// past the literal `Const.new`) without an unbounded/cyclic fixpoint
		// loop for a pathological mutual-reference case.
		ivarType := map[string]string{}
		methodReturnType := map[string]string{}
		for round := 0; round < 3; round++ {
			for _, a := range asts {
				collectRubyIvarTypes(a.root, a.src, "", false, ivarType, methodReturnType)
			}
			for _, a := range asts {
				collectRubyMethodReturnTypes(a.root, a.src, "", false, ivarType, methodReturnType)
			}
		}

		ix := &rubyTypeIndex{
			svc:      svcName,
			byName:   byNameByService[svcName],
			byQual:   map[string][]string{},
			fileByID: fileByID,
		}
		if ix.byName == nil {
			ix.byName = map[string][]string{}
		}
		byDecl := byDeclByService[svcName]
		for _, a := range asts {
			for _, d := range collectRubyClassDecls(a.root, a.src, a.file, nil) {
				if id, ok := byDecl[declKey(d.file, d.name, d.line)]; ok {
					ix.byQual[d.qualified()] = append(ix.byQual[d.qualified()], id)
				}
			}
		}

		var refs []classMethodCallRef
		for _, a := range asts {
			refs = append(refs, scanRubyReceiverTypedCalls(a.root, a.src, a.file, svcName, ivarType, methodReturnType)...)
		}

		for _, ref := range refs {
			callEdges, unresolved := emitClassMethodCall(ix, ref, methodsByClass, seen, ox)
			allEdges = append(allEdges, callEdges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

// ---------------------------------------------------------------------------
// shared file parsing
// ---------------------------------------------------------------------------

type rubyRTFileAST struct {
	file string
	src  []byte
	root *sitter.Node
	tree *sitter.Tree
}

// parseRubyFiles parses every file once, reused across this pass's phases —
// scanRubyClassMethodCalls's per-pass re-parse is fine for one pass, but this
// one walks each file three times (ivar types, return types, call refs).
func parseRubyFiles(files []string) []rubyRTFileAST {
	out := make([]rubyRTFileAST, 0, len(files))
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		rel := patterns.RelativizeToCwd(file)
		p := sitter.NewParser()
		p.SetLanguage(rubysitter.GetLanguage())
		tree, err := p.ParseCtx(context.Background(), nil, src)
		if err != nil || tree == nil {
			continue
		}
		out = append(out, rubyRTFileAST{file: rel, src: src, root: tree.RootNode(), tree: tree})
	}
	return out
}

// collectRubyClassDecls mirrors scanRubyClassMethodCalls's decls collection
// (kept separate since that function's decls/refs are entangled with its own
// single-purpose walk).
func collectRubyClassDecls(node *sitter.Node, src []byte, file string, ns []string) []rubyDecl {
	var out []rubyDecl
	var walk func(n *sitter.Node, ns []string)
	walk = func(n *sitter.Node, ns []string) {
		inner := ns
		if t := n.Type(); t == "class" || t == "module" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				line := int(n.StartPoint().Row) + 1
				out = append(out, rubyDecl{name: clsName, ns: ns, file: file, line: line})
				inner = append(append([]string{}, ns...), strings.Split(clsName, "::")...)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), inner)
		}
	}
	walk(node, ns)
	return out
}

// inferRubyNewClass reports the constant name if n is (directly) a
// `Const.new(...)` call — the one syntactic shape this pass treats as
// unambiguous type evidence, the same shape extractRubyVariables' EdgeTypeInstantiates
// case already recognises for same-file `Foo.new`.
func inferRubyNewClass(n *sitter.Node, src []byte) string {
	if n == nil || n.Type() != "call" {
		return ""
	}
	mn := n.ChildByFieldName("method")
	if mn == nil || mn.Content(src) != "new" {
		return ""
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil || recv.Type() != "constant" {
		return ""
	}
	return recv.Content(src)
}

// inferRubyExprClass extends inferRubyNewClass to a self-factory call —
// `Const.new_instance(...)`, `Const.build(...)`, any name — whose OWN return
// type methodReturnType already knows (populated by an earlier round of
// collectRubyMethodReturnTypes over the same file set). This is what lets
// `AwsFacade.new_instance` resolve to AwsFacade once `new_instance`'s own
// body (`AwsFacade.new(...)`) has been inferred in a prior round — see the
// round loop in LinkRubyReceiverTypeCalls.
func inferRubyExprClass(n *sitter.Node, src []byte, methodReturnType map[string]string) string {
	if cls := inferRubyNewClass(n, src); cls != "" {
		return cls
	}
	if n == nil || n.Type() != "call" {
		return ""
	}
	mn := n.ChildByFieldName("method")
	recv := n.ChildByFieldName("receiver")
	if mn == nil || recv == nil || recv.Type() != "constant" {
		return ""
	}
	return methodReturnType[recv.Content(src)+"\x00"+mn.Content(src)]
}

// inferRubyNewClassCtx extends inferRubyNewClass to DC.5's implicit-self
// shape: inside a singleton context (`class << self` or `def self.x`), a
// receiverless `new(...)` refers to the enclosing class itself, exactly the
// way an explicit `Const.new(...)` already does. Outside a singleton context
// a receiverless `new` is not `self.class.new` — Ruby has no such implicit
// dispatch — so selfIsClass gates this strictly.
func inferRubyNewClassCtx(n *sitter.Node, src []byte, class string, selfIsClass bool) string {
	if cls := inferRubyNewClass(n, src); cls != "" {
		return cls
	}
	if n == nil || n.Type() != "call" || !selfIsClass || class == "" {
		return ""
	}
	mn := n.ChildByFieldName("method")
	if mn == nil || mn.Content(src) != "new" {
		return ""
	}
	if recv := n.ChildByFieldName("receiver"); recv != nil {
		return ""
	}
	return class
}

// inferRubyInlineChainClass recognizes DC.5's "zero-hop inline chain" shape —
// `<new-expr>.method` with no intermediate variable, where <new-expr> is
// either a literal `Const.new(...)` or (inside a singleton context) an
// implicit-self `new(...)`. Returns the instantiated class, or "" if n is not
// this shape.
func inferRubyInlineChainClass(n *sitter.Node, src []byte, class string, selfIsClass bool) string {
	if n == nil || n.Type() != "call" {
		return ""
	}
	recv := n.ChildByFieldName("receiver")
	if recv == nil || recv.Type() != "call" {
		return ""
	}
	return inferRubyNewClassCtx(recv, src, class, selfIsClass)
}

// inferRubyExprClassCtx is inferRubyExprClass widened with the two DC.5
// shapes above, for callers that need singleton-context awareness (ivar and
// local-variable assignment right-hand sides).
func inferRubyExprClassCtx(n *sitter.Node, src []byte, class string, selfIsClass bool, methodReturnType map[string]string) string {
	if cls := inferRubyNewClassCtx(n, src, class, selfIsClass); cls != "" {
		return cls
	}
	if cls := inferRubyInlineChainClass(n, src, class, selfIsClass); cls != "" {
		return cls
	}
	return inferRubyExprClass(n, src, methodReturnType)
}

// lastMeaningfulStatement returns the last named (non-comment) child of a
// method body, or nil for an empty body.
func lastMeaningfulStatement(body *sitter.Node) *sitter.Node {
	if body == nil {
		return nil
	}
	for i := int(body.NamedChildCount()) - 1; i >= 0; i-- {
		c := body.NamedChild(i)
		if c.Type() != "comment" {
			return c
		}
	}
	return nil
}

// collectRubyIvarTypes walks node recording, for every `@ivar = Const.new`
// or `@ivar ||= Const.new` assignment, the class an instance/class variable
// was memoized to. class tracks the syntactically enclosing class/module
// name (simple name, not fully qualified — matching methodsByClass's join
// key elsewhere in this package). selfIsClass tracks whether the current
// position is inside a singleton context (`class << self` or `def self.x`)
// where implicit `self` refers to class itself — DC.5's implicit-receiver
// `new` shape is only valid there.
func collectRubyIvarTypes(node *sitter.Node, src []byte, class string, selfIsClass bool, ivarType, methodReturnType map[string]string) {
	t := node.Type()
	if t == "class" || t == "module" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			class = nameNode.Content(src)
		}
		selfIsClass = false
	}
	if t == "singleton_class" || t == "singleton_method" {
		selfIsClass = true
	}
	if t == "assignment" || t == "operator_assignment" {
		left := node.ChildByFieldName("left")
		right := node.ChildByFieldName("right")
		if left != nil && right != nil && class != "" &&
			(left.Type() == "instance_variable" || left.Type() == "class_variable") {
			if cls := inferRubyExprClassCtx(right, src, class, selfIsClass, methodReturnType); cls != "" {
				ivarType[class+"\x00"+left.Content(src)] = cls
			}
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		collectRubyIvarTypes(node.NamedChild(i), src, class, selfIsClass, ivarType, methodReturnType)
	}
}

// collectRubyMethodReturnTypes walks node recording, for every method whose
// body's last statement syntactically resolves to a known class, class +
// "\x00" + methodName → that class. Four shapes are recognised, in order: a
// trailing `Const.new(...)` (a plain factory method); a trailing
// `@ivar ||=`/`@ivar =` assignment to `Const.new(...)` (a memo-reader
// writing its memo and returning it in the same expression — Ruby's `||=`
// evaluates to the assigned value); a trailing bare `@ivar` read whose type
// ivarType already knows (a memo-reader whose memoizing assignment isn't the
// last line); and DC.5's narrow "service object" convention — a classmethod
// literally named `call`, inside a singleton context, whose body is the
// inline chain `new(...).call` — inferred as returning "the same class's
// instance" (not the instance `call` method's own return value, which is
// unbounded in general — see docs/deadcode-false-positive-plan.md DC.5).
// `class << self` bodies are covered the same as `def self.x` via
// selfIsClass, tracked the same way scanRubyReceiverTypedCalls tracks it.
func collectRubyMethodReturnTypes(node *sitter.Node, src []byte, class string, selfIsClass bool, ivarType, methodReturnType map[string]string) {
	t := node.Type()
	if t == "class" || t == "module" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			class = nameNode.Content(src)
		}
		selfIsClass = false
	}
	if t == "singleton_class" || t == "singleton_method" {
		selfIsClass = true
	}
	if t == "method" || t == "singleton_method" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil && class != "" {
			name := nameNode.Content(src)
			last := lastMeaningfulStatement(node.ChildByFieldName("body"))
			if last != nil {
				cls := ""
				switch last.Type() {
				case "assignment", "operator_assignment":
					if left := last.ChildByFieldName("left"); left != nil &&
						(left.Type() == "instance_variable" || left.Type() == "class_variable") {
						if right := last.ChildByFieldName("right"); right != nil {
							cls = inferRubyExprClassCtx(right, src, class, selfIsClass, methodReturnType)
						}
					}
				case "instance_variable", "class_variable":
					cls = ivarType[class+"\x00"+last.Content(src)]
				case "call":
					if name == "call" && selfIsClass {
						cls = inferRubyInlineChainClass(last, src, class, selfIsClass)
					}
					if cls == "" {
						cls = inferRubyExprClass(last, src, methodReturnType)
					}
				}
				if cls != "" {
					methodReturnType[class+"\x00"+name] = cls
				}
			}
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		collectRubyMethodReturnTypes(node.NamedChild(i), src, class, selfIsClass, ivarType, methodReturnType)
	}
}

// scanRubyReceiverTypedCalls walks file collecting a classMethodCallRef for
// every call whose receiver is:
//   - a local variable last assigned `= Const.new(...)` (or, per DC.5, any
//     other traceable receiver expression inferRubyExprClassCtx recognises)
//     earlier in the same method (flow-insensitive: the most recent
//     assignment textually before the call site wins, since Go's tree-sitter
//     walk is already depth/source order);
//   - an instance/class variable memoized via ivarType;
//   - a bare same-class method call (no args, no explicit receiver) whose
//     return type methodReturnType already knows — the memo-reader shape;
//   - (DC.5) an inline chain — the receiver is itself a `new(...)` call
//     (literal-constant or, inside a singleton context, implicit-self) with
//     no intermediate variable, e.g. `new(x).method` inside `def self.call`.
//
// `new`/`include`/`extend`/`prepend` and constant/self receivers are
// excluded: those are already handled by extractRubyVariables or
// LinkRubyClassMethodCalls. selfIsClass tracks whether implicit `self` at the
// current position refers to the enclosing class (`class << self`/`def
// self.x`), the same way collectRubyMethodReturnTypes tracks it.
func scanRubyReceiverTypedCalls(root *sitter.Node, src []byte, file, svcName string, ivarType, methodReturnType map[string]string) []classMethodCallRef {
	var refs []classMethodCallRef

	var walk func(n *sitter.Node, class string, ns []string, methodID string, locals map[string]string, selfIsClass bool)
	walk = func(n *sitter.Node, class string, ns []string, methodID string, locals map[string]string, selfIsClass bool) {
		inner := ns
		if n.Type() == "singleton_class" || n.Type() == "singleton_method" {
			selfIsClass = true
		}
		switch n.Type() {
		case "class", "module":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				class = clsName
				inner = append(append([]string{}, ns...), strings.Split(clsName, "::")...)
			}
			selfIsClass = false
		case "method", "singleton_method":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				methodID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, file, nameNode.Content(src), int(n.StartPoint().Row)+1)
			}
			// Fresh local-type scope per method: a local in one method tells us
			// nothing about a same-named local in another.
			locals = map[string]string{}
		case "assignment", "operator_assignment":
			if left := n.ChildByFieldName("left"); left != nil && left.Type() == "identifier" && methodID != "" {
				if right := n.ChildByFieldName("right"); right != nil {
					if cls := inferRubyExprClassCtx(right, src, class, selfIsClass, methodReturnType); cls != "" {
						locals[left.Content(src)] = cls
					}
				}
			}
		case "call":
			if methodID != "" {
				if mn := n.ChildByFieldName("method"); mn != nil {
					mname := mn.Content(src)
					switch mname {
					case "new", "include", "extend", "prepend":
						// resolved elsewhere (instantiates/inherits)
					default:
						if recv := n.ChildByFieldName("receiver"); recv != nil {
							cls := ""
							switch recv.Type() {
							case "identifier":
								if t, ok := locals[recv.Content(src)]; ok {
									cls = t
								} else if class != "" {
									cls = methodReturnType[class+"\x00"+recv.Content(src)]
								}
							case "instance_variable", "class_variable":
								if class != "" {
									cls = ivarType[class+"\x00"+recv.Content(src)]
								}
							case "call":
								cls = inferRubyNewClassCtx(recv, src, class, selfIsClass)
							}
							if cls != "" {
								refs = append(refs, classMethodCallRef{
									receiver: cls, ns: append([]string{}, ns...),
									file: file, line: int(n.StartPoint().Row) + 1,
									fromID: methodID, mname: mname, inferred: true,
								})
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), class, inner, methodID, locals, selfIsClass)
		}
	}
	walk(root, "", nil, "", map[string]string{}, false)
	return refs
}
