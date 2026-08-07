package linker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ResolveGoHTTPHosts is Tier J.2b — the Go analogue of Tier L's
// ResolveRubyHTTPHosts. A typed Go API client never posts to a literal URL; it
// posts to `c.baseURL + path`, and `baseURL` was handed to a constructor from a
// config struct field that was read from an environment variable:
//
//	config.go:234        MySycamoreAPIURL: os.Getenv("MYSYCAMORE_API_URL")
//	main.go:1242         c := NewMySycamoreClient(config.MySycamoreAPIURL, key)
//	mysycamore_client.go:24   return &MySycamoreClient{baseURL: strings.TrimSuffix(baseURL, "/")}
//	mysycamore_client.go:51   reqURL := fmt.Sprintf("%s/api/v1/users?email=%s", c.baseURL, …)
//
// Tier X.11 already reduces that call site to the path `*/api/v1/users`, but the
// `*` host carries no service identity, so the contract engine has to consider
// every handler in the workspace as a candidate. This pass stamps
// Meta["env_var"] on the client node, which ApplyHints (J.2c) then matches
// against a workspace `links: hint: MYSYCAMORE_API_URL` to set
// Meta["target_service"] — the allowlist Engine.Link already knows how to apply.
//
// It never invents a host. A client whose base cannot be traced to exactly one
// env var is left untouched, which is simply the pre-J.2 behaviour (no
// allowlist), not a new false positive. Scope is bounded and stated honestly:
//
//   - single-assignment name → env var mapping within one service,
//   - ≤ 2 constructor hops (config field → constructor param → receiver field),
//   - the request call site must sit in a method whose receiver type owns
//     exactly one env-derived, host-named field (hostishName — the same
//     base/url/host/endpoint test Tier L applies to Ruby host methods).
//
// Anything longer, or anything ambiguous at any step, resolves to nothing.
// Parsing is syntactic (go/parser, no type checking and no SSA): the pass runs
// inside the linker, after every service has been parsed, where the SSA programs
// the parser built are long gone and rebuilding them would cost more than the
// whole linking stage.
//
// Returns the mutated http_client nodes so the caller can re-persist them; the
// node metas are also mutated in place in the passed slice.
func ResolveGoHTTPHosts(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	// Cheap gate: only pay the extra parses for services that actually have a
	// Go http_client whose host went unresolved.
	svcNeeds := make(map[string]bool)
	for i := range nodes {
		if goDynamicHTTPNode(&nodes[i]) {
			svcNeeds[nodes[i].Service] = true
		}
	}
	if len(svcNeeds) == 0 {
		return nil
	}

	svcIndex := make(map[string]*goHostIndex, len(svcNeeds))
	for svc, files := range serviceFiles {
		if !svcNeeds[svc] {
			continue
		}
		svcIndex[svc] = buildGoHostIndex(files)
	}

	var changed []graph.Node
	for i := range nodes {
		n := &nodes[i]
		if !goDynamicHTTPNode(n) {
			continue
		}
		idx := svcIndex[n.Service]
		if idx == nil {
			continue
		}
		env := idx.envForNode(n.File, n.Line)
		if env == "" {
			continue
		}
		n.Meta = ensureMeta(n.Meta)
		n.Meta["env_var"] = env
		n.Meta["host_resolved_via"] = "go_env_field"
		changed = append(changed, *n)
	}
	return changed
}

// goDynamicHTTPNode reports whether n is a Go http_client whose host is not
// pinned: either the matcher could not resolve the URL at all (key_dynamic), or
// the URL/path was reconstructed with a wildcard host by Tier X.7/X.11
// (`*/api/v1/users`). A client with a fully literal absolute URL needs no
// env-var attribution — its host already names the target.
func goDynamicHTTPNode(n *graph.Node) bool {
	if n.Type != graph.NodeTypeHTTPClient || n.Language != "go" || n.File == "" {
		return false
	}
	if n.Meta["env_var"] != "" {
		return false // already attributed (idempotent re-run)
	}
	if n.Meta["key_dynamic"] == "true" {
		return true
	}
	return strings.HasPrefix(stripQuotes(n.Meta["url"]), "*") ||
		strings.HasPrefix(stripQuotes(n.Meta["path"]), "*")
}

// ── per-service index ───────────────────────────────────────────────────────

// goHostIndex holds one service's resolved name→env and type.field→env maps
// plus the parsed files needed to locate a node's enclosing method.
type goHostIndex struct {
	fset *token.FileSet
	// files maps an absolute filename to its parsed AST.
	files map[string]*ast.File
	// typeFieldEnv maps a struct type name → host-named field → env var. Only
	// unambiguous entries survive.
	typeFieldEnv map[string]map[string]string
}

// envConflict marks a name whose value came from two different env vars. Such a
// name resolves to nothing — an honest ambiguity is never guessed (#12).
const envConflict = "\x00conflict"

// buildGoHostIndex parses a service's Go files and resolves, in three bounded
// steps: (1) every name assigned from os.Getenv, (2) every struct field a
// constructor fills from one of its parameters, (3) the env var each of those
// constructors is actually called with.
func buildGoHostIndex(files []string) *goHostIndex {
	idx := &goHostIndex{
		fset:         token.NewFileSet(),
		files:        make(map[string]*ast.File),
		typeFieldEnv: make(map[string]map[string]string),
	}

	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	var parsed []*ast.File
	for _, f := range sorted {
		if !isGoFile(f) {
			continue
		}
		file, err := parser.ParseFile(idx.fset, f, nil, parser.SkipObjectResolution)
		if err != nil || file == nil {
			continue // a file that does not parse contributes nothing
		}
		idx.files[f] = file
		parsed = append(parsed, file)
	}
	if len(parsed) == 0 {
		return idx
	}

	nameEnv := collectNameEnv(parsed)
	ctorFields := collectCtorFields(parsed)
	ctorArgEnv := collectCtorArgEnvs(parsed, nameEnv)

	// (1) fields filled directly from an env expression, e.g. `&T{base: os.Getenv("X")}`
	// or `&T{base: cfg.APIURL}` where APIURL is env-derived.
	for _, cf := range ctorFields {
		// Only host-ish fields participate: a client struct routinely holds an
		// env-derived apiKey/token next to its baseURL, and attributing a request
		// to the *token's* env var would be a fabricated host (#12).
		if !hostishName(cf.fieldName) {
			continue
		}
		env := ""
		switch {
		case cf.directEnv != "":
			env = cf.directEnv
		case cf.paramIndex >= 0:
			// (2) filled from parameter i of ctor — resolved by what the callers pass.
			env = ctorArgEnv[argKey{fn: cf.fnName, idx: cf.paramIndex}]
		}
		if env == "" || env == envConflict {
			continue
		}
		fields := idx.typeFieldEnv[cf.typeName]
		if fields == nil {
			fields = make(map[string]string)
			idx.typeFieldEnv[cf.typeName] = fields
		}
		if prev, seen := fields[cf.fieldName]; seen && prev != env {
			fields[cf.fieldName] = envConflict
			continue
		}
		fields[cf.fieldName] = env
	}
	return idx
}

// envForNode resolves the env var behind the client call site at file:line, or
// "". The call site must live in a method whose receiver type has exactly one
// env-derived field referenced in that method body — or, when the body
// references none, exactly one env-derived field in total (the common
// `c.baseURL` client whose request building was hoisted into a helper).
func (idx *goHostIndex) envForNode(file string, line int) string {
	f := idx.files[file]
	if f == nil {
		return ""
	}
	fn := idx.enclosingFunc(f, line)
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	typeName := baseTypeName(fn.Recv.List[0].Type)
	fields := idx.typeFieldEnv[typeName]
	if len(fields) == 0 {
		return ""
	}

	referenced := make(map[string]bool)
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, isIdent := sel.X.(*ast.Ident); !isIdent {
			return true
		}
		if env, known := fields[sel.Sel.Name]; known && env != envConflict {
			referenced[env] = true
		}
		return true
	})
	if len(referenced) == 1 {
		for env := range referenced {
			return env
		}
	}
	if len(referenced) > 1 {
		return "" // two env-derived bases in one method: ambiguous, do not guess
	}

	// No env field named in this method (the URL came in as a parameter from a
	// sibling method). Fall back to the receiver type's sole env-derived field.
	sole := ""
	for _, env := range fields {
		if env == envConflict {
			return ""
		}
		if sole != "" && sole != env {
			return ""
		}
		sole = env
	}
	return sole
}

// enclosingFunc returns the innermost top-level FuncDecl whose body spans line.
func (idx *goHostIndex) enclosingFunc(f *ast.File, line int) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start := idx.fset.Position(fn.Pos()).Line
		end := idx.fset.Position(fn.End()).Line
		if line >= start && line <= end {
			return fn
		}
	}
	return nil
}

// ── step 1: name → env var ──────────────────────────────────────────────────

// collectNameEnv maps every bare name (struct field key, variable, constant) to
// the single env var it is assigned from, service-wide. A name assigned two
// different env vars maps to envConflict and resolves to nothing.
func collectNameEnv(files []*ast.File) map[string]string {
	out := make(map[string]string)
	record := func(name, env string) {
		if name == "" || env == "" {
			return
		}
		if prev, seen := out[name]; seen && prev != env {
			out[name] = envConflict
			return
		}
		out[name] = env
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok {
					record(key.Name, directEnvOf(node.Value))
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if i >= len(node.Rhs) {
						break
					}
					record(finalName(lhs), directEnvOf(node.Rhs[i]))
				}
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if i < len(node.Values) {
						record(name.Name, directEnvOf(node.Values[i]))
					}
				}
			}
			return true
		})
	}
	return out
}

// directEnvOf returns the env var an expression reads, or "". It recognises
// os.Getenv("X") / os.LookupEnv("X") and descends through the string plumbing
// that usually wraps them (`strings.TrimSuffix(os.Getenv("X"), "/")`,
// `os.Getenv("X") + "/v1"`, `getEnvOrDefault("X", …)` — any call whose sole
// string-literal argument is consumed by an env read below it). Two different
// env vars in one expression is an ambiguity and yields "".
func directEnvOf(expr ast.Expr) string {
	found := map[string]bool{}
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch node := e.(type) {
		case *ast.CallExpr:
			if env := getenvArg(node); env != "" {
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

// getenvArg returns the literal env-var name of an os.Getenv / os.LookupEnv
// call, or "". A non-literal argument (os.Getenv(key)) is not an env var name.
func getenvArg(call *ast.CallExpr) string {
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
	return stringLit(call.Args[0])
}

// ── step 2: constructor field ← parameter ───────────────────────────────────

// ctorField records one `&T{Field: …}` initialisation inside a function: either
// straight from an env expression (directEnv) or from the function's parameter
// at paramIndex.
type ctorField struct {
	typeName   string
	fieldName  string
	fnName     string
	paramIndex int // -1 when the value is not a parameter
	directEnv  string
}

// collectCtorFields finds every composite literal of a named struct type inside
// a function body and records how each of its string fields was filled.
func collectCtorFields(files []*ast.File) []ctorField {
	var out []ctorField
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			params := paramNames(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				typeName := baseTypeName(lit.Type)
				if typeName == "" {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					cf := ctorField{
						typeName:   typeName,
						fieldName:  key.Name,
						fnName:     fn.Name.Name,
						paramIndex: -1,
						directEnv:  directEnvOf(kv.Value),
					}
					if cf.directEnv == "" {
						cf.paramIndex = paramRefIndex(kv.Value, params)
						if cf.paramIndex < 0 {
							continue
						}
					}
					out = append(out, cf)
				}
				return true
			})
		}
	}
	return out
}

// paramNames returns a function's positional parameter names, expanding grouped
// declarations (`func f(baseURL, apiKey string)` → ["baseURL", "apiKey"]).
func paramNames(fn *ast.FuncDecl) []string {
	if fn.Type == nil || fn.Type.Params == nil {
		return nil
	}
	var out []string
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "") // unnamed parameter still occupies a position
			continue
		}
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
	}
	return out
}

// paramRefIndex returns the index of the parameter an expression reduces to, or
// -1. It looks through the string plumbing a constructor typically applies to
// its argument (`strings.TrimSuffix(baseURL, "/")`), and refuses an expression
// mentioning two different parameters.
func paramRefIndex(expr ast.Expr, params []string) int {
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

// ── step 3: constructor call sites → env var ────────────────────────────────

type argKey struct {
	fn  string
	idx int
}

// collectCtorArgEnvs resolves, for every (function, positional argument), the
// single env var every call site passes there. Call sites that disagree yield
// envConflict; call sites passing something not env-derived are ignored, so one
// wired-up caller is enough (the migration tool calls its clients from a single
// `main.go` branch, but a service with a test harness has more).
func collectCtorArgEnvs(files []*ast.File, nameEnv map[string]string) map[argKey]string {
	out := make(map[argKey]string)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fnName := finalName(call.Fun)
			if fnName == "" {
				return true
			}
			for i, arg := range call.Args {
				env := directEnvOf(arg)
				if env == "" {
					// `config.MySycamoreAPIURL` → the field's env var.
					if name := finalName(arg); name != "" {
						env = nameEnv[name]
					}
				}
				if env == "" || env == envConflict {
					continue
				}
				k := argKey{fn: fnName, idx: i}
				if prev, seen := out[k]; seen && prev != env {
					out[k] = envConflict
					continue
				}
				out[k] = env
			}
			return true
		})
	}
	return out
}

// ── small helpers ───────────────────────────────────────────────────────────

// finalName returns the trailing identifier of an ident or selector expression
// (`config.MySycamoreAPIURL` → MySycamoreAPIURL, `c.baseURL` → baseURL), or "".
func finalName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		return node.Sel.Name
	case *ast.StarExpr:
		return finalName(node.X)
	case *ast.ParenExpr:
		return finalName(node.X)
	}
	return ""
}

// baseTypeName returns the named type behind a type expression, stripping
// pointers, qualifiers and generic instantiations (`*pkg.Client[T]` → Client).
func baseTypeName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return baseTypeName(node.X)
	case *ast.SelectorExpr:
		return node.Sel.Name
	case *ast.ParenExpr:
		return baseTypeName(node.X)
	case *ast.IndexExpr:
		return baseTypeName(node.X)
	case *ast.IndexListExpr:
		return baseTypeName(node.X)
	}
	return ""
}

// stringLit returns the value of a plain string literal expression, or "".
func stringLit(expr ast.Expr) string {
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

// isGoFile reports whether path is a non-test Go source file. Test files are
// excluded: a test's fake wiring (`NewClient("http://localhost")`) is not the
// deployed configuration.
func isGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}
