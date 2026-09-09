package patternsynth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// CorpusFile is one source file of the sampled corpus, read once and reused by
// every candidate's validation run — the validator scores each candidate over
// the whole corpus, and re-reading N files per candidate is the difference
// between a synth run of seconds and one of minutes.
type CorpusFile struct {
	// Path is relative to the corpus root, slash-separated: it goes into hit
	// reports and into the corpus SHA, both of which must not move when the
	// corpus is checked out somewhere else.
	Path string
	Abs  string
	Src  []byte
}

// Site is one source site attributed to the target package.
//
// Not every site is a method call. A package's surface also shows up as a bare
// call that *passes* a package value across a function boundary
// (`registerUserRoutes(group, h)`) and as a declaration that *receives* one
// (`func registerUserRoutes(rg *gin.RouterGroup)`). Sampling only
// `receiver.Method(...)` is why those two shapes never reached the proposer at
// all — no cluster, so no candidate, so nothing for the gate to judge.
type Site struct {
	File     string
	Line     int
	Kind     string   // SiteCall | SiteBareCall | SiteDecl
	Method   string   // SiteCall: selector field ("Get"); SiteBareCall: callee ("registerUserRoutes")
	Recv     string   // operand identifier, e.g. "r" (receiver) or "chi" (package)
	RecvKind string   // RecvBinding | RecvPackage; empty for SiteDecl
	ArgShape []string // per-argument tree-sitter node type, in order
	// Args holds each argument's source text, used only for role inference
	// (a string literal that looks like a URL path becomes @path).
	Args []string

	// BindingArgs (SiteBareCall) are the argument positions holding a package
	// value. They are the site's whole claim to attribution: a bare call names
	// no package identifier, so the only evidence it belongs to the package is
	// that a package-typed value flows into it.
	BindingArgs []int

	// SiteDecl only.
	Name     string // declared function/method name
	DeclKind string // "function_declaration" | "method_declaration"
	TypeName string // the package type's own name, e.g. "RouterGroup"
	TypePtr  bool   // the parameter is a pointer to it
}

// Site kinds.
const (
	SiteCall     = "call"      // pkg.M(...) or binding.M(...)
	SiteBareCall = "bare_call" // f(..., binding, ...) — a package value crossing a call boundary
	SiteDecl     = "decl"      // func f(x pkg.T) — a declaration receiving one
)

// Receiver kinds. A binding is a value obtained from the package
// (`r := chi.NewRouter()`, or a parameter typed `chi.Router`); a package call
// is a call on the imported package identifier itself (`chi.NewRouter()`).
const (
	RecvBinding = "binding"
	RecvPackage = "package"
)

// Cluster is a set of call sites that share a callee shape: the same receiver
// kind and the same argument node-type sequence. Method names are deliberately
// *not* part of the key — grouping `Get`/`Post`/`Put`/`Delete` into one cluster
// is what lets the proposer emit a single pattern with a `#match?` alternation
// over the verbs it actually observed, instead of one near-duplicate pattern
// per verb.
type Cluster struct {
	Kind     string // SiteCall | SiteBareCall | SiteDecl
	RecvKind string
	ArgShape []string
	Methods  []string // sorted, distinct; callee names for SiteBareCall, empty for SiteDecl
	Sites    []Site

	// SiteBareCall: the argument positions holding a package value.
	BindingArgs []int

	// SiteDecl: the declaration shape the cluster is about. Parameter *index*
	// is deliberately not part of the identity — the proposed query matches any
	// parameter of the type, so keying on position would emit two identical
	// patterns for `f(rg *gin.RouterGroup)` and `g(h H, rg *gin.RouterGroup)`.
	DeclKind string
	TypeName string
	TypePtr  bool
	PkgAlias string // the local identifier the corpus binds the package to
}

// Key is the cluster's identity, and its sort key.
func (c Cluster) Key() string {
	switch c.Kind {
	case SiteDecl:
		return "decl(" + c.DeclKind + "," + c.typeExpr() + ")"
	case SiteBareCall:
		// One cluster for every bare call, whatever its signature. Keying on the
		// argument shape looks consistent with the call case and is not: a
		// receiver call's shape *is* its meaning, while a registrar's extra
		// parameters are incidental. Measured on gotify, keying on shape split
		// one concept across 13 near-identical patterns.
		return "bare()"
	default:
		// Unchanged for call sites: the key appears in reports and in the
		// disambiguation order, and moving it would move every existing name.
		return c.RecvKind + "(" + strings.Join(c.ArgShape, ",") + ")"
	}
}

// typeExpr renders the package type as it appears in source, which is also how
// the proposed query has to spell it.
func (c Cluster) typeExpr() string {
	alias := c.PkgAlias
	if alias == "" {
		alias = "pkg"
	}
	if c.TypePtr {
		return "*" + alias + "." + c.TypeName
	}
	return alias + "." + c.TypeName
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, "+")
}

// Sample walks the corpus, keeps the files attributable to pkg, and clusters
// the call sites in them by callee shape.
//
// Attribution is import-based, not name-based: a file that does not import pkg
// contributes nothing, which is what keeps a corpus's negative fixtures (a
// `cache.Get("key")` in a file that never imports the router) out of the
// sample without any special-casing.
func Sample(opts Options) ([]Cluster, []CorpusFile, error) {
	if opts.Language != "go" {
		return nil, nil, fmt.Errorf("sample: language %q not supported yet (only \"go\"); "+
			"a new language needs a sampler that knows how its imports bind names", opts.Language)
	}
	files, err := readCorpus(opts.Corpus, ".go")
	if err != nil {
		return nil, nil, err
	}

	byKey := map[string]*Cluster{}
	for _, f := range files {
		root, err := patterns.ParseTree("go", f.Src)
		if err != nil || root == nil {
			continue
		}
		aliases := goImportAliases(root, f.Src, opts.Package)
		if len(aliases) == 0 {
			continue
		}
		bindings := goBindings(root, f.Src, aliases)
		sites := goCallSites(root, f.Src, f.Path, aliases, bindings)
		sites = append(sites, goDeclSites(root, f.Src, f.Path, aliases)...)
		for _, s := range sites {
			c := byKey[clusterKey(s)]
			if c == nil {
				c = &Cluster{
					Kind: s.Kind, RecvKind: s.RecvKind, ArgShape: s.ArgShape,
					BindingArgs: s.BindingArgs, DeclKind: s.DeclKind,
					TypeName: s.TypeName, TypePtr: s.TypePtr, PkgAlias: s.Recv,
				}
				byKey[clusterKey(s)] = c
			}
			c.Sites = append(c.Sites, s)
		}
	}

	out := make([]Cluster, 0, len(byKey))
	for _, c := range byKey {
		seen := map[string]bool{}
		for _, s := range c.Sites {
			if s.Method != "" && !seen[s.Method] {
				seen[s.Method] = true
				c.Methods = append(c.Methods, s.Method)
			}
		}
		sort.Strings(c.Methods)
		sort.Slice(c.Sites, func(i, j int) bool {
			if c.Sites[i].File != c.Sites[j].File {
				return c.Sites[i].File < c.Sites[j].File
			}
			return c.Sites[i].Line < c.Sites[j].Line
		})
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, files, nil
}

func clusterKey(s Site) string {
	return Cluster{
		Kind: s.Kind, RecvKind: s.RecvKind, ArgShape: s.ArgShape,
		BindingArgs: s.BindingArgs, DeclKind: s.DeclKind,
		TypeName: s.TypeName, TypePtr: s.TypePtr, PkgAlias: s.Recv,
	}.Key()
}

// skipDirs are never walked: they hold code the corpus does not own, and a
// vendored copy of the target package would flood every cluster with the
// library's own internal call sites.
var skipDirs = map[string]bool{
	"vendor": true, "node_modules": true, ".git": true, ".polyflow": true,
}

func readCorpus(root, ext string) ([]CorpusFile, error) {
	var out []CorpusFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ext {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		out = append(out, CorpusFile{Path: filepath.ToSlash(rel), Abs: path, Src: src})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read corpus %s: %w", root, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("read corpus %s: no %s files", root, ext)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// CorpusSHA is a content hash of the sampled corpus, recorded in the generated
// file's header. A synthesized pattern nobody can trace back to the evidence
// that justified it is unreviewable, and synthesis makes producing those cheap.
func CorpusSHA(files []CorpusFile) string {
	h := sha256.New()
	for _, f := range files { // readCorpus sorted by path
		fmt.Fprintf(h, "%s\x00%x\x00", f.Path, sha256.Sum256(f.Src))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// ── Go sampling ──────────────────────────────────────────────────────────────

// goImportAliases returns the local identifiers this file binds to pkg (its
// last path element, or the explicit alias). A subpackage counts: importing
// `pkg/middleware` is still the package's surface.
func goImportAliases(root *sitter.Node, src []byte, pkg string) map[string]bool {
	out := map[string]bool{}
	walk(root, func(n *sitter.Node) bool {
		if n.Type() != "import_spec" {
			return true
		}
		pathNode := n.ChildByFieldName("path")
		if pathNode == nil {
			return true
		}
		path := strings.Trim(pathNode.Content(src), "\"`")
		if path != pkg && !strings.HasPrefix(path, pkg+"/") {
			return true
		}
		if name := n.ChildByFieldName("name"); name != nil {
			alias := name.Content(src)
			if alias != "_" && alias != "." {
				out[alias] = true
			}
			return true
		}
		out[goImportIdent(path)] = true
		return true
	})
	return out
}

// goImportIdent is the identifier an unaliased import binds: the last path
// element, or the one before it when that element is a major-version suffix
// (`.../chi/v5` binds `chi`).
func goImportIdent(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		return parts[len(parts)-2]
	}
	return last
}

// goBindings returns the identifiers in this file that hold a value obtained
// from the package: assigned from a package call (`r := chi.NewRouter()`) or
// declared with a package type (`func(r chi.Router)`, `var r chi.Router`).
//
// It also follows a binding through a call on an existing binding
// (`v1 := r.Group("/api")`), to a fixpoint: sub-routers are how most routes in
// a real corpus are registered, and without this the sampler sees only the
// routes hung directly off the root router. The cost is that an unrelated
// `v := r.Anything()` also becomes a binding — bounded, because a wrongly
// admitted binding only adds call sites to a cluster the gate still judges on
// its own merits.
func goBindings(root *sitter.Node, src []byte, aliases map[string]bool) map[string]bool {
	out := map[string]bool{}
	// Fixpoint: `a := pkg.New(); b := a.Group(); c := b.Group()` needs one pass
	// per link of the chain, and declaration order in the file is not the
	// order the chain builds in.
	for pass := 0; ; pass++ {
		before := len(out)
		collectGoBindings(root, src, aliases, out)
		if len(out) == before || pass > 8 {
			return out
		}
	}
}

func collectGoBindings(root *sitter.Node, src []byte, aliases, out map[string]bool) {
	walk(root, func(n *sitter.Node) bool {
		switch n.Type() {
		case "short_var_declaration", "assignment_statement":
			right := n.ChildByFieldName("right")
			left := n.ChildByFieldName("left")
			if right == nil || left == nil || !exprListCallsPackage(right, src, aliases, out) {
				return true
			}
			for i := 0; i < int(left.NamedChildCount()); i++ {
				if c := left.NamedChild(i); c.Type() == "identifier" {
					out[c.Content(src)] = true
				}
			}
		case "parameter_declaration", "var_spec", "const_spec", "field_declaration":
			typ := n.ChildByFieldName("type")
			if typ == nil || !qualifiedByPackage(typ, src, aliases) {
				return true
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c.Type() == "identifier" {
					out[c.Content(src)] = true
				}
			}
		}
		return true
	})
}

// qualifiedByPackage reports whether a type expression names a type from one of
// the package aliases, through any number of pointer/slice wrappers.
func qualifiedByPackage(typ *sitter.Node, src []byte, aliases map[string]bool) bool {
	_, _, _, ok := packageType(typ, src, aliases)
	return ok
}

// packageType resolves a type expression to the package type it names: the
// local alias the file bound the package to, the type's own name, and whether
// it sits behind a pointer. The alias travels with it because the proposed
// query has to spell the identifier the corpus uses — an aliased import would
// otherwise never match.
func packageType(typ *sitter.Node, src []byte, aliases map[string]bool) (alias, name string, ptr, ok bool) {
	switch typ.Type() {
	case "qualified_type":
		p, n := typ.ChildByFieldName("package"), typ.ChildByFieldName("name")
		if p == nil || n == nil || !aliases[p.Content(src)] {
			return "", "", false, false
		}
		return p.Content(src), n.Content(src), false, true
	case "pointer_type":
		if inner := typ.NamedChild(0); inner != nil {
			if a, n, _, ok := packageType(inner, src, aliases); ok {
				return a, n, true, true
			}
		}
	case "slice_type", "array_type":
		// Recognized for binding purposes (a `[]pkg.T` still holds package
		// values) but never proposed as a declaration shape.
		if inner := typ.NamedChild(0); inner != nil {
			if a, n, p, ok := packageType(inner, src, aliases); ok {
				return a, n, p, ok
			}
		}
	}
	return "", "", false, false
}

// goDeclSites collects declarations that receive a package-typed parameter.
//
// This is a declaration, not a call, and the distinction is the point: the
// binding between a function and the package value it is handed lives in its
// *signature*, and a sampler that only looks at call expressions cannot see it.
// The package-qualified type is what makes the site attributable — an
// unqualified parameter would match any helper in the corpus.
func goDeclSites(root *sitter.Node, src []byte, file string, aliases map[string]bool) []Site {
	var out []Site
	walk(root, func(n *sitter.Node) bool {
		kind := n.Type()
		if kind != "function_declaration" && kind != "method_declaration" {
			return true
		}
		nameNode := n.ChildByFieldName("name")
		params := n.ChildByFieldName("parameters")
		if nameNode == nil || params == nil {
			return true
		}
		// One site per distinct package type in the signature: two parameters
		// of the same type are one shape, and the proposed query matches any
		// parameter position, so emitting both would double-count the site.
		seen := map[string]bool{}
		for i := 0; i < int(params.NamedChildCount()); i++ {
			p := params.NamedChild(i)
			if p.Type() != "parameter_declaration" {
				continue
			}
			typ := p.ChildByFieldName("type")
			pname := p.ChildByFieldName("name")
			if typ == nil || pname == nil || typ.Type() == "slice_type" || typ.Type() == "array_type" {
				continue
			}
			alias, tname, ptr, ok := packageType(typ, src, aliases)
			if !ok || seen[tname] {
				continue
			}
			seen[tname] = true
			out = append(out, Site{
				File: file, Line: int(n.StartPoint().Row) + 1, Kind: SiteDecl,
				Name: nameNode.Content(src), DeclKind: kind, Recv: alias,
				TypeName: tname, TypePtr: ptr,
			})
		}
		return true
	})
	return out
}

// IndexCallables returns the names of callables declared anywhere in the
// corpus: the resolution table behind the gate's callable key. A capture whose
// text names one of these is a reference to something that can be called, which
// is a key a linker can join on — unlike a capture that names a value.
func IndexCallables(files []CorpusFile, opts Options) map[string]bool {
	out := map[string]bool{}
	if opts.Language != "go" {
		return out
	}
	for _, f := range files {
		root, err := patterns.ParseTree("go", f.Src)
		if err != nil || root == nil {
			continue
		}
		walk(root, func(n *sitter.Node) bool {
			switch n.Type() {
			case "function_declaration", "method_declaration":
				if name := n.ChildByFieldName("name"); name != nil {
					out[name.Content(f.Src)] = true
				}
			case "short_var_declaration", "var_spec", "const_spec":
				// `handler := func(...) {...}` declares a callable too, and a
				// pattern capturing `handler` joins on it just as well.
				right := n.ChildByFieldName("right")
				if right == nil {
					if right = n.ChildByFieldName("value"); right == nil {
						return true
					}
				}
				if !hasFuncLiteral(right) {
					return true
				}
				left := n.ChildByFieldName("left")
				if left == nil {
					left = n
				}
				for i := 0; i < int(left.NamedChildCount()); i++ {
					if c := left.NamedChild(i); c.Type() == "identifier" {
						out[c.Content(f.Src)] = true
					}
				}
			}
			return true
		})
	}
	return out
}

func hasFuncLiteral(exprs *sitter.Node) bool {
	for i := 0; i < int(exprs.NamedChildCount()); i++ {
		if exprs.NamedChild(i).Type() == "func_literal" {
			return true
		}
	}
	return exprs.Type() == "func_literal"
}

// exprListCallsPackage reports whether any expression in the list is a call on
// the package identifier itself or on a value already known to come from it.
func exprListCallsPackage(exprs *sitter.Node, src []byte, aliases, bindings map[string]bool) bool {
	for i := 0; i < int(exprs.NamedChildCount()); i++ {
		e := exprs.NamedChild(i)
		if e.Type() != "call_expression" {
			continue
		}
		fn := e.ChildByFieldName("function")
		if fn == nil || fn.Type() != "selector_expression" {
			continue
		}
		op := fn.ChildByFieldName("operand")
		if op == nil || op.Type() != "identifier" {
			continue
		}
		if name := op.Content(src); aliases[name] || bindings[name] {
			return true
		}
	}
	return false
}

// argShapeAllowed is the bounded set of argument node types a proposed query
// may name literally. Anything outside it becomes a `(_)` wildcard: a query
// naming a node type nobody reviewed is how a synthesized pattern ends up
// matching one corpus and nothing else.
var argShapeAllowed = map[string]bool{
	"interpreted_string_literal": true,
	"raw_string_literal":         true,
	"func_literal":               true,
	"identifier":                 true,
	"selector_expression":        true,
	"call_expression":            true,
	"composite_literal":          true,
	"int_literal":                true,
}

func goCallSites(root *sitter.Node, src []byte, file string, aliases, bindings map[string]bool) []Site {
	var out []Site
	walk(root, func(n *sitter.Node) bool {
		if n.Type() != "call_expression" {
			return true
		}
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return true
		}
		var s Site
		switch fn.Type() {
		case "selector_expression":
			op, field := fn.ChildByFieldName("operand"), fn.ChildByFieldName("field")
			if op == nil || field == nil || op.Type() != "identifier" {
				return true
			}
			recv := op.Content(src)
			kind := ""
			switch {
			case aliases[recv]:
				kind = RecvPackage
			case bindings[recv]:
				kind = RecvBinding
			default:
				return true
			}
			s = Site{Kind: SiteCall, Method: field.Content(src), Recv: recv, RecvKind: kind}
		case "identifier":
			// A bare call names no package identifier, so it is attributable
			// only through its arguments: `registerUserRoutes(group, h)` is a
			// package site because a package value flows into it. Without an
			// argument that carries one, this is just a local call.
			s = Site{Kind: SiteBareCall, Method: fn.Content(src)}
		default:
			return true
		}
		s.File, s.Line = file, int(n.StartPoint().Row)+1

		if args := n.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				a := args.NamedChild(i)
				t := a.Type()
				if !argShapeAllowed[t] {
					t = "_"
				}
				s.ArgShape = append(s.ArgShape, t)
				s.Args = append(s.Args, a.Content(src))
				if a.Type() == "identifier" && bindings[a.Content(src)] {
					s.BindingArgs = append(s.BindingArgs, i)
				}
			}
		}
		if s.Kind == SiteBareCall && len(s.BindingArgs) == 0 {
			return true
		}
		out = append(out, s)
		return true
	})
	return out
}

// walk visits n and its named descendants; fn returns false to prune.
func walk(n *sitter.Node, fn func(*sitter.Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		walk(n.NamedChild(i), fn)
	}
}
