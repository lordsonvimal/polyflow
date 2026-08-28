package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	bashsitter "github.com/smacker/go-tree-sitter/bash"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkShellInvocationEdges emits cross-file `calls` edges (meta via=exec)
// for shell script invocation: `bash x.sh`, `sh x.sh`, `. x.sh`,
// `source x.sh`, and bare `./x.sh`. Runs after every service's shell files
// have been parsed (internal/parser/shell.go always mints a per-file
// (script) scope node — see its doc comment), the same "re-parse
// independently" shape internal/linker/import_edges.go's Ruby
// require_relative pass already uses for cross-file resolution: the linking
// phase has the whole workspace's node set, which a single file's Parse call
// does not.
//
// Only the LITERAL invocation targets reach an edge or the
// shell_binary_unresolved ledger here. A dynamic (variable-built) target is
// deliberately skipped — internal/parser/shell.go's per-file pass already
// ledgers it as shell_invocation_dynamic during Parse (it is knowable
// per-file, with no need to wait for the whole workspace), and re-ledgering
// it here would double-count the same site.
//
// Command names resolving to a known workspace binary rather than an
// indexed shell script (e.g. `./bin/myservice migrate`) are explicitly
// descoped (SH1): they ledger as shell_binary_unresolved rather than being
// guessed at — resolving a binary to "the build output of some other
// service, if current" is a build-graph question this pass does not
// attempt.
func LinkShellInvocationEdges(nodes []graph.Node, serviceFiles map[string][]string) (newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	// (service, file) -> the file's synthetic (script) scope node ID —
	// always present per shell.go's guarantee, so this map doubles as the
	// "is this an indexed shell file" membership test the resolver needs.
	scriptNodeID := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction && n.Label == "(script)" {
			scriptNodeID[n.Service+"\x00"+n.File] = n.ID
		}
	}

	svcShellFiles := make(map[string]map[string]bool, len(serviceFiles))
	svcRoot := make(map[string]string, len(serviceFiles))
	for svc, files := range serviceFiles {
		s := make(map[string]bool, len(files))
		for _, f := range files {
			if isShellFile(f) {
				s[f] = true
			}
		}
		svcShellFiles[svc] = s
		svcRoot[svc] = commonDir(files)
	}

	// Iterate services in a stable order (bug-class rule 2): map iteration
	// order over serviceFiles must never reach output.
	svcNames := make([]string, 0, len(serviceFiles))
	for svc := range serviceFiles {
		svcNames = append(svcNames, svc)
	}
	sort.Strings(svcNames)

	seenEdge := make(map[string]bool)
	seenUnresolved := make(map[string]bool)

	for _, svc := range svcNames {
		for _, file := range serviceFiles[svc] {
			if !isShellFile(file) {
				continue
			}
			relFile := patterns.RelativizeToCwd(file)
			fromID := scriptNodeID[svc+"\x00"+relFile]
			if fromID == "" {
				continue // shell.go's own Parse pass never ran on this file
			}

			for _, inv := range parseShellInvocations(file) {
				if !inv.literal {
					continue // already ledgered per-file by shell.go
				}
				resolved := resolveShellInvocationPath(file, inv.target, svcRoot[svc], svcShellFiles[svc])
				if resolved == "" {
					key := svc + "\x00" + relFile + "\x00" + inv.target
					if !seenUnresolved[key] {
						seenUnresolved[key] = true
						unresolved = append(unresolved, graph.UnresolvedRef{
							Service: svc, File: relFile, Line: inv.line,
							Name: inv.target, Kind: "shell_binary_unresolved",
						})
					}
					continue
				}
				toID := scriptNodeID[svc+"\x00"+patterns.RelativizeToCwd(resolved)]
				if toID == "" {
					continue
				}
				eid := fmt.Sprintf("calls:%s->%s", fromID, toID)
				if seenEdge[eid] {
					continue
				}
				seenEdge[eid] = true
				newEdges = append(newEdges, graph.Edge{
					ID:         eid,
					From:       fromID,
					To:         toID,
					Type:       graph.EdgeTypeCalls,
					Confidence: graph.ConfidenceStatic,
					Meta:       map[string]string{"via": "exec"},
				})
			}
		}
	}
	return
}

// isShellFile reports whether file is a shell script or Bats-core test file
// — mirrors internal/patterns's own isShellFile (package-private there;
// linker cannot import parser or patterns' unexported helpers, so this is a
// small, deliberate duplication, the same shape isRubyFile/isJSFile already
// use independently per-linker-file in this package).
func isShellFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".sh" || ext == ".bash" || ext == ".bats"
}

// shellInvocation is one recognized invocation call site.
type shellInvocation struct {
	line    int
	target  string // raw, stripped text (only meaningful when literal)
	literal bool
}

// shellInvocationVerbQuery matches `bash x.sh` / `sh x.sh` / `. x.sh` /
// `source x.sh` — the `.` anchor between name: and argument: restricts
// @path to the FIRST argument (a command node's argument: field repeats
// once per positional argument; without the anchor every argument would
// match). Mirrors patterns/shell/invocation.yaml's shell_invocation_verb —
// kept as a literal Go string (not loaded from the YAML) because this pass
// re-parses independently of the patterns/matcher pipeline, the same
// decoupling internal/linker/import_edges.go's parseJSImportSources and
// parseRubyRequireRelative already use for their own cross-file queries.
const shellInvocationVerbQuery = `(command
  name: (command_name (word) @verb)
  .
  argument: (_) @path
  (#match? @verb "^(bash|sh|source|\\.)$"))`

// shellInvocationBareQuery matches bare `./x.sh` / `../x.sh` executable-bit
// invocation — the whole command_name node is the target. Mirrors
// patterns/shell/invocation.yaml's shell_invocation_bare.
const shellInvocationBareQuery = `(command
  name: (command_name) @path
  (#match? @path "^\\.\\.?/"))`

// parseShellInvocations walks file for both invocation shapes and returns
// each call site's line, literal-vs-dynamic classification, and (for
// literal sites) the stripped target text.
func parseShellInvocations(file string) []shellInvocation {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lang := bashsitter.GetLanguage()
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil
	}

	var out []shellInvocation
	for _, qs := range []string{shellInvocationVerbQuery, shellInvocationBareQuery} {
		q, err := compiledQuery(qs, lang)
		if err != nil {
			continue
		}
		cur := sitter.NewQueryCursor()
		cur.Exec(q, root)
		for {
			m, ok := cur.NextMatch()
			if !ok {
				break
			}
			m = cur.FilterPredicates(m, src)
			if len(m.Captures) == 0 {
				continue
			}
			var pathNode *sitter.Node
			for _, c := range m.Captures {
				if q.CaptureNameForId(c.Index) == "path" {
					pathNode = c.Node
				}
			}
			if pathNode == nil {
				continue
			}
			literal := shellNodeIsLiteral(pathNode)
			target := ""
			if literal {
				target = patterns.StripStringLiteral(pathNode.Content(src))
			}
			out = append(out, shellInvocation{
				line:    int(pathNode.StartPoint().Row) + 1,
				target:  target,
				literal: literal,
			})
		}
	}
	return out
}

// shellNodeIsLiteral mirrors internal/parser's shellPathIsLiteral — see that
// function's doc comment for the shape rationale. Duplicated rather than
// shared: linker cannot import parser (an existing, deliberate one-way
// dependency boundary in this codebase — the config-baseURL-prefix
// precedent), and this is a small, self-contained structural check.
func shellNodeIsLiteral(n *sitter.Node) bool {
	switch n.Type() {
	case "word", "raw_string", "ansi_c_string":
		return true
	case "string":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			switch n.NamedChild(i).Type() {
			case "simple_expansion", "expansion", "command_substitution":
				return false
			}
		}
		return true
	case "command_name":
		if n.NamedChildCount() == 1 {
			return shellNodeIsLiteral(n.NamedChild(0))
		}
		return false
	default:
		return false
	}
}

// resolveShellInvocationPath resolves a literal invocation target to an
// indexed shell file path: relative to the invoking file's own directory
// first, then relative to the service root — the same two-step order
// internal/linker/import_edges.go's JS/Ruby resolvers already use (see
// resolveJSImportPath / resolveRubyImportPath). Returns "" when neither
// resolves to an indexed file (the shell_binary_unresolved case: a
// workspace binary, or a script genuinely outside this workspace).
func resolveShellInvocationPath(importingFile, target, serviceRoot string, indexedFiles map[string]bool) string {
	if target == "" {
		return ""
	}
	if c := filepath.Clean(filepath.Join(filepath.Dir(importingFile), target)); indexedFiles[c] {
		return c
	}
	if serviceRoot != "" {
		trimmed := strings.TrimPrefix(target, "./")
		if c := filepath.Clean(filepath.Join(serviceRoot, trimmed)); indexedFiles[c] {
			return c
		}
	}
	return ""
}

// commonDir returns the deepest directory common to every path in files —
// this pass's stand-in for "service root" (no explicit service-root plumbing
// reaches this far into the linker today; every file arrives as a raw walk
// path). Returns "" for an empty input.
func commonDir(files []string) string {
	if len(files) == 0 {
		return ""
	}
	root := filepath.Dir(files[0])
	for _, f := range files[1:] {
		d := filepath.Dir(f)
		for root != "." && root != string(filepath.Separator) &&
			!strings.HasPrefix(d+string(filepath.Separator), root+string(filepath.Separator)) &&
			d != root {
			root = filepath.Dir(root)
		}
	}
	return root
}
