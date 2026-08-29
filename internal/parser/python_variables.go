package parser

import (
	"context"
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"
	pythonsitter "github.com/smacker/go-tree-sitter/python"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// resolvePythonAttributeCalls resolves same-file Python attribute calls
// (self.foo(), cls.foo(), and typed-local `x = Foo(); x.bar()`) into `calls`
// edges between the function/class nodes patterns/python/functions.yaml
// already emitted. Tier PC (docs/python-parity-plan.md): before this pass,
// patterns/python/functions.yaml only matched bare-identifier callees
// (`function: (identifier)`), so every instance-method call — Python's
// primary call shape — produced zero node, zero edge, zero ledger entry.
//
// Python's call grammar is unambiguous — every call with parens is a `call`
// node, verified against the real grammar (see Tier PC's pinned facts) — so
// unlike Ruby's bare-identifier tier this needs no call-vs-read
// disambiguation, only receiver typing.
//
// An attribute call whose receiver is neither self/cls nor a tracked local
// resolves to nothing and is silently dropped: it is the common,
// honestly-unknown case (a function parameter, a dict value, a stdlib
// object), not a structural miss, so it is deliberately not ledgered — same
// policy Ruby's bare-identifier tier uses for its unresolved case
// (docs/ruby-bare-identifier-call-plan.md).
func resolvePythonAttributeCalls(file string, src []byte, nodes []graph.Node) []graph.Edge {
	p := sitter.NewParser()
	p.SetLanguage(pythonsitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	type span struct {
		line, end int
		id, label string
	}
	var classSpans, funcSpans []span
	classByLabel := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.File != file {
			continue
		}
		s := span{n.Line, n.EndLine, n.ID, n.Label}
		switch n.Type {
		case graph.NodeTypeClass:
			classSpans = append(classSpans, s)
			if _, ok := classByLabel[n.Label]; !ok {
				classByLabel[n.Label] = n.ID
			}
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			funcSpans = append(funcSpans, s)
		}
	}
	if len(funcSpans) == 0 {
		return nil
	}

	// innermost returns the tightest span (latest start line) containing
	// line, among spans with a known end line — an unbounded (end==0) span
	// is never a candidate, since it would swallow everything after it.
	innermost := func(spans []span, line int) (span, bool) {
		best := -1
		for i := range spans {
			s := &spans[i]
			if s.end == 0 || s.line > line || line > s.end {
				continue
			}
			if best == -1 || s.line > spans[best].line {
				best = i
			}
		}
		if best == -1 {
			return span{}, false
		}
		return spans[best], true
	}

	// methodsByClass[classID][methodName] = methodID, built by attributing
	// each function span to its innermost enclosing class span. A function
	// with no enclosing class (module-level) is simply never added — it
	// can't be a self/cls or typed-instance target.
	methodsByClass := map[string]map[string]string{}
	for _, f := range funcSpans {
		cls, ok := innermost(classSpans, f.line)
		if !ok {
			continue
		}
		if methodsByClass[cls.id] == nil {
			methodsByClass[cls.id] = map[string]string{}
		}
		methodsByClass[cls.id][f.label] = f.id
	}

	// Locals pre-pass: `x = ClassName(...)` inside a method body binds `x`
	// to ClassName for the rest of that method — conservative, whole-method
	// scope, same simplification Ruby's locals pre-pass uses
	// (preCollectRubyLocals in ruby_variables.go).
	locals := map[string]map[string]string{} // methodID -> varName -> classID
	var collectLocals func(n *sitter.Node)
	collectLocals = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "identifier" && right.Type() == "call" {
				if fn := right.ChildByFieldName("function"); fn != nil && fn.Type() == "identifier" {
					if classID, ok := classByLabel[fn.Content(src)]; ok {
						line := int(n.StartPoint().Row) + 1
						if m, ok := innermost(funcSpans, line); ok {
							if locals[m.id] == nil {
								locals[m.id] = map[string]string{}
							}
							locals[m.id][left.Content(src)] = classID
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectLocals(n.NamedChild(i))
		}
	}
	collectLocals(tree.RootNode())

	var edges []graph.Edge
	seen := map[string]bool{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "call" {
			if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "attribute" {
				obj := fn.ChildByFieldName("object")
				attr := fn.ChildByFieldName("attribute")
				if obj != nil && attr != nil && obj.Type() == "identifier" {
					line := int(n.StartPoint().Row) + 1
					if caller, ok := innermost(funcSpans, line); ok {
						objName := obj.Content(src)
						methodName := attr.Content(src)
						var classID string
						switch {
						case objName == "self" || objName == "cls":
							if cls, ok := innermost(classSpans, line); ok {
								classID = cls.id
							}
						case locals[caller.id] != nil:
							classID = locals[caller.id][objName]
						}
						if classID != "" {
							if calleeID, ok := methodsByClass[classID][methodName]; ok && calleeID != caller.id {
								edgeID := fmt.Sprintf("%s:%s->%s", graph.EdgeTypeCalls, caller.id, calleeID)
								if !seen[edgeID] {
									seen[edgeID] = true
									edges = append(edges, graph.Edge{
										ID:   edgeID,
										From: caller.id,
										To:   calleeID,
										Type: graph.EdgeTypeCalls,
									})
								}
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())

	return edges
}
