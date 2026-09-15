package factpipe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_go_http_hosts.go is the "go_http_hosts" hub provider (see hub.go) —
// the Tier FX migration of internal/linker/go_http_hosts.go's retired
// ResolveGoHTTPHosts (Tier J.2b).
//
// This is NOT a Tier VG (valuegraph) migration, even though the plan
// document originally parked it there (docs/js-value-graph-pilot-plan.md's
// VG.7). Reading the retired Go first: buildGoHostIndex is not a bounded
// per-expression traversal ("what string can THIS expression be, walking
// outward") — it is a whole-service LEAST-FIXPOINT dataflow analysis:
// collect every name->env fact across all files, collect every constructor
// field assignment, then run a bounded-hop fixpoint (resolveCtorArgEnvs /
// propagateArgEnvs, up to maxHostHops rounds) propagating env-var identity
// through constructor call chains across function boundaries, with
// ambiguity as a STICKY poison value. That shape — global facts to a
// fixpoint, then a lookup — is exactly what internal/datalog's stratified
// bottom-up evaluation + count aggregate already does natively, and a much
// better fit than valuegraph.Engine's bounded single-query model (which
// has no concept of "propagate to a fixpoint across every function in the
// service"). See rules/go/go_http_hosts.dl for the fixpoint expressed as
// four explicit hop layers (the datalog engine has no arithmetic builtin,
// so maxHostHops=4 is unrolled rather than computed).
//
// This hub does ONLY the part that is unavoidably real Go: parsing service
// files with go/parser and walking the AST for syntactic facts no `.dl`
// join can produce (an identifier's assignment site, a composite literal's
// field, a call's positional arguments, a method's receiver type). Every
// relational step after that — name resolution, hop-bounded forwarding,
// conflict-as-ambiguity, the two-branch per-node grading (referenced field
// vs sole-field fallback) — is `.dl` rules, not Go.
func init() { RegisterHub("go_http_hosts", goHTTPHostsHub) }

const (
	ghNameEnvOccPred  = "go_name_env_occ"      // (Name, Env)
	ghCtorFieldDirect = "go_ctor_field_direct" // (FnName, TypeName, FieldName, Env)
	ghCtorFieldParam  = "go_ctor_field_param"  // (FnName, TypeName, FieldName, ParamIdx)
	ghCallArgDirect   = "go_call_arg_direct"   // (CalleeFn, ArgIdx, Env)
	ghCallArgName     = "go_call_arg_name"     // (CalleeFn, ArgIdx, Name)
	ghForwardParam    = "go_forward_param"     // (CallerFn, ParamIdx, CalleeFn, ArgIdx)
	ghClientRecv      = "go_client_recv"       // (NodeID, TypeName)
	ghClientRef       = "go_client_ref"        // (NodeID, FieldName)
)

func goHTTPHostsHub(nodes []graph.Node, files []string, _ string) []Fact {
	svcNeeds := false
	for i := range nodes {
		if ghDynamicHTTPNode(&nodes[i]) {
			svcNeeds = true
			break
		}
	}
	if !svcNeeds {
		return nil
	}

	fset := token.NewFileSet()
	byFile := make(map[string]*ast.File)
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	var parsed []*ast.File
	for _, f := range sorted {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil || file == nil {
			continue
		}
		byFile[f] = file
		parsed = append(parsed, file)
	}
	if len(parsed) == 0 {
		return nil
	}

	var out []Fact
	add := func(pred string, args ...Atom) {
		out = append(out, Fact{Pred: pred, Args: args, Origin: Origin{Kind: OriginPrimitive, Pattern: pred}})
	}

	// ── name -> env occurrences, service-wide ──────────────────────────────
	record := func(name, env string) {
		if name == "" || env == "" {
			return
		}
		add(ghNameEnvOccPred, Str(name), Str(env))
	}
	for _, f := range parsed {
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok {
					record(key.Name, ghDirectEnvOf(node.Value))
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if i >= len(node.Rhs) {
						break
					}
					record(ghFinalName(lhs), ghDirectEnvOf(node.Rhs[i]))
				}
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if i < len(node.Values) {
						record(name.Name, ghDirectEnvOf(node.Values[i]))
					}
				}
			}
			return true
		})
	}

	// ── constructor composite-literal fields + call-site args + forwarding ─
	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			fnName := fn.Name.Name
			params := ghParamNames(fn)

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					typeName := ghBaseTypeName(node.Type)
					if typeName == "" {
						return true
					}
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok || !ghHostishName(key.Name) {
							continue
						}
						if env := ghDirectEnvOf(kv.Value); env != "" {
							add(ghCtorFieldDirect, Str(fnName), Str(typeName), Str(key.Name), Str(env))
							continue
						}
						if idx := ghParamRefIndex(kv.Value, params); idx >= 0 {
							add(ghCtorFieldParam, Str(fnName), Str(typeName), Str(key.Name), Int(int64(idx)))
						}
					}
				case *ast.CallExpr:
					callee := ghFinalName(node.Fun)
					if callee == "" {
						return true
					}
					for i, arg := range node.Args {
						if env := ghDirectEnvOf(arg); env != "" {
							add(ghCallArgDirect, Str(callee), Int(int64(i)), Str(env))
						} else if name := ghFinalName(arg); name != "" {
							add(ghCallArgName, Str(callee), Int(int64(i)), Str(name))
						}
						if pi := ghParamRefIndex(arg, params); pi >= 0 && ghHostishName(params[pi]) {
							add(ghForwardParam, Str(fnName), Int(int64(pi)), Str(callee), Int(int64(i)))
						}
					}
				}
				return true
			})
		}
	}

	// ── per-node receiver + referenced host-ish fields ──────────────────────
	for i := range nodes {
		n := &nodes[i]
		if !ghDynamicHTTPNode(n) {
			continue
		}
		f := byFile[n.File]
		if f == nil {
			continue
		}
		fn := ghEnclosingFunc(fset, f, n.Line)
		if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		typeName := ghBaseTypeName(fn.Recv.List[0].Type)
		if typeName == "" {
			continue
		}
		add(ghClientRecv, Node(n.ID), Str(typeName))
		seen := map[string]bool{}
		ast.Inspect(fn, func(m ast.Node) bool {
			sel, ok := m.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, isIdent := sel.X.(*ast.Ident); !isIdent {
				return true
			}
			if ghHostishName(sel.Sel.Name) && !seen[sel.Sel.Name] {
				seen[sel.Sel.Name] = true
				add(ghClientRef, Node(n.ID), Str(sel.Sel.Name))
			}
			return true
		})
	}

	return out
}

// ghDynamicHTTPNode mirrors the retired goDynamicHTTPNode gate exactly.
func ghDynamicHTTPNode(n *graph.Node) bool {
	if n.Type != graph.NodeTypeHTTPClient || n.Language != "go" || n.File == "" {
		return false
	}
	if n.Meta["env_var"] != "" {
		return false
	}
	if n.Meta["key_dynamic"] == "true" {
		return true
	}
	return strings.HasPrefix(ghStripQuotes(n.Meta["url"]), "*") ||
		strings.HasPrefix(ghStripQuotes(n.Meta["path"]), "*")
}

func ghStripQuotes(s string) string {
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	return s
}

// ghHostishName mirrors internal/linker's shared hostishName (defined in
// ruby_http_hosts.go, which this migration does not touch).
func ghHostishName(name string) bool {
	l := strings.ToLower(name)
	for _, tok := range []string{"url", "uri", "host", "endpoint", "app", "base"} {
		if strings.Contains(l, tok) {
			return true
		}
	}
	return false
}

// ghDirectEnvOf mirrors the retired directEnvOf: recognises os.Getenv("X") /
// os.LookupEnv("X") and descends through surrounding string plumbing. Two
// different env vars in one expression is an ambiguity and yields "".
func ghDirectEnvOf(expr ast.Expr) string {
	found := map[string]bool{}
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch node := e.(type) {
		case *ast.CallExpr:
			if env := ghGetenvArg(node); env != "" {
				found[env] = true
				return
			}
			for _, arg := range node.Args {
				walk(arg)
			}
		case *ast.BinaryExpr:
			walk(node.X)
			walk(node.Y)
		case *ast.ParenExpr:
			walk(node.X)
		}
	}
	walk(expr)
	if len(found) != 1 {
		return ""
	}
	for env := range found {
		return env
	}
	return ""
}

func ghGetenvArg(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return ""
	}
	if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
		return ""
	}
	if len(call.Args) == 0 {
		return ""
	}
	return ghStringLit(call.Args[0])
}

func ghStringLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return v
}

func ghFinalName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		return node.Sel.Name
	case *ast.StarExpr:
		return ghFinalName(node.X)
	case *ast.ParenExpr:
		return ghFinalName(node.X)
	}
	return ""
}

func ghBaseTypeName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return ghBaseTypeName(node.X)
	case *ast.SelectorExpr:
		return node.Sel.Name
	case *ast.ParenExpr:
		return ghBaseTypeName(node.X)
	case *ast.IndexExpr:
		return ghBaseTypeName(node.X)
	case *ast.IndexListExpr:
		return ghBaseTypeName(node.X)
	}
	return ""
}

func ghParamNames(fn *ast.FuncDecl) []string {
	if fn.Type == nil || fn.Type.Params == nil {
		return nil
	}
	var out []string
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "")
			continue
		}
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
	}
	return out
}

// ghParamRefIndex mirrors the retired paramRefIndex: the index of the
// parameter an expression reduces to, or -1, looking through string
// plumbing and refusing an expression naming two different parameters.
func ghParamRefIndex(expr ast.Expr, params []string) int {
	found := map[int]bool{}
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch node := e.(type) {
		case *ast.Ident:
			for i, p := range params {
				if p != "" && p == node.Name {
					found[i] = true
				}
			}
		case *ast.CallExpr:
			for _, arg := range node.Args {
				walk(arg)
			}
		case *ast.BinaryExpr:
			walk(node.X)
			walk(node.Y)
		case *ast.ParenExpr:
			walk(node.X)
		}
	}
	walk(expr)
	if len(found) != 1 {
		return -1
	}
	for i := range found {
		return i
	}
	return -1
}

// ghEnclosingFunc mirrors the retired enclosingFunc: the innermost
// top-level FuncDecl whose body spans line.
func ghEnclosingFunc(fset *token.FileSet, f *ast.File, line int) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		end := fset.Position(fn.End()).Line
		if line >= start && line <= end {
			return fn
		}
	}
	return nil
}
