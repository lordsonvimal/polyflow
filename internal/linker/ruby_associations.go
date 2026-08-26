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
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// LinkRubyAssociations resolves ActiveRecord `has_many`/`belongs_to`/`has_one`
// macros to class-granularity `calls` edges. Cross-file is the common case
// (the owning model and the associated model are almost always separate
// files), so — like LinkRubyClassMethodCalls — this lives in the linker
// rather than the same-file parser pass.
//
// `has_many`/`belongs_to`/`has_one` have no receiver, so nothing in
// extractRubyVariables (internal/parser/ruby_variables.go) ever inspects
// their symbol argument: the `call` node resolves as an ordinary method
// call, fails to match anything local or builtin, and is dropped. The
// association itself — a direct, one-hop, developer-obvious relationship
// between two models — never became an edge at all.
//
// The naive target (singularize the association name, or take it verbatim
// for belongs_to/has_one, then classify) is used even for `through:` and
// `as:` (polymorphic) associations: it still names the right class in the
// overwhelmingly common case where the association name matches the far
// model's name, and a miss just means no edge (today's status quo), not a
// wrong one. `has_and_belongs_to_many`, polymorphic targets, and STI are out
// of scope — see docs/ruby-activerecord-association-plan.md.
func LinkRubyAssociations(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	byNameByService := make(map[string]map[string][]string)
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
		}
		byName[n.Label] = append(byName[n.Label], n.ID)
		classTotal++
	}
	if classTotal == 0 {
		return nil, nil
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

		byName := byNameByService[svcName]
		if byName == nil {
			byName = map[string][]string{}
		}

		var refs []classAssociationRef
		for _, file := range files {
			if !isRubyFile(file) {
				continue
			}
			refs = append(refs, scanRubyAssociations(file, svcName)...)
		}

		for _, ref := range refs {
			edges, unresolved := emitAssociation(svcName, byName, ref, seen)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

// classAssociationRef is one `has_many`/`belongs_to`/`has_one` declaration to
// resolve, mirroring classMethodCallRef's shape.
type classAssociationRef struct {
	ownerClassID   string
	ownerClassName string
	targetClass    string
	file           string
	line           int
}

func emitAssociation(
	svcName string,
	byName map[string][]string,
	ref classAssociationRef,
	seen map[string]bool,
) ([]graph.Edge, []graph.UnresolvedRef) {
	targets := byName[ref.targetClass]
	if len(targets) == 0 {
		// External gem/Rails engine model this service doesn't declare —
		// same reasoning LinkRubyClassMethodCalls uses for an unresolved
		// receiver: not a miss this pass owns.
		return nil, nil
	}

	var unresolved []graph.UnresolvedRef
	conf := graph.ConfidenceInferred
	if len(targets) > 1 {
		conf = graph.ConfidencePartial
		missKey := fmt.Sprintf("association_collision:%s:%d:%s->%s", ref.file, ref.line, ref.ownerClassName, ref.targetClass)
		if !seen[missKey] {
			seen[missKey] = true
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: svcName, File: ref.file, Line: ref.line,
				Name: ref.ownerClassName + "->" + ref.targetClass, Kind: "association_collision",
			})
		}
	}

	var edges []graph.Edge
	for _, targetID := range targets {
		eid := fmt.Sprintf("calls:%s->%s:association", ref.ownerClassID, targetID)
		if seen[eid] {
			continue
		}
		seen[eid] = true
		edges = append(edges, graph.Edge{
			ID: eid, From: ref.ownerClassID, To: targetID,
			Type: graph.EdgeTypeCalls, Confidence: conf,
			Meta: map[string]string{"via": "association", "granularity": "class"},
		})
	}
	return edges, unresolved
}

// ---------------------------------------------------------------------------
// scan
// ---------------------------------------------------------------------------

var rubyAssociationMacros = map[string]bool{
	"has_many": true, "belongs_to": true, "has_one": true,
}

// scanRubyAssociations walks a Ruby file for has_many/belongs_to/has_one
// calls that sit directly in a class body — guarding against a same-named
// local helper method defined outside a model, the same reasoning
// scanRubyClassMethodCalls' class-body walk uses.
func scanRubyAssociations(file, svcName string) []classAssociationRef {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	file = patterns.RelativizeToCwd(file)
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()

	var refs []classAssociationRef
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "class" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				line := int(n.StartPoint().Row) + 1
				classID := fmt.Sprintf("%s:%s:class:%s:%d", svcName, file, clsName, line)
				if body := n.ChildByFieldName("body"); body != nil {
					for i := 0; i < int(body.NamedChildCount()); i++ {
						m := body.NamedChild(i)
						if m.Type() != "call" {
							continue
						}
						mn := m.ChildByFieldName("method")
						if mn == nil || !rubyAssociationMacros[mn.Content(src)] {
							continue
						}
						args := m.ChildByFieldName("arguments")
						if args == nil {
							continue
						}
						target := associationTarget(mn.Content(src), args, src)
						if target == "" {
							continue
						}
						refs = append(refs, classAssociationRef{
							ownerClassID: classID, ownerClassName: clsName,
							targetClass: target, file: file,
							line: int(m.StartPoint().Row) + 1,
						})
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return refs
}

// associationTarget reads the target class name for one association call:
// the explicit `class_name:` override if present, otherwise the association
// symbol itself, singularized (has_many only) and classified. Returns "" for
// any shape other than a leading `simple_symbol` (a dynamic symbol or a
// variable) — a rare shape, not a structural miss worth ledgering, same
// reasoning the bare-identifier tier used.
func associationTarget(macro string, args *sitter.Node, src []byte) string {
	var symbolText, className string
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch a.Type() {
		case "simple_symbol":
			if symbolText == "" {
				symbolText = strings.TrimPrefix(a.Content(src), ":")
			}
		case "pair":
			key := a.ChildByFieldName("key")
			value := a.ChildByFieldName("value")
			if key == nil || value == nil {
				continue
			}
			keyText := strings.TrimSuffix(strings.TrimPrefix(key.Content(src), ":"), ":")
			if keyText == "class_name" {
				className = extractRubyStringContent(value, src)
			}
		}
	}
	if symbolText == "" {
		return ""
	}
	if className != "" {
		return className
	}
	if macro == "has_many" {
		return snakeToClassName(railsinflect.Singularize(symbolText))
	}
	return snakeToClassName(symbolText)
}

// snakeToClassName converts a snake_case name to a PascalCase class name
// (`deliverable` → `Deliverable`, `lyra_batch_job` → `SceBatchJob`). No
// camelize/classify helper exists anywhere else in the repo (verified:
// `grep -rn "func.*[Cc]amel" internal/` returns nothing).
func snakeToClassName(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}
