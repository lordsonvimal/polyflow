package patternsynth

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Proposal is one candidate pattern plus the cluster that justified it. The
// cluster travels with the pattern because every downstream step needs it: the
// gate reports which sites a rejection cost, and the header records the
// evidence.
type Proposal struct {
	Pattern patterns.Pattern
	Cluster Cluster
	Notes   []string
}

// Proposer turns a cluster of call sites into candidate patterns.
//
// This is the seam the plan leaves open: an LLM proposer and the template
// proposer below are interchangeable here, and the gate does not care which
// produced a candidate. Nothing downstream may trust a proposal — a proposer
// is allowed to emit a query that does not compile, matches nothing, or
// matches the whole corpus, because the validator rejects all three.
type Proposer interface {
	Propose(c Cluster, opts Options) ([]Proposal, error)
	Name() string
}

// TemplateProposer builds a query directly from the cluster's observed shape:
// the receiver, a `#match?` alternation over the method names seen, and one
// query child per argument position. It invents nothing — every literal in the
// output query came from a call site in the corpus — which makes it the
// baseline any smarter proposer has to beat.
type TemplateProposer struct{}

func (TemplateProposer) Name() string { return "template" }

// httpVerbs are the method names that make a route registration recognizable
// as one. This is the proposer's only piece of domain knowledge, and it only
// affects *naming and capture names* — never which sites match.
var httpVerbs = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true,
}

// pathLiteral matches a string literal whose content looks like a URL path.
var pathLiteral = regexp.MustCompile(`^["` + "`" + `]/`)

func (p TemplateProposer) Propose(c Cluster, opts Options) ([]Proposal, error) {
	if len(c.Sites) == 0 {
		return nil, nil
	}
	switch c.Kind {
	case SiteDecl:
		return p.proposeDecl(c, opts)
	case SiteBareCall:
		return p.proposeBareCall(c, opts)
	}
	if len(c.Methods) == 0 {
		return nil, nil
	}
	// A cluster deliberately drops the method name so `Get`/`Post`/`Put` merge
	// into one verb alternation. That merge is only right while the methods
	// agree on whether their arguments name a callable: `r.Use(logging)` and
	// `r.SetLimit(maxInFlight)` share a shape, and lumping them into one
	// `^(SetLimit|Use)$` pattern buries a real key under a value. Split the
	// alternation, and the queries differ by their method predicate, so the two
	// patterns can never match the same site.
	groups := splitByCallableArg(c, opts.Callables)
	if len(groups) > 1 {
		var out []Proposal
		for _, g := range groups {
			props, err := p.Propose(g, opts)
			if err != nil {
				return nil, err
			}
			out = append(out, props...)
		}
		return out, nil
	}

	slug := opts.Slug()
	name, kind := p.name(c, slug)

	captures, queryArgs, notes := p.arguments(c, kind)

	var b strings.Builder
	fmt.Fprintf(&b, "(call_expression\n")
	fmt.Fprintf(&b, "  function: (selector_expression\n")
	fmt.Fprintf(&b, "    operand: (identifier) @receiver\n")
	if c.RecvKind == RecvPackage {
		// A package-qualified call is only that package's call when the
		// operand is the imported identifier; without this the query would
		// match any `x.Foo(...)` in any file.
		fmt.Fprintf(&b, "    (#eq? @receiver %q)\n", c.Sites[0].Recv)
	}
	fmt.Fprintf(&b, "    field: (field_identifier) @method\n")
	fmt.Fprintf(&b, "    %s)\n", methodPredicate(c.Methods))
	if len(queryArgs) > 0 {
		fmt.Fprintf(&b, "  arguments: (argument_list\n")
		for i, a := range queryArgs {
			end := ""
			if i == len(queryArgs)-1 {
				end = ")"
			}
			fmt.Fprintf(&b, "    %s%s\n", a, end)
		}
	}
	query := strings.TrimRight(b.String(), "\n") + ")"

	pat := patterns.Pattern{Name: name, Query: query, Captures: captures}
	return []Proposal{{Pattern: pat, Cluster: c, Notes: notes}}, nil
}

// splitByCallableArg partitions a call cluster's methods into those whose sites
// pass a callable and those whose sites do not, returning one sub-cluster per
// non-empty group — or the cluster unchanged when they all agree, which is the
// common case and the one that keeps existing output identical.
//
// The split is over corpus evidence, not over meaning: it asks whether the
// argument at a site names something the corpus declares as callable, the same
// question the gate asks. Nothing here knows what a middleware is.
func splitByCallableArg(c Cluster, callables map[string]bool) []Cluster {
	if len(c.Methods) < 2 || len(callables) == 0 {
		return []Cluster{c}
	}
	byMethod := map[string][]Site{}
	for _, s := range c.Sites {
		byMethod[s.Method] = append(byMethod[s.Method], s)
	}
	withKey, withoutKey := Cluster{}, Cluster{}
	for _, m := range c.Methods {
		sites := byMethod[m]
		n := 0
		for _, s := range sites {
			if siteHasCallableArg(s, callables) {
				n++
			}
		}
		target := &withoutKey
		if len(sites) > 0 && float64(n)/float64(len(sites)) >= callableKeyFloor {
			target = &withKey
		}
		target.Methods = append(target.Methods, m)
		target.Sites = append(target.Sites, sites...)
	}
	if len(withKey.Methods) == 0 || len(withoutKey.Methods) == 0 {
		return []Cluster{c}
	}
	out := make([]Cluster, 0, 2)
	for _, g := range []Cluster{withKey, withoutKey} {
		sub := c
		sub.Methods, sub.Sites = g.Methods, g.Sites
		sort.Slice(sub.Sites, func(i, j int) bool {
			if sub.Sites[i].File != sub.Sites[j].File {
				return sub.Sites[i].File < sub.Sites[j].File
			}
			return sub.Sites[i].Line < sub.Sites[j].Line
		})
		out = append(out, sub)
	}
	return out
}

func siteHasCallableArg(s Site, callables map[string]bool) bool {
	for _, a := range s.Args {
		if isCallableRef(a, callables) {
			return true
		}
	}
	return false
}

// proposeDecl builds the pattern for a declaration that receives a
// package-typed parameter.
//
// The name is left unconstrained on purpose. Every other template here gates on
// what it observed, but here the package-qualified parameter type is already
// the gate: `*gin.RouterGroup` never appears on a plain helper, so pinning the
// observed function names on top would buy no precision and cost every
// registrar the corpus happens not to contain.
func (TemplateProposer) proposeDecl(c Cluster, opts Options) ([]Proposal, error) {
	if c.TypeName == "" {
		return nil, nil
	}
	nameField := "(identifier) @name"
	if c.DeclKind == "method_declaration" {
		nameField = "(field_identifier) @name"
	}
	typeQuery := "(qualified_type\n        package: (package_identifier) @_pkg\n        name: (type_identifier) @_type)"
	if c.TypePtr {
		typeQuery = "(pointer_type\n        (qualified_type\n          package: (package_identifier) @_pkg\n          name: (type_identifier) @_type))"
	}
	query := fmt.Sprintf(`(%s
  name: %s
  parameters: (parameter_list
    (parameter_declaration
      name: (identifier) @param
      type: %s))
  (#eq? @_pkg %q)
  (#eq? @_type %q)) @_decl`,
		c.DeclKind, nameField, typeQuery, c.pkgAlias(opts), c.TypeName)

	// `<slug>_<type>_param_decl` is bookkeeping, not a call site: it records
	// that this function is handed a package value. classifyPattern maps the
	// `_param_decl` suffix to an inert node that emits no edges — a synthesized
	// declaration must not manufacture a call the corpus does not contain.
	name := opts.Slug() + "_" + strings.ToLower(c.TypeName)
	if c.DeclKind == "method_declaration" {
		name += "_method"
	}
	name += "_param_decl"

	pat := patterns.Pattern{Name: name, Query: query, Captures: []patterns.Capture{
		{Name: "name"}, {Name: "param"},
	}}
	return []Proposal{{Pattern: pat, Cluster: c, Notes: []string{
		"declaration site: records which functions receive a " + c.typeExpr() + ", not a call",
	}}}, nil
}

// proposeBareCall builds the pattern for a call that passes a package value
// across a function boundary.
//
// The callee alternation is over the names actually observed, exactly as the
// method alternation is for a receiver call. It over-fits to the corpus, and
// that is the honest baseline: without it the query is "any call taking any
// identifier", which matches most of a real corpus.
func (TemplateProposer) proposeBareCall(c Cluster, opts Options) ([]Proposal, error) {
	if len(c.Methods) == 0 {
		return nil, nil
	}
	// The argument list is constrained to "has a named argument" and no further.
	// Every sampled site has one — the package value it passes is an identifier
	// — so pinning the full signature would cost nothing in recall and split
	// one concept into a pattern per arity. `@arg` is not claimed to *be* the
	// package value: which argument that is depends on the callee, and the
	// pattern's key is the callee.
	query := fmt.Sprintf(`(call_expression
  function: (identifier) @callee
  %s
  arguments: (argument_list
    (identifier) @arg))`, calleePredicate(c.Methods))

	pat := patterns.Pattern{
		Name:     opts.Slug() + "_binding_arg_call",
		Query:    query,
		Captures: []patterns.Capture{{Name: "callee"}, {Name: "arg"}},
	}
	return []Proposal{{Pattern: pat, Cluster: c, Notes: []string{
		"attributed by argument: a package value flows into this call",
	}}}, nil
}

func calleePredicate(names []string) string {
	if len(names) == 1 {
		return fmt.Sprintf("(#eq? @callee %q)", names[0])
	}
	return fmt.Sprintf("(#match? @callee \"^(%s)$\")", strings.Join(names, "|"))
}

// pkgAlias is the local identifier the corpus binds the package to. The query
// has to spell the alias the corpus uses, not the import path's last element,
// or an aliased import would never match.
func (c Cluster) pkgAlias(opts Options) string {
	if c.PkgAlias != "" {
		return c.PkgAlias
	}
	return goImportIdent(opts.Package)
}

// name derives the pattern name, and with it the node type: classifyPattern is
// name-driven, so `<slug>_get` becomes an http_handler and `<slug>_route_group`
// a route_group. Getting the name wrong is not cosmetic.
func (TemplateProposer) name(c Cluster, slug string) (name, kind string) {
	switch {
	case isGroupShape(c):
		return slug + "_route_group", "group"
	case hasVerb(c.Methods):
		return slug + "_" + strings.ToLower(dominantMethod(c)) + "_route", "route"
	default:
		return slug + "_" + strings.ToLower(dominantMethod(c)), "call"
	}
}

// isGroupShape recognizes `r.Route("/admin", func(r Router){ ... })`: a path
// literal followed by a function literal is a nesting construct, not an
// endpoint, and the two get different node types.
func isGroupShape(c Cluster) bool {
	if len(c.ArgShape) < 2 {
		return false
	}
	return isStringShape(c.ArgShape[0]) && c.ArgShape[1] == "func_literal"
}

func hasVerb(methods []string) bool {
	for _, m := range methods {
		if httpVerbs[strings.ToLower(m)] {
			return true
		}
	}
	return false
}

// dominantMethod is the most frequently observed method name in the cluster,
// ties broken lexicographically — a deterministic choice, so two runs over the
// same corpus produce the same file.
func dominantMethod(c Cluster) string {
	counts := map[string]int{}
	for _, s := range c.Sites {
		counts[s.Method]++
	}
	names := append([]string(nil), c.Methods...)
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	return names[0]
}

func methodPredicate(methods []string) string {
	if len(methods) == 1 {
		return fmt.Sprintf("(#eq? @method %q)", methods[0])
	}
	quoted := make([]string, len(methods))
	copy(quoted, methods)
	return fmt.Sprintf("(#match? @method \"^(%s)$\")", strings.Join(quoted, "|"))
}

func isStringShape(t string) bool {
	return t == "interpreted_string_literal" || t == "raw_string_literal"
}

// arguments builds one query child and one capture per argument position.
//
// Capture *names* are the contract with MatchToGraph, which reads
// `method`/`path`/`handler` by name to mint a node — a positionally-correct
// query with the wrong capture names produces matches and no graph. Positions
// whose role cannot be inferred get a generic `argN` name and no role, which
// is honest: the pattern records the shape and leaves the meaning to review.
func (TemplateProposer) arguments(c Cluster, kind string) ([]patterns.Capture, []string, []string) {
	captures := []patterns.Capture{{Name: "receiver", Role: "receiver"}, {Name: "method", Role: methodRole(kind)}}
	var queryArgs, notes []string
	used := map[string]int{"receiver": 1, "method": 1}

	pathSeen := false
	for i, shape := range c.ArgShape {
		name, role := "", ""
		switch {
		case isStringShape(shape) && !pathSeen && looksLikePath(c, i):
			pathSeen = true
			if kind == "group" {
				name, role = "prefix", "route_prefix"
			} else {
				name, role = "path", "http_path"
			}
		case shape == "func_literal" && kind == "group":
			// A span capture: the `@_` prefix stores the literal's end line on
			// the match, which is what line-range containment needs to know
			// which routes are nested inside the group.
			name = "_body"
		case (shape == "identifier" || shape == "selector_expression" || shape == "func_literal") && pathSeen && kind == "route":
			name, role = "handler", "handler_ref"
		default:
			name = fmt.Sprintf("arg%d", i)
			notes = append(notes, fmt.Sprintf("argument %d (%s): no role inferred", i, shape))
		}
		if used[name] > 0 {
			used[name]++
			name = fmt.Sprintf("%s%d", name, used[name])
		}
		used[name]++

		node := "(_)"
		if shape != "_" {
			node = "(" + shape + ")"
		}
		queryArgs = append(queryArgs, fmt.Sprintf("%s @%s", node, name))
		if !strings.HasPrefix(name, "_") {
			captures = append(captures, patterns.Capture{Name: name, Role: role})
		}
	}
	return captures, queryArgs, notes
}

func methodRole(kind string) string {
	if kind == "route" {
		return "http_method"
	}
	return "method"
}

// looksLikePath reports whether every site in the cluster has a path-shaped
// literal at position i. Every, not any: one `"/health"` among fifty option
// strings does not make the position a path.
func looksLikePath(c Cluster, i int) bool {
	for _, s := range c.Sites {
		if i >= len(s.Args) || !pathLiteral.MatchString(s.Args[i]) {
			return false
		}
	}
	return true
}
