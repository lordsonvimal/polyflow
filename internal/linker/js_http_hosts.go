package linker

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// ResolveJSHTTPHosts is Tier JH — the JS/TS analogue of ResolveGoHTTPHosts /
// ResolveRubyHTTPHosts, and VG.7's third migrated pass: the module-scope
// identifier resolution below is the valuegraph engine's ordinary
// resolveSymbol/bindingsIn behaviour (internal/valuegraph/engine.go), not a
// bespoke walk — this pass supplies only what the engine has no vocabulary
// for: finding the host identifier at the call site, and recognising a
// resolved member-expression as an env read. Unlike ruby_http_hosts (Tier
// FX), this stayed on Tier VG because the JS binding spec already existed
// and needed no new construct; see docs/js-value-graph-pilot-plan.md VG.7.
//
// A JS/TS http_client built from a template literal such as
// `${_backendUrl}/api/graph` already
// gets its host segment reduced to a wildcard by the JS KeyWalker's
// template-reconstruction (patterns/matcher.go, X.1b) — `Meta["url"]` reads
// `*/api/graph` — but the identifier that produced the hole
// (`_backendUrl`) is discarded in that reconstruction, so Tier CB's guard 3
// (`n.Meta["env_var"]`) is always empty for JS/TS regardless of how good its
// own path-composition logic is.
//
// This pass re-parses the file to recover that identifier from the call
// site's own AST (the reconstruction is lossy; the source on disk is not)
// and resolves it to its single unambiguous module-scope origin, via the
// engine:
//
//  1. an env read (`process.env.X`, `import.meta.env.X`) — the direct JS/TS
//     equivalent of what Tier L/J.2b already trace for Ruby/Go, stamped
//     Meta["env_var"] so ResolveConfigBaseURLPaths (Tier CB) consumes it
//     exactly as it already does for the other two languages.
//  2. a module-level string-literal default (`let _backendUrl =
//     'http://localhost:4747'`) — a genuinely different, weaker evidence
//     class: the value is not read from any config source Tier CB's
//     configsrc.Load knows about, and it may be overwritten at runtime (an
//     exported setter). Stamped Meta["host_default_literal"] instead of
//     Meta["env_var"], with Meta["confidence_ceiling"] capped at
//     graph.ConfidencePartial so it is never treated as equivalent-confidence
//     to a committed env value.
//
// Everything else resolves to nothing: no interpolation at the host
// position, an interpolation that is not a bare identifier, an identifier
// the engine cannot bind to exactly one value (no declaration, more than
// one, or a value that isn't a literal or a recognised env read), or a
// module-scope reassignment — the engine folds that into the same Union the
// declaration sits in, which is neither Literal nor a bare env-member
// Opaque, so it abstains without this pass having to special-case it. An
// honest miss over a guess (#12). Reassignment *inside* a function body (an
// exported setter) does not disqualify case 2: bindingsIn already skips a
// nested scope that does not span the use site.
//
// Returns the mutated http_client nodes so the caller can re-persist them;
// the node metas are also mutated in place in the passed slice.
func ResolveJSHTTPHosts(nodes []graph.Node, serviceFiles map[string][]string) []graph.Node {
	svcNeeds := make(map[string]bool)
	for i := range nodes {
		if jsDynamicHTTPNode(&nodes[i]) {
			svcNeeds[nodes[i].Service] = true
		}
	}
	if len(svcNeeds) == 0 {
		return nil
	}

	fileCache := make(map[string]*jsHostFile)
	var changed []graph.Node
	for i := range nodes {
		n := &nodes[i]
		if !jsDynamicHTTPNode(n) {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			jf = parseJSHostFile(n.File)
			fileCache[n.File] = jf
		}
		if jf == nil {
			continue
		}
		ident := jf.hostIdentAtLine(n.Line)
		if ident == nil {
			continue
		}
		val, kind := jf.resolveModuleIdent(ident)
		if val == "" {
			continue
		}
		n.Meta = ensureMeta(n.Meta)
		switch kind {
		case jsHostEnvVar:
			n.Meta["env_var"] = val
			n.Meta["host_resolved_via"] = "js_env_var"
		case jsHostDefaultLiteral:
			n.Meta["host_default_literal"] = val
			n.Meta["host_resolved_via"] = "js_module_default"
			n.Meta["confidence_ceiling"] = graph.ConfidencePartial
		}
		changed = append(changed, *n)
	}
	return changed
}

// jsDynamicHTTPNode reports whether n is a JS/TS http_client whose host
// segment is an unresolved wildcard hole — the KeyWalker's marker for "at
// least one `${...}`/concatenation operand here" (patterns/matcher.go,
// jsReconstructTemplateString/jsReconstructConcat) — and not already
// attributed by a prior run.
func jsDynamicHTTPNode(n *graph.Node) bool {
	if n.Type != graph.NodeTypeHTTPClient || n.File == "" {
		return false
	}
	if n.Language != "javascript" && n.Language != "typescript" {
		return false
	}
	if n.Meta["env_var"] != "" || n.Meta["host_default_literal"] != "" {
		return false // already attributed (idempotent re-run)
	}
	return strings.HasPrefix(n.Meta["url"], "*") || strings.HasPrefix(n.Meta["path"], "*")
}

// ── per-file AST ─────────────────────────────────────────────────────────

type jsHostFile struct {
	src  []byte
	root *sitter.Node
}

func parseJSHostFile(file string) *jsHostFile {
	if !isJSFile(file) {
		return nil
	}
	src, root, _, ok := jsParse(file)
	if !ok {
		return nil
	}
	return &jsHostFile{src: src, root: root}
}

// jsHostLineSlack bounds how many lines past the http_client node's own line
// a candidate template literal / concatenation may start on. The node's line
// is the enclosing call site's start (`streamSSE(` / `fetchWithTimeout(`),
// but a wrapped call frequently puts its URL argument on the next line or
// two — confirmed on a real backend-client.ts (`deleteRepo`'s
// `fetchWithTimeout(\n  \`${_backendUrl}/api/repo?...\`,\n  ...)`). A window
// small enough that it can't cross into an unrelated statement.
const jsHostLineSlack = 5

// hostIdentAtLine finds a template literal or `+`-concatenation whose host
// position (the very first hole, before any literal text) is a bare
// identifier, and returns that identifier's node. Literal text appearing
// before the first hole (`https://${x}`) is not this shape — the KeyWalker
// only wildcards the *host*, not a scheme prefix, so a node whose path/url
// starts with "*" was produced by a hole in the leading position. Searched
// in document order across a small line window starting at line, so the
// first candidate found is the one lexically nearest the call site.
func (jf *jsHostFile) hostIdentAtLine(line int) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row >= line && row <= line+jsHostLineSlack {
			switch n.Type() {
			case "template_string":
				if id := jsTemplateHostIdent(n, jf.src); id != nil {
					found = id
					return
				}
			case "binary_expression":
				if id := jsConcatHostIdent(n, jf.src); id != nil {
					found = id
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(jf.root)
	return found
}

// jsTemplateHostIdent returns the identifier node inside a template
// literal's first `${...}` hole, provided that hole is the template's very
// first segment (no literal text or backtick-adjacent content precedes it)
// and the hole contains nothing but a bare identifier.
func jsTemplateHostIdent(n *sitter.Node, src []byte) *sitter.Node {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case "`":
			continue
		case "template_substitution":
			if c.NamedChildCount() != 1 {
				return nil
			}
			inner := c.NamedChild(0)
			if inner.Type() != "identifier" {
				return nil
			}
			return inner
		default:
			return nil // literal text before the first hole — not the host position
		}
	}
	return nil
}

// jsConcatHostIdent returns the identifier node at the leftmost operand of a
// `+`-chained concatenation, provided that operand is a bare identifier. A
// chain rooted in anything but `+`, or whose leftmost operand is not a bare
// identifier, is not this shape.
func jsConcatHostIdent(n *sitter.Node, src []byte) *sitter.Node {
	if !jsIsPlus(n, src) {
		return nil
	}
	left := n.ChildByFieldName("left")
	for left != nil && jsIsPlus(left, src) {
		left = left.ChildByFieldName("left")
	}
	if left != nil && left.Type() == "identifier" {
		return left
	}
	return nil
}

func jsIsPlus(n *sitter.Node, src []byte) bool {
	if n == nil || n.Type() != "binary_expression" {
		return false
	}
	op := n.ChildByFieldName("operator")
	return op != nil && string(src[op.StartByte():op.EndByte()]) == "+"
}

// ── module-scope resolution ─────────────────────────────────────────────

type jsHostKind int

const (
	jsHostNone jsHostKind = iota
	jsHostEnvVar
	jsHostDefaultLiteral
)

// reJSEnvMember matches a resolved member-expression's source text against
// the two env-read shapes Tier L/J.2b already recognise for the other
// languages. Matched on text rather than re-walking the node: the engine has
// already stopped at this member_expression and handed back only its source
// span (Value.Origin.Text), which is all a caller needs to tell "this
// Opaque IS an env var" from any other member read (VG.4 §7.2 precedent —
// policy the lattice itself does not carry, applied in the adapter).
var reJSEnvMember = regexp.MustCompile(`^(?:process\.env|import\.meta\.env)\.([A-Za-z_$][A-Za-z0-9_$]*)$`)

// resolveModuleIdent resolves ident to its single module-scope value via the
// valuegraph engine — an env read or a string literal — or ("", jsHostNone)
// for anything else: no binding, more than one (the engine folds multiple
// bindings of one name into a Union, which matches neither case below), a
// call or other opaque expression the engine cannot follow, or a
// module-scope reassignment (also folded into the same disqualifying
// Union — see the engine's bindingsIn: a nested function scope that does not
// span ident's use site is skipped entirely, which is what keeps an
// exported setter's reassignment from disqualifying case 2).
func (jf *jsHostFile) resolveModuleIdent(ident *sitter.Node) (string, jsHostKind) {
	eng := valuegraph.New(jsValuegraphSpec(), jsEngineFileSource{}, valuegraph.Options{})
	v := eng.Resolve(valuegraph.Query{Src: jf.src, Root: jf.root, Expr: ident})
	switch v.Kind {
	case valuegraph.KindLiteral:
		return v.Text, jsHostDefaultLiteral
	case valuegraph.KindOpaque:
		if v.Origin.Reason == valuegraph.ReasonMember {
			if m := reJSEnvMember.FindStringSubmatch(v.Origin.Text); m != nil {
				return m[1], jsHostEnvVar
			}
		}
	}
	return "", jsHostNone
}
