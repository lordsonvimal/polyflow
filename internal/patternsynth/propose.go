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
	if len(c.Methods) == 0 || len(c.Sites) == 0 {
		return nil, nil
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
