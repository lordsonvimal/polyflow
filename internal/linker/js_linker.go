package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// NewJSLinker creates a JSLinker.
func NewJSLinker() *JSLinker { return &JSLinker{} }

// JSLinker resolves cross-file JS/TS edges:
//  1. Component declaration linking: redirects `renders` edges from JSX usage
//     nodes (component type, same-file proxy) to the actual function declaration
//     node in the imported file, then removes the proxy nodes.
//  2. Import-aware call linking: resolves `obj.method()` calls through import
//     statements and emits `calls` edges to the declaration node.
type JSLinker struct{}

// LinkJS runs both JS linking passes and returns new edges plus the set of
// proxy node IDs that should be removed from the graph.
func (l *JSLinker) LinkJS(nodes []graph.Node, edges []graph.Edge, serviceFiles map[string][]string) (newEdges []graph.Edge, removeNodeIDs map[string]bool) {
	removeNodeIDs = make(map[string]bool)

	// Index nodes by ID for lookup.
	nodeByID := make(map[string]*graph.Node, len(nodes))
	for i := range nodes {
		nodeByID[nodes[i].ID] = &nodes[i]
	}

	// Index function/method declaration nodes by service+label (prefer same service).
	// key: service + "\x00" + label → nodeID
	funcByServiceLabel := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			key := n.Service + "\x00" + n.Label
			if _, exists := funcByServiceLabel[key]; !exists {
				funcByServiceLabel[key] = n.ID
			}
		}
	}

	// --- Pass 1: redirect renders edges from component proxy → declaration ---
	// Build map: component usage nodeID → enclosing function nodeID (from existing renders edges)
	// We want: enclosingFunc -renders-> declaration (not proxy).
	edgeFromByTo := make(map[string]string) // usageNodeID → callerID
	for _, e := range edges {
		if e.Type == graph.EdgeTypeRenders {
			edgeFromByTo[e.To] = e.From
		}
	}

	// For each component usage node, resolve to a function declaration in the same service.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeComponent {
			continue
		}
		// templ components are declarations emitted by the templ parser (with
		// datastar action/bind children attached), not JSX usage proxies —
		// there is no JS function declaration to redirect to; keep them.
		if n.Language == "templ" {
			continue
		}
		// Skip framework components (Show, For, Match etc. — no user declaration).
		if isFrameworkComponent(n.Label) {
			removeNodeIDs[n.ID] = true
			continue
		}

		// Find the declaration node: same label, function type, same service.
		declID, ok := funcByServiceLabel[n.Service+"\x00"+n.Label]
		if !ok {
			// No matching declaration — could be an external library component; drop proxy.
			removeNodeIDs[n.ID] = true
			continue
		}
		if declID == n.ID {
			continue
		}

		// Redirect the renders edge: from enclosingFunc → declID instead of → proxy.
		callerID, hasCaller := edgeFromByTo[n.ID]
		if hasCaller && callerID != declID {
			newEdges = append(newEdges, graph.Edge{
				ID:   fmt.Sprintf("renders:%s->%s", callerID, declID),
				From: callerID,
				To:   declID,
				Type: graph.EdgeTypeRenders,
			})
		}
		// Mark the proxy node for removal.
		removeNodeIDs[n.ID] = true
	}

	// --- Pass 2: import-aware call linking ---
	// Build per-service file list for import resolution.
	for svcName, files := range serviceFiles {
		// Build label→nodeID index for this service's function nodes.
		svcFuncByLabel := make(map[string]string)
		for i := range nodes {
			n := &nodes[i]
			if n.Service != svcName {
				continue
			}
			if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
				if _, exists := svcFuncByLabel[n.Label]; !exists {
					svcFuncByLabel[n.Label] = n.ID
				}
			}
		}

		// Build file→nodeID index for enclosing function lookup.
		// key: file + "\x00" + label → nodeID
		funcByFileAndLabel := make(map[string]string)
		for i := range nodes {
			n := &nodes[i]
			if n.Service != svcName {
				continue
			}
			if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
				funcByFileAndLabel[n.File+"\x00"+n.Label] = n.ID
			}
		}

		// Module-scope variables are call targets too: Solid signal accessors
		// and setters (const [x, setX] = createSignal(...)) are variables that
		// hold functions, so uiStore.setX(...) must resolve to the variable
		// node. Function declarations take precedence on label collisions.
		svcVarByLabel := make(map[string]string)
		for i := range nodes {
			n := &nodes[i]
			if n.Service != svcName {
				continue
			}
			if n.Type == graph.NodeTypeVariable && n.Meta["scope"] == "module" {
				if _, exists := svcVarByLabel[n.Label]; !exists {
					svcVarByLabel[n.Label] = n.ID
				}
			}
		}

		// Build per-file line→enclosing function index.
		funcLinesByFile := make(map[string][]lineNode)
		for i := range nodes {
			n := &nodes[i]
			if n.Service != svcName {
				continue
			}
			if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
				end := 0
				if v, ok := n.Meta["end_line"]; ok {
					fmt.Sscanf(v, "%d", &end)
				}
				funcLinesByFile[n.File] = append(funcLinesByFile[n.File], lineNode{n.Line, end, n.ID})
			}
		}

		seen := make(map[string]bool)
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			importEdges := resolveImportCalls(file, svcFuncByLabel, svcVarByLabel, funcLinesByFile)
			for _, e := range importEdges {
				if !seen[e.ID] {
					seen[e.ID] = true
					newEdges = append(newEdges, e)
				}
			}
		}
	}

	return newEdges, removeNodeIDs
}

// isFrameworkComponent returns true for SolidJS/React built-in components that
// are not user-defined functions and should not be kept as nodes.
func isFrameworkComponent(label string) bool {
	switch label {
	case "Show", "For", "Switch", "Match", "Suspense", "ErrorBoundary",
		"Portal", "Dynamic", "Index", "Await", "Transition":
		return true
	}
	return false
}

func isJSFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" || ext == ".mjs"
}

// resolveImportCalls parses file for import declarations and member-expression
// call sites, then emits calls edges from the enclosing function to the
// resolved target function in the imported file's node set.
type lineNode struct {
	line int
	end  int // declaration body end (0 = unknown, treated as open-ended)
	id   string
}

func resolveImportCalls(file string, svcFuncByLabel, svcVarByLabel map[string]string, funcLinesByFile map[string][]lineNode) []graph.Edge {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}

	lang := grammarLangForFile(file)
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return nil
	}

	// --- Extract import bindings: localName → set of exported names from that module ---
	// We care about two forms:
	//   import { addFilter } from "../stores/ui"       → addFilter → addFilter
	//   import { uiStore } from "../stores/ui"         → uiStore.X → X (member call)
	// We collect: importedName (as used in this file) → []exportedName (in the source file)
	// For named imports { x } → x maps 1:1.
	// For default imports X → we skip (default exports are harder to resolve).
	// For namespace imports * as X → X.method → method.
	type importBinding struct {
		localName    string // name used in this file
		exportedName string // name exported from source (empty = same as local)
		isNamespace  bool   // import * as X
	}
	var bindings []importBinding

	importQuery := `
(import_statement
  (import_clause
    (named_imports
      (import_specifier
        name: (identifier) @exported
        alias: (identifier) @local))))`
	importQuerySameAlias := `
(import_statement
  (import_clause
    (named_imports
      (import_specifier
        name: (identifier) @name))))`
	nsQuery := `
(import_statement
  (import_clause
    (namespace_import
      (identifier) @ns)))`

	for _, qstr := range []string{importQuery} {
		q, err := sitter.NewQuery([]byte(qstr), lang)
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
			caps := make(map[string]string)
			for _, c := range m.Captures {
				caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
			}
			if exp, ok1 := caps["exported"]; ok1 {
				if loc, ok2 := caps["local"]; ok2 {
					bindings = append(bindings, importBinding{localName: loc, exportedName: exp})
				}
			}
		}
	}
	for _, qstr := range []string{importQuerySameAlias} {
		q, err := sitter.NewQuery([]byte(qstr), lang)
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
			caps := make(map[string]string)
			for _, c := range m.Captures {
				caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
			}
			if name, ok := caps["name"]; ok {
				bindings = append(bindings, importBinding{localName: name, exportedName: name})
			}
		}
	}
	{
		q, err := sitter.NewQuery([]byte(nsQuery), lang)
		if err == nil {
			cur := sitter.NewQueryCursor()
			cur.Exec(q, root)
			for {
				m, ok := cur.NextMatch()
				if !ok {
					break
				}
				for _, c := range m.Captures {
					ns := c.Node.Content(src)
					bindings = append(bindings, importBinding{localName: ns, isNamespace: true})
				}
			}
		}
	}

	if len(bindings) == 0 {
		return nil
	}

	// Build lookup: localName → exportedName (for plain calls)
	// and nsName → true (for member calls obj.method())
	plainImport := make(map[string]string) // localName → exportedName
	nsImports := make(map[string]bool)     // namespace import names
	for _, b := range bindings {
		if b.isNamespace {
			nsImports[b.localName] = true
		} else {
			expName := b.exportedName
			if expName == "" {
				expName = b.localName
			}
			plainImport[b.localName] = expName
		}
	}

	// --- Detect call sites ---
	// Plain call: localName() where localName is a named import → resolve to exportedName
	plainCallQuery := `
(call_expression
  function: (identifier) @callee)`

	// Member call: obj.method() where obj is a named or namespace import
	memberCallQuery := `
(call_expression
  function: (member_expression
    object: (identifier) @obj
    property: (property_identifier) @method))`

	type callSite struct {
		targetLabel string // resolved function name in the service
		line        int
	}
	var callSites []callSite

	{
		q, err := sitter.NewQuery([]byte(plainCallQuery), lang)
		if err == nil {
			cur := sitter.NewQueryCursor()
			cur.Exec(q, root)
			for {
				m, ok := cur.NextMatch()
				if !ok {
					break
				}
				for _, c := range m.Captures {
					local := c.Node.Content(src)
					if exported, ok := plainImport[local]; ok {
						callSites = append(callSites, callSite{
							targetLabel: exported,
							line:        int(c.Node.StartPoint().Row) + 1,
						})
					}
				}
			}
		}
	}
	{
		q, err := sitter.NewQuery([]byte(memberCallQuery), lang)
		if err == nil {
			cur := sitter.NewQueryCursor()
			cur.Exec(q, root)
			for {
				m, ok := cur.NextMatch()
				if !ok {
					break
				}
				caps := make(map[string]string)
				var minLine int
				for _, c := range m.Captures {
					caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
					row := int(c.Node.StartPoint().Row) + 1
					if minLine == 0 || row < minLine {
						minLine = row
					}
				}
				obj, method := caps["obj"], caps["method"]
				// Resolve: obj is a named import (e.g. uiStore exported as-is) or namespace.
				_, isNS := nsImports[obj]
				if isNS {
					callSites = append(callSites, callSite{targetLabel: method, line: minLine})
				} else if exported, ok := plainImport[obj]; ok {
					// obj itself was imported as a value (e.g. store object); method is the member.
					// We try to find a function named method in the service (it's declared in the
					// source file as a standalone function, not as a method on the object).
					_ = exported
					callSites = append(callSites, callSite{targetLabel: method, line: minLine})
				}
			}
		}
	}

	// JSX event prop references: onClick={importedFn} — not a call_expression,
	// so the plain call query misses them. Only compiles against TSX grammar.
	jsxEventPropQuery := `
(jsx_attribute
  (property_identifier) @prop
  (#match? @prop "^on[A-Z]")
  (jsx_expression
    (identifier) @callee))`
	{
		q, err := sitter.NewQuery([]byte(jsxEventPropQuery), lang)
		if err == nil {
			cur := sitter.NewQueryCursor()
			cur.Exec(q, root)
			for {
				m, ok := cur.NextMatch()
				if !ok {
					break
				}
				caps := make(map[string]string)
				var minLine int
				for _, c := range m.Captures {
					caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
					row := int(c.Node.StartPoint().Row) + 1
					if minLine == 0 || row < minLine {
						minLine = row
					}
				}
				local := caps["callee"]
				if exported, ok := plainImport[local]; ok {
					callSites = append(callSites, callSite{targetLabel: exported, line: minLine})
				}
			}
		}
	}

	if len(callSites) == 0 {
		return nil
	}

	funcs := funcLinesByFile[file]

	var edges []graph.Edge
	for _, cs := range callSites {
		targetID, ok := svcFuncByLabel[cs.targetLabel]
		if !ok {
			// Fall back to module-scope variables (signal accessors/setters).
			if targetID, ok = svcVarByLabel[cs.targetLabel]; !ok {
				continue
			}
		}
		// Find the innermost function containing the call site. Functions
		// without a recorded end line are treated as open-ended.
		var best *lineNode
		for j := range funcs {
			f := &funcs[j]
			if f.line > cs.line {
				continue
			}
			if f.end > 0 && cs.line > f.end {
				continue
			}
			if best == nil || f.line > best.line {
				best = f
			}
		}
		if best == nil || best.id == targetID {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("calls:%s->%s", best.id, targetID),
			From: best.id,
			To:   targetID,
			Type: graph.EdgeTypeCalls,
		})
	}
	return edges
}

func grammarLangForFile(file string) *sitter.Language {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".tsx", ".jsx":
		return tsxsitter.GetLanguage()
	case ".ts", ".js", ".mjs":
		return tssitter.GetLanguage()
	default:
		return tssitter.GetLanguage()
	}
}
