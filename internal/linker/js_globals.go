package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkJSGlobals resolves unresolved call_ref references and inline event
// handler strings against the per-service global symbol table — the fallback
// layer after import-aware resolution (LinkJS). See docs/goal-completion-plan.md
// Phase L.W1 for the contract.
//
// Resolution order (pinned): imports first (existing behavior, enforced via
// importedNames); globals second. A name explained by an import is never
// re-resolved against globals even if a global with the same name exists.
//
// Collision rule: when the same global name is defined in ≥2 files, emit
// candidate edges to ALL definitions (via=global_ambiguous) plus one
// global_collision ledger entry — never silently pick one (rule 1, fan-out).
//
// Determinism (rule 2): global table keys iterated sorted; collision
// candidates ordered by file path.
//
// Returns:
//   - newEdges: resolved calls edges (confidence=inferred)
//   - globallyResolved: file+"\x00"+name pairs whose call_ref is now explained
//     (caller suppresses them from allUnresolved in the indexer)
//   - unresolvedOut: global_collision ledger entries to surface
func LinkJSGlobals(
	nodes []graph.Node,
	allUnresolved []graph.UnresolvedRef,
	importedNames map[string]bool,
	svcFiles map[string][]string,
) (newEdges []graph.Edge, globallyResolved map[string]bool, unresolvedOut []graph.UnresolvedRef) {
	globallyResolved = make(map[string]bool)

	// Build per-service global symbol table:
	//   svc → globalName → []globalEntry (sorted by file for determinism)
	type globalEntry struct {
		nodeID string
		file   string
	}
	svcGlobals := make(map[string]map[string][]globalEntry)
	for _, n := range nodes {
		globalName := n.Meta["global_symbol"]
		if globalName == "" {
			continue
		}
		if svcGlobals[n.Service] == nil {
			svcGlobals[n.Service] = make(map[string][]globalEntry)
		}
		svcGlobals[n.Service][globalName] = append(
			svcGlobals[n.Service][globalName],
			globalEntry{nodeID: n.ID, file: n.File},
		)
	}
	// Sort each entry list by (file, nodeID) for determinism (rule 2).
	for _, tbl := range svcGlobals {
		for name := range tbl {
			sort.Slice(tbl[name], func(i, j int) bool {
				if tbl[name][i].file != tbl[name][j].file {
					return tbl[name][i].file < tbl[name][j].file
				}
				return tbl[name][i].nodeID < tbl[name][j].nodeID
			})
		}
	}

	seenEdge := make(map[string]bool)
	seenCollision := make(map[string]bool)

	emitGlobalEdges := func(fromID, callee, svc string) {
		tbl, ok := svcGlobals[svc]
		if !ok {
			return
		}
		entries, ok := tbl[callee]
		if !ok {
			return
		}
		via := "global"
		if len(entries) > 1 {
			via = "global_ambiguous"
			key := svc + "\x00" + callee
			if !seenCollision[key] {
				seenCollision[key] = true
				unresolvedOut = append(unresolvedOut, graph.UnresolvedRef{
					Service: svc, Name: callee, Kind: "global_collision",
				})
			}
		}
		for _, e := range entries {
			if fromID == e.nodeID {
				continue
			}
			id := fmt.Sprintf("calls:%s->%s", fromID, e.nodeID)
			if seenEdge[id] {
				continue
			}
			seenEdge[id] = true
			newEdges = append(newEdges, graph.Edge{
				ID:         id,
				From:       fromID,
				To:         e.nodeID,
				Type:       graph.EdgeTypeCalls,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"via": via},
			})
		}
	}

	// Per-service attribution structures (same logic as LinkJS.resolveImportCalls).
	// We build funcLinesByFile and funcByFileAndLabel per service to reconstruct
	// the from-node for call_ref resolution.
	type attrInfo struct {
		funcLinesByFile    map[string][]lineNode
		funcByFileAndLabel map[string]string
	}
	svcAttr := make(map[string]*attrInfo)
	for svcName := range svcFiles {
		a := &attrInfo{
			funcLinesByFile:    make(map[string][]lineNode),
			funcByFileAndLabel: make(map[string]string),
		}
		for _, n := range nodes {
			if n.Service != svcName {
				continue
			}
			if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
				continue
			}
			key := n.File + "\x00" + n.Label
			if _, exists := a.funcByFileAndLabel[key]; !exists {
				a.funcByFileAndLabel[key] = n.ID
			}
			if n.Label == "(module)" {
				continue
			}
			end := 0
			if v, ok := n.Meta["end_line"]; ok {
				fmt.Sscanf(v, "%d", &end)
			}
			a.funcLinesByFile[n.File] = append(a.funcLinesByFile[n.File], lineNode{n.Line, end, n.ID})
		}
		svcAttr[svcName] = a
	}

	// resolveFrom mirrors LinkJS's logic: innermost containing function → module node.
	resolveFrom := func(svc, file string, lineNo int) string {
		a, ok := svcAttr[svc]
		if !ok {
			return ""
		}
		funcs := a.funcLinesByFile[file]
		var best *lineNode
		for j := range funcs {
			f := &funcs[j]
			if f.line > lineNo {
				continue
			}
			if f.end > 0 && lineNo > f.end {
				continue
			}
			if best == nil || f.line > best.line {
				best = f
			}
		}
		if best != nil {
			return best.id
		}
		return a.funcByFileAndLabel[file+"\x00(module)"]
	}

	// 1. Resolve unresolved call_ref entries against the global table.
	// Imports-first: skip any name that imports already explain.
	// Iterate sorted by (service, file, name) for determinism.
	type uKey struct{ svc, file, name string; line int }
	var sortedRefs []uKey
	for _, u := range allUnresolved {
		if u.Kind != "call_ref" {
			continue
		}
		if importedNames[u.File+"\x00"+u.Name] {
			continue // import explains this name — no global fallback
		}
		tbl := svcGlobals[u.Service]
		if tbl == nil {
			continue
		}
		if _, ok := tbl[u.Name]; !ok {
			continue
		}
		sortedRefs = append(sortedRefs, uKey{u.Service, u.File, u.Name, u.Line})
	}
	sort.Slice(sortedRefs, func(i, j int) bool {
		a, b := sortedRefs[i], sortedRefs[j]
		if a.svc != b.svc {
			return a.svc < b.svc
		}
		if a.file != b.file {
			return a.file < b.file
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.line < b.line
	})

	for _, ref := range sortedRefs {
		fromID := resolveFrom(ref.svc, ref.file, ref.line)
		if fromID == "" {
			continue
		}
		emitGlobalEdges(fromID, ref.name, ref.svc)
		globallyResolved[ref.file+"\x00"+ref.name] = true
	}

	// 2. Resolve inline handler strings on dom_target nodes.
	// The from-node is the dom_target node itself.
	// Sort dom_target nodes by ID for determinism.
	var domTargets []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget && n.Meta["handler"] != "" {
			domTargets = append(domTargets, n)
		}
	}
	sort.Slice(domTargets, func(i, j int) bool { return domTargets[i].ID < domTargets[j].ID })

	for _, n := range domTargets {
		callee := extractHandlerCallee(n.Meta["handler"])
		if callee == "" {
			continue
		}
		tbl := svcGlobals[n.Service]
		if tbl == nil {
			continue
		}
		if _, ok := tbl[callee]; !ok {
			continue
		}
		emitGlobalEdges(n.ID, callee, n.Service)
	}

	// Sort output for determinism (rule 2).
	sort.Slice(newEdges, func(i, j int) bool { return newEdges[i].ID < newEdges[j].ID })
	sort.Slice(unresolvedOut, func(i, j int) bool {
		if unresolvedOut[i].Service != unresolvedOut[j].Service {
			return unresolvedOut[i].Service < unresolvedOut[j].Service
		}
		return unresolvedOut[i].Name < unresolvedOut[j].Name
	})
	return newEdges, globallyResolved, unresolvedOut
}

// extractHandlerCallee extracts the leading identifier from an inline event
// handler string so it can be resolved in the global symbol table.
//
//	"save()"          → "save"
//	"App.submit(this)"→ "App"
//	"save"            → "save"
//	""                → ""
func extractHandlerCallee(handler string) string {
	handler = strings.TrimSpace(handler)
	if len(handler) == 0 {
		return ""
	}
	// First character must be a valid JS identifier start (letter, _, $).
	c0 := handler[0]
	if !(c0 >= 'a' && c0 <= 'z' || c0 >= 'A' && c0 <= 'Z' || c0 == '_' || c0 == '$') {
		return ""
	}
	end := 1
	for end < len(handler) {
		c := handler[end]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' {
			end++
		} else {
			break
		}
	}
	return handler[:end]
}
