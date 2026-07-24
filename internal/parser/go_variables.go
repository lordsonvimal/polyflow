package parser

import (
	"encoding/json"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// varExtractResult is the output of extractVariables. It carries nodes and
// edges plus the type maps that the implements sweep in AnalyzeService needs.
type varExtractResult struct {
	Nodes        []graph.Node
	Edges        []graph.Edge
	StructIDs    map[*types.Named]string // named struct → node ID
	InterfaceIDs map[*types.Named]string // named interface → node ID
}

// extractVariables walks the already-built SSA packages and emits the
// variable-tracking and type-relationship layers of the graph:
//
//   - variable nodes for package-level vars/consts and closure-captured
//     locals (the variables whose mutation matters beyond one function —
//     purely-local variables are deliberately NOT nodes)
//   - struct nodes with their field list in meta
//   - interface nodes (Tier I.1) with their method list in meta
//   - writes/reads edges from functions to tracked globals
//   - captures edges from the enclosing function to variables its closures
//     capture (Go closures capture by reference)
//   - flows_to edges when a tracked variable is passed at a call site,
//     annotated by-ref vs by-value
//   - uses_type edges from functions whose signatures mention a struct
//   - inherits edges for struct embedding (Anonymous fields, via=embedding)
//   - instantiates edges from constructors to the types they allocate
//
// All edges carry static confidence — they come from the type checker, not
// heuristics.
func extractVariables(
	pkgs []*packages.Package,
	ssaPkgs []*ssa.Package,
	dir, service string,
	fset *token.FileSet,
	inService map[*ssa.Function]bool,
	resolveFunc func(*ssa.Function) (string, bool),
) varExtractResult {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	// relPath converts an absolute SSA position to the workspace-relative
	// form used by tree-sitter node IDs and File fields.
	relPath := func(abs string) string {
		if rel, err := filepath.Rel(canonicalPath(cwd), canonicalPath(abs)); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return abs
	}
	inDir := func(pos token.Position) bool {
		return pos.IsValid() && pos.Filename != "" &&
			strings.HasPrefix(canonicalPath(pos.Filename), canonicalPath(dir))
	}

	var nodes []graph.Node
	var edges []graph.Edge
	nodeSeen := map[string]bool{}
	edgeSeen := map[string]bool{}

	addNode := func(n graph.Node) {
		if !nodeSeen[n.ID] {
			nodeSeen[n.ID] = true
			nodes = append(nodes, n)
		}
	}
	addEdge := func(typ graph.EdgeType, from, to string, meta map[string]string) {
		id := fmt.Sprintf("semantic:%s:%s->%s", typ, from, to)
		if edgeSeen[id] {
			return
		}
		edgeSeen[id] = true
		edges = append(edges, graph.Edge{
			ID: id, From: from, To: to, Type: typ,
			Confidence: graph.ConfidenceStatic, Meta: meta,
		})
	}

	// globalIDs maps each package-level *ssa.Global to its node ID so the
	// instruction walk below can attribute loads/stores to it.
	globalIDs := map[*ssa.Global]string{}
	// qualifiedNameIDs maps "<pkgPath>.<Name>" to the node ID for cross-package
	// global and const lookups (B.2). Pointer identity in globalIDs can fail
	// when the SSA program was built from test-variant packages whose dependency
	// resolution uses a different *ssa.Package instance than the one in ssaPkgs.
	// Non-test-file entries win over test-file entries (per-spec shadowing rule).
	qualifiedNameIDs := map[string]string{}
	// structIDs maps a named struct type to its node ID for uses_type and
	// inherits/instantiates edges.
	structIDs := map[*types.Named]string{}
	// interfaceIDs maps a named interface type to its node ID (Tier I.1).
	interfaceIDs := map[*types.Named]string{}

	// Local JSON-marshaling types for type metadata.
	type fieldInfo struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Tag  string `json:"tag,omitempty"`
	}
	type methodInfo struct {
		Name      string `json:"name"`
		Signature string `json:"signature"`
	}

	// pendingEmbeds collects struct types with anonymous (embedded) fields for
	// the inherits-edge pass that runs after all nodes are emitted.
	type embedEntry struct {
		structID string
		st       *types.Struct
	}
	var pendingEmbeds []embedEntry

	// ── Package members: globals, consts, struct types, interface types ──────
	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		for _, m := range p.Members {
			pos := fset.Position(m.Pos())
			if !inDir(pos) {
				continue
			}
			file := relPath(pos.Filename)
			switch v := m.(type) {
			case *ssa.Global:
				// A Global's SSA type is a pointer to the variable's type.
				dataType := v.Type().String()
				if ptr, ok := v.Type().(*types.Pointer); ok {
					dataType = ptr.Elem().String()
				}
				id := fmt.Sprintf("%s:%s:variable:%s:%d", service, file, v.Name(), pos.Line)
				globalIDs[v] = id
				addNode(graph.Node{
					ID: id, Type: graph.NodeTypeVariable, Label: v.Name(),
					Service: service, File: file, Line: pos.Line, Language: "go",
					Meta: map[string]string{
						"data_type": dataType, "kind": "var",
						"scope": "package", "mutable": "true",
					},
				})
				// B.2: register by qualified name for cross-package fallback.
				// Non-test-file entry wins when both variants are in ssaPkgs.
				if v.Package() != nil && v.Package().Pkg != nil {
					qk := v.Package().Pkg.Path() + "." + v.Name()
					if _, exists := qualifiedNameIDs[qk]; !exists {
						qualifiedNameIDs[qk] = id
					} else if !strings.HasSuffix(pos.Filename, "_test.go") {
						qualifiedNameIDs[qk] = id // prod file wins over test file
					}
				}
			case *ssa.NamedConst:
				id := fmt.Sprintf("%s:%s:variable:%s:%d", service, file, v.Name(), pos.Line)
				addNode(graph.Node{
					ID: id, Type: graph.NodeTypeVariable, Label: v.Name(),
					Service: service, File: file, Line: pos.Line, Language: "go",
					Meta: map[string]string{
						"data_type": v.Value.Type().String(), "kind": "const",
						"scope": "package", "mutable": "false",
					},
				})
				// B.2: register const by qualified name for const-ref resolution.
				if v.Package() != nil && v.Package().Pkg != nil {
					qk := v.Package().Pkg.Path() + "." + v.Name()
					if _, exists := qualifiedNameIDs[qk]; !exists {
						qualifiedNameIDs[qk] = id
					} else if !strings.HasSuffix(pos.Filename, "_test.go") {
						qualifiedNameIDs[qk] = id
					}
				}
			case *ssa.Type:
				named, ok := v.Type().(*types.Named)
				if !ok {
					continue
				}
				switch under := named.Underlying().(type) {
				case *types.Struct:
					fields := make([]fieldInfo, 0, under.NumFields())
					for i := 0; i < under.NumFields(); i++ {
						f := under.Field(i)
						fields = append(fields, fieldInfo{Name: f.Name(), Type: f.Type().String(), Tag: under.Tag(i)})
					}
					fieldsJSON, _ := json.Marshal(fields)
					id := fmt.Sprintf("%s:%s:struct:%s:%d", service, file, v.Name(), pos.Line)
					structIDs[named] = id
					addNode(graph.Node{
						ID: id, Type: graph.NodeTypeStruct, Label: v.Name(),
						Service: service, File: file, Line: pos.Line, Language: "go",
						Meta: map[string]string{
							"fields":      string(fieldsJSON),
							"field_count": fmt.Sprintf("%d", under.NumFields()),
						},
					})
					if under.NumFields() > 0 {
						pendingEmbeds = append(pendingEmbeds, embedEntry{id, under})
					}
				case *types.Interface:
					if under.NumMethods() == 0 {
						continue // empty interfaces (any/interface{}) produce no edges
					}
					methods := make([]methodInfo, 0, under.NumMethods())
					for i := 0; i < under.NumMethods(); i++ {
						m := under.Method(i)
						methods = append(methods, methodInfo{Name: m.Name(), Signature: m.Type().String()})
					}
					methodsJSON, _ := json.Marshal(methods)
					id := fmt.Sprintf("%s:%s:interface:%s:%d", service, file, v.Name(), pos.Line)
					interfaceIDs[named] = id
					addNode(graph.Node{
						ID: id, Type: graph.NodeTypeInterface, Label: v.Name(),
						Service: service, File: file, Line: pos.Line, Language: "go",
						Meta: map[string]string{"methods": string(methodsJSON)},
					})
				}
			}
		}
	}

	// ── Inherits edges: struct embedding (anonymous fields) ──────────────────
	for _, e := range pendingEmbeds {
		for i := 0; i < e.st.NumFields(); i++ {
			f := e.st.Field(i)
			if !f.Anonymous() {
				continue
			}
			ft := f.Type()
			// Dereference pointer embedding (e.g., struct{ *Base }).
			if pt, ok := ft.(*types.Pointer); ok {
				ft = pt.Elem()
			}
			named, ok := ft.(*types.Named)
			if !ok {
				continue
			}
			// Only emit when the embedded type is an in-service struct or interface.
			var targetID string
			if id, ok := structIDs[named]; ok {
				targetID = id
			} else if id, ok := interfaceIDs[named]; ok {
				targetID = id
			}
			if targetID == "" {
				continue
			}
			addEdge(graph.EdgeTypeInherits, e.structID, targetID, map[string]string{"via": "embedding"})
		}
	}

	// rootGlobal peels FieldAddr/IndexAddr chains to find the Global (if any)
	// a store/load address ultimately refers to.
	var rootGlobal func(v ssa.Value) *ssa.Global
	rootGlobal = func(v ssa.Value) *ssa.Global {
		switch a := v.(type) {
		case *ssa.Global:
			return a
		case *ssa.FieldAddr:
			return rootGlobal(a.X)
		case *ssa.IndexAddr:
			return rootGlobal(a.X)
		case *ssa.UnOp:
			if a.Op == token.MUL {
				return rootGlobal(a.X)
			}
		}
		return nil
	}

	// byRef reports whether a value of type t is shared when passed — the
	// callee can observe or cause mutations through it.
	byRef := func(t types.Type) bool {
		switch t.Underlying().(type) {
		case *types.Pointer, *types.Slice, *types.Map, *types.Chan:
			return true
		}
		return false
	}

	// enclosing resolves fn (or, for anonymous closures, its outermost named
	// parent) to a graph node ID.
	enclosing := func(fn *ssa.Function) (string, bool) {
		for fn.Parent() != nil {
			fn = fn.Parent()
		}
		return resolveFunc(fn)
	}

	// instCounts accumulates instantiation counts across all SSA functions
	// that resolve to the same enclosing node ID (closures → parent).
	// Key: fnID + "->" + typeID.
	instCounts := map[string]int{}

	// ── Instruction walk: reads, writes, captures, flows_to, uses_type ──────
	for fn := range inService {
		fnID, fnResolved := enclosing(fn)

		// Closure captures: every free variable of fn was declared in a
		// parent function; surface it as a captured-variable node.
		if fnResolved && len(fn.FreeVars) > 0 {
			for _, fv := range fn.FreeVars {
				pos := fset.Position(fv.Pos())
				if !inDir(pos) {
					continue
				}
				file := relPath(pos.Filename)
				dataType := fv.Type().String()
				if ptr, ok := fv.Type().(*types.Pointer); ok {
					dataType = ptr.Elem().String()
				}
				id := fmt.Sprintf("%s:%s:variable:%s:%d", service, file, fv.Name(), pos.Line)
				addNode(graph.Node{
					ID: id, Type: graph.NodeTypeVariable, Label: fv.Name(),
					Service: service, File: file, Line: pos.Line, Language: "go",
					Meta: map[string]string{
						"data_type": dataType, "kind": "var",
						"scope": "captured", "mutable": "true",
					},
				})
				// Go closures always capture by reference.
				addEdge(graph.EdgeTypeCaptures, fnID, id, map[string]string{"by": "ref"})
			}
		}

		// uses_type: signature parameters/results referencing a known struct.
		if fnResolved {
			sig := fn.Signature
			checkType := func(t types.Type) {
				named, ok := t.(*types.Named)
				if !ok {
					if ptr, isPtr := t.(*types.Pointer); isPtr {
						named, ok = ptr.Elem().(*types.Named)
					}
				}
				if !ok || named == nil {
					return
				}
				if sid, tracked := structIDs[named]; tracked {
					addEdge(graph.EdgeTypeUsesType, fnID, sid, nil)
				}
			}
			for i := 0; i < sig.Params().Len(); i++ {
				checkType(sig.Params().At(i).Type())
			}
			for i := 0; i < sig.Results().Len(); i++ {
				checkType(sig.Results().At(i).Type())
			}
		}

		// funcInstCounts collects instantiations in this SSA function body.
		var funcInstCounts map[string]int

		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch in := instr.(type) {
				case *ssa.Store:
					if g := rootGlobal(in.Addr); g != nil {
						if id, ok := resolveGlobalID(g, globalIDs, qualifiedNameIDs); ok && fnResolved {
							addEdge(graph.EdgeTypeWrites, fnID, id, map[string]string{"op": "assign"})
						}
					}
					// Mutation through a captured variable's address.
					if fv, ok := in.Addr.(*ssa.FreeVar); ok && fnResolved {
						pos := fset.Position(fv.Pos())
						if inDir(pos) {
							id := fmt.Sprintf("%s:%s:variable:%s:%d", service, relPath(pos.Filename), fv.Name(), pos.Line)
							if nodeSeen[id] {
								addEdge(graph.EdgeTypeWrites, fnID, id, map[string]string{"op": "assign", "via": "closure"})
							}
						}
					}
				case *ssa.MapUpdate:
					if g := rootGlobal(in.Map); g != nil {
						if id, ok := resolveGlobalID(g, globalIDs, qualifiedNameIDs); ok && fnResolved {
							addEdge(graph.EdgeTypeWrites, fnID, id, map[string]string{"op": "map_update"})
						}
					}
				case *ssa.UnOp:
					if in.Op != token.MUL {
						continue
					}
					if g, ok := in.X.(*ssa.Global); ok {
						if id, tracked := resolveGlobalID(g, globalIDs, qualifiedNameIDs); tracked && fnResolved {
							addEdge(graph.EdgeTypeReads, fnID, id, nil)
						}
					}
				case ssa.CallInstruction:
					common := in.Common()
					callee, _ := common.Value.(*ssa.Function)
					if callee == nil || !inService[callee] {
						continue
					}
					calleeID, ok := resolveFunc(callee)
					if !ok {
						continue
					}
					for _, arg := range common.Args {
						var g *ssa.Global
						var argType types.Type
						switch a := arg.(type) {
						case *ssa.Global:
							// Address of a global passed directly — by ref.
							g, argType = a, a.Type()
						case *ssa.UnOp:
							if a.Op == token.MUL {
								if root, isG := a.X.(*ssa.Global); isG {
									g, argType = root, a.Type()
								}
							}
						}
						if g == nil {
							continue
						}
						id, tracked := resolveGlobalID(g, globalIDs, qualifiedNameIDs)
						if !tracked {
							continue
						}
						mode := "value"
						if byRef(argType) {
							mode = "ref"
						}
						addEdge(graph.EdgeTypeFlowsTo, id, calleeID, map[string]string{
							"mode": mode, "data_type": argType.String(),
						})
					}
				case *ssa.Alloc:
					// Track struct instantiations: &T{} or local T{} both produce
					// *ssa.Alloc with Type() = *T. Attribute to the enclosing function.
					if !fnResolved {
						continue
					}
					pt, ok := in.Type().(*types.Pointer)
					if !ok {
						continue
					}
					named, ok := pt.Elem().(*types.Named)
					if !ok {
						continue
					}
					if typeID, ok := structIDs[named]; ok {
						if funcInstCounts == nil {
							funcInstCounts = map[string]int{}
						}
						funcInstCounts[typeID]++
					}
				}
			}
		}

		// Accumulate this function's instantiation counts into instCounts.
		if fnResolved {
			for typeID, count := range funcInstCounts {
				instCounts[fnID+"->"+typeID] += count
			}
		}
	}

	// ── Instantiates edges (emitted once per (fn, type) pair with count) ────
	for key, count := range instCounts {
		sep := strings.Index(key, "->")
		fnID, typeID := key[:sep], key[sep+2:]
		addEdge(graph.EdgeTypeInstantiates, fnID, typeID, map[string]string{
			"count": strconv.Itoa(count),
		})
	}

	// ── B.2: cross-package const references via typed AST ───────────────────
	// Go constants are compile-time-folded and invisible to SSA instructions,
	// so we resolve them using the type-checker's Uses map (available from
	// packages.LoadAllSyntax, the same load mode the SSA pass already uses).
	// We build a per-file function-range index from inService to find the
	// enclosing function node for each const-reference identifier.
	//
	// Implementation note: the spec calls for the tree-sitter layer here, but
	// MatchToGraph has no access to the in-service const node set (those are
	// emitted by this SSA pass). Using the typed AST achieves the same result
	// with higher precision (type-checker distinguishes *types.Const from
	// *types.Var and *types.Func) and avoids new cross-layer coupling.
	// Recorded as a deviation in the phase outcome note.
	type fnRng struct {
		start  int
		end    int // 0 = unknown
		nodeID string
	}
	fnsByFile := map[string][]fnRng{}
	for fn := range inService {
		if fn.Parent() != nil {
			continue // skip closures; attribute to named parent
		}
		pos := fset.Position(fn.Pos())
		if !inDir(pos) {
			continue
		}
		nodeID, ok := resolveFunc(fn)
		if !ok {
			continue
		}
		endLine := 0
		if syn := fn.Syntax(); syn != nil {
			endPos := fset.Position(syn.End())
			if endPos.IsValid() {
				endLine = endPos.Line
			}
		}
		cf := canonicalPath(pos.Filename)
		fnsByFile[cf] = append(fnsByFile[cf], fnRng{pos.Line, endLine, nodeID})
	}
	for cf := range fnsByFile {
		sort.Slice(fnsByFile[cf], func(i, j int) bool {
			return fnsByFile[cf][i].start < fnsByFile[cf][j].start
		})
	}
	enclosingFnAt := func(filename string, line int) (string, bool) {
		cf := canonicalPath(filename)
		var best *fnRng
		for i := range fnsByFile[cf] {
			f := &fnsByFile[cf][i]
			if f.start > line {
				continue
			}
			if f.end > 0 && line > f.end {
				continue
			}
			if best == nil || f.start > best.start {
				best = f
			}
		}
		if best == nil {
			return "", false
		}
		return best.nodeID, true
	}

	for _, p := range pkgs {
		if p == nil || p.TypesInfo == nil || p.Types == nil {
			continue
		}
		for ident, obj := range p.TypesInfo.Uses {
			c, ok := obj.(*types.Const)
			if !ok {
				continue
			}
			if c.Pkg() == nil || c.Pkg() == p.Types {
				continue // same-package const: SSA handles it (or no edge needed)
			}
			qk := c.Pkg().Path() + "." + c.Name()
			constNodeID, ok := qualifiedNameIDs[qk]
			if !ok {
				continue // not an in-service const
			}
			pos := fset.Position(ident.Pos())
			if !inDir(pos) {
				continue
			}
			fnID, ok := enclosingFnAt(pos.Filename, pos.Line)
			if !ok {
				continue
			}
			addEdge(graph.EdgeTypeReads, fnID, constNodeID, nil)
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return varExtractResult{
		Nodes:        nodes,
		Edges:        edges,
		StructIDs:    structIDs,
		InterfaceIDs: interfaceIDs,
	}
}

// resolveGlobalID returns the node ID for a package-level global, using pointer
// identity first (same-package, fast path) then falling back to the service-wide
// qualifiedNameIDs map (cross-package, needed when SSA builds dependency packages
// from a different variant than the one stored in globalIDs — B.2).
func resolveGlobalID(g *ssa.Global, globalIDs map[*ssa.Global]string, qualifiedNameIDs map[string]string) (string, bool) {
	if id, ok := globalIDs[g]; ok {
		return id, true
	}
	if g.Package() == nil || g.Package().Pkg == nil {
		return "", false
	}
	qk := g.Package().Pkg.Path() + "." + g.Name()
	id, ok := qualifiedNameIDs[qk]
	return id, ok
}
