package parser

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// ShellParser parses shell script source files (.sh, .bash) and Bats-core
// test files (.bats — Bats scripts are ordinary bash-dialect content; no
// test-file special-casing, SH0's rule-8 disposition).
type ShellParser struct{}

func (p *ShellParser) Language() string     { return "bash" }
func (p *ShellParser) Extensions() []string { return []string{".sh", ".bash", ".bats"} }

// shellInvocationPatternNames are patterns/shell/invocation.yaml's pattern
// names (SH1). They are never fed to patterns.MatchToGraph: an invocation
// site is a cross-file relationship, not an in-file node, and resolving it
// needs the whole workspace's node set — that happens in a dedicated linker
// pass (linker.LinkShellInvocationEdges), which re-parses independently, the
// same shape internal/linker/import_edges.go's Ruby require_relative pass
// already uses. This file only handles the part that IS knowable per-file:
// a dynamic (variable-built) invocation target can never resolve, so it is
// ledgered here immediately rather than deferred (SH1, rule 12).
var shellInvocationPatternNames = map[string]bool{
	"shell_invocation_verb": true,
	"shell_invocation_bare": true,
}

func (p *ShellParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}
	results, matchErr := matcher.Match("bash", file, src)

	var defResults []patterns.MatchResult
	var unresolved []graph.UnresolvedRef
	for _, r := range results {
		if !shellInvocationPatternNames[r.PatternName] {
			defResults = append(defResults, r)
			continue
		}
		// SH1's narrow literal-vs-variable check: inspects the retained
		// tree-sitter node for the @path capture directly (not routed
		// through contract.KeyWalker — shell has no producer/consumer
		// key concept per the checklist's item-4 descope; this is a
		// plain structural check specific to "is this path argument
		// built from a variable").
		node := r.KeyNodes["path"]
		if node == nil || shellPathIsLiteral(node) {
			continue // literal: left for the linker pass to resolve
		}
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: service, File: r.File, Line: r.Line,
			Name: r.Captures["path"], Kind: "shell_invocation_dynamic",
		})
	}

	nodes, edges, defUnresolved := patterns.MatchToGraph(service, defResults)
	setLanguage(nodes, "bash")
	unresolved = append(unresolved, defUnresolved...)

	// SH0: guarantee every shell file has a landing node for cross-file
	// `exec` edges (SH1) even when it declares only functions and has no
	// top-level statement that would otherwise lazily trigger
	// MatchToGraph's synthetic (script) scope node. Deduped by ID against
	// whatever MatchToGraph already produced.
	relFile := patterns.RelativizeToCwd(file)
	scriptID := scriptNodeID(service, relFile)
	hasScript := false
	for i := range nodes {
		if nodes[i].ID == scriptID {
			hasScript = true
			break
		}
	}
	if !hasScript {
		nodes = append(nodes, graph.Node{
			ID:       scriptID,
			Type:     graph.NodeTypeFunction,
			Label:    "(script)",
			Service:  service,
			File:     relFile,
			Line:     0,
			Language: "bash",
			Meta:     map[string]string{"scope": "script"},
		})
	}

	return nodes, edges, unresolved, matchErr
}

// scriptNodeID returns the synthetic per-file (script) scope node ID for a
// shell file — the same ID format matcher.go's MatchToGraph module-scope
// fallback uses (moduleScopeFor), so the two never mint duplicate nodes for
// the same file.
func scriptNodeID(service, relFile string) string {
	return service + ":" + relFile + ":function:(script):0"
}

// shellPathIsLiteral reports whether a captured path/command-name node is an
// all-literal value (safe to resolve) or built from a shell expansion
// (variable, command substitution, concatenation — must be ledgered, never
// guessed, per rule 12). Handles the two shapes SH1's queries actually
// capture: the bare-form `command_name` wrapper (unwrapped to its single
// child) and the verb-form argument node directly.
func shellPathIsLiteral(n *sitter.Node) bool {
	switch n.Type() {
	case "word", "raw_string", "ansi_c_string":
		return true
	case "string":
		// Double-quoted: literal only when it carries no expansion child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			switch n.NamedChild(i).Type() {
			case "simple_expansion", "expansion", "command_substitution":
				return false
			}
		}
		return true
	case "command_name":
		if n.NamedChildCount() == 1 {
			return shellPathIsLiteral(n.NamedChild(0))
		}
		return false
	default:
		// concatenation, simple_expansion, expansion, command_substitution,
		// or anything else not explicitly recognized as literal.
		return false
	}
}

func init() {
	Register(&ShellParser{})
}
