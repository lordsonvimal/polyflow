package linker

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ResolveJSHTTPHosts is Tier JH — the JS/TS analogue of ResolveGoHTTPHosts /
// ResolveRubyHTTPHosts. Neither of those passes has ever run on a JS/TS
// http_client: a client built from `` `${_backendUrl}/api/graph` `` already
// gets its host segment reduced to a wildcard by the JS KeyWalker's
// template-reconstruction (patterns/matcher.go, X.1b) — `Meta["url"]` reads
// `*/api/graph` — but the identifier that produced the hole
// (`_backendUrl`) is discarded in that reconstruction, so Tier CB's guard 3
// (`n.Meta["env_var"]`) is always empty for JS/TS regardless of how good its
// own path-composition logic is.
//
// This pass re-parses the file to recover that identifier from the call
// site's own AST (the reconstruction is lossy; the source on disk is not)
// and resolves it to its single unambiguous module-scope textual origin:
//
//  1. an env read (`process.env.X`, `import.meta.env.X`) — the direct JS/TS
//     equivalent of what Tier L/J.2b already trace for Ruby/Go, stamped
//     Meta["env_var"] so ResolveConfigBaseURLPaths (Tier CB) consumes it
//     exactly as it already does for the other two languages.
//  2. a module-level `let`/`const` string-literal default
//     (`let _backendUrl = 'http://localhost:4747'`) — a genuinely different,
//     weaker evidence class: the value is not read from any config source
//     Tier CB's configsrc.Load knows about, and it may be overwritten at
//     runtime (GitNexus's own `setBackendUrl`). Stamped
//     Meta["host_default_literal"] instead of Meta["env_var"], with
//     Meta["confidence_ceiling"] capped at graph.ConfidencePartial so it is
//     never treated as equivalent-confidence to a committed env value.
//
// Everything else — no interpolation at the host position, an interpolation
// that is not a bare identifier, an identifier with no module-scope
// declaration, an identifier reassigned elsewhere at module scope with a
// non-literal value — resolves to nothing. An honest miss over a guess
// (#12). Reassignment *inside* a function body (an exported setter) does not
// disqualify case 2: that is precisely the weaker-evidence shape the
// confidence cap exists for, not an ambiguity to abstain on.
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
		if ident == "" {
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
// two — confirmed on GitNexus's own backend-client.ts (`deleteRepo`'s
// `fetchWithTimeout(\n  \`${_backendUrl}/api/repo?...\`,\n  ...)`). A window
// small enough that it can't cross into an unrelated statement.
const jsHostLineSlack = 5

// hostIdentAtLine finds a template literal or `+`-concatenation whose host
// position (the very first hole, before any literal text) is a bare
// identifier, and returns its name. Literal text appearing before the first
// hole (`https://${x}`) is not this shape — the KeyWalker only wildcards the
// *host*, not a scheme prefix, so a node whose path/url starts with "*" was
// produced by a hole in the leading position. Searched in document order
// across a small line window starting at line, so the first candidate found
// is the one lexically nearest the call site.
func (jf *jsHostFile) hostIdentAtLine(line int) string {
	var found string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != "" || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row >= line && row <= line+jsHostLineSlack {
			switch n.Type() {
			case "template_string":
				if id := jsTemplateHostIdent(n, jf.src); id != "" {
					found = id
					return
				}
			case "binary_expression":
				if id := jsConcatHostIdent(n, jf.src); id != "" {
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

// jsTemplateHostIdent returns the identifier inside a template literal's
// first `${...}` hole, provided that hole is the template's very first
// segment (no literal text or backtick-adjacent content precedes it) and
// the hole contains nothing but a bare identifier.
func jsTemplateHostIdent(n *sitter.Node, src []byte) string {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		switch c.Type() {
		case "`":
			continue
		case "template_substitution":
			if c.NamedChildCount() != 1 {
				return ""
			}
			inner := c.NamedChild(0)
			if inner.Type() != "identifier" {
				return ""
			}
			return inner.Content(src)
		default:
			return "" // literal text before the first hole — not the host position
		}
	}
	return ""
}

// jsConcatHostIdent returns the identifier at the leftmost operand of a
// `+`-chained concatenation, provided that operand is a bare identifier. A
// chain rooted in anything but `+`, or whose leftmost operand is not a bare
// identifier, is not this shape.
func jsConcatHostIdent(n *sitter.Node, src []byte) string {
	if !jsIsPlus(n, src) {
		return ""
	}
	left := n.ChildByFieldName("left")
	for left != nil && jsIsPlus(left, src) {
		left = left.ChildByFieldName("left")
	}
	if left != nil && left.Type() == "identifier" {
		return left.Content(src)
	}
	return ""
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

// resolveModuleIdent resolves ident to its single module-scope declaration's
// initializer — an env read or a string literal — or ("", jsHostNone) when
// there is none, more than one, or a module-scope (not function-scope)
// reassignment makes the declared value untrustworthy as "the" value.
func (jf *jsHostFile) resolveModuleIdent(ident string) (string, jsHostKind) {
	var declRHS *sitter.Node
	declCount := 0
	for i := 0; i < int(jf.root.NamedChildCount()); i++ {
		stmt := jf.root.NamedChild(i)
		decl := stmt
		if stmt.Type() == "export_statement" {
			if d := stmt.ChildByFieldName("declaration"); d != nil {
				decl = d
			}
		}
		if decl.Type() != "lexical_declaration" && decl.Type() != "variable_declaration" {
			continue
		}
		for j := 0; j < int(decl.NamedChildCount()); j++ {
			d := decl.NamedChild(j)
			if d.Type() != "variable_declarator" {
				continue
			}
			nameNode := d.ChildByFieldName("name")
			if nameNode == nil || nameNode.Type() != "identifier" || nameNode.Content(jf.src) != ident {
				continue
			}
			declCount++
			declRHS = d.ChildByFieldName("value")
		}
	}
	if declCount != 1 || declRHS == nil {
		return "", jsHostNone
	}
	if jf.hasModuleScopeReassign(ident) {
		return "", jsHostNone
	}
	if env := jsEnvReadVar(declRHS, jf.src); env != "" {
		return env, jsHostEnvVar
	}
	if lit, ok := jsStringLiteral(declRHS, jf.src); ok {
		return lit, jsHostDefaultLiteral
	}
	return "", jsHostNone
}

// hasModuleScopeReassign reports whether ident is reassigned by a top-level
// (module-scope, not inside any function) assignment expression elsewhere in
// the file. Such a reassignment means the declared initializer is not
// actually the value in force, unlike a reassignment nested in a function
// body (an exported setter, the common real shape) that only runs when
// called — that case is exactly what the confidence cap on case 2 exists
// for, not a reason to abstain outright.
func (jf *jsHostFile) hasModuleScopeReassign(ident string) bool {
	for i := 0; i < int(jf.root.NamedChildCount()); i++ {
		stmt := jf.root.NamedChild(i)
		if stmt.Type() != "expression_statement" || stmt.NamedChildCount() == 0 {
			continue
		}
		expr := stmt.NamedChild(0)
		if expr.Type() != "assignment_expression" {
			continue
		}
		left := expr.ChildByFieldName("left")
		if left != nil && left.Type() == "identifier" && left.Content(jf.src) == ident {
			return true
		}
	}
	return false
}

// jsEnvReadVar returns the env var name of a `process.env.X` /
// `import.meta.env.X` member-expression, or "". Matched on the object
// subexpression's own source text rather than its node shape, since
// `import.meta` parses as its own construct in the TS grammar rather than an
// ordinary identifier chain.
func jsEnvReadVar(n *sitter.Node, src []byte) string {
	if n == nil || n.Type() != "member_expression" {
		return ""
	}
	prop := n.ChildByFieldName("property")
	obj := n.ChildByFieldName("object")
	if prop == nil || obj == nil {
		return ""
	}
	switch strings.TrimSpace(obj.Content(src)) {
	case "process.env", "import.meta.env":
		return prop.Content(src)
	}
	return ""
}

// jsStringLiteral returns a plain string literal node's content (quotes
// stripped), or ("", false) for anything else — a template literal or any
// other expression is not a literal default.
func jsStringLiteral(n *sitter.Node, src []byte) (string, bool) {
	if n == nil || n.Type() != "string" {
		return "", false
	}
	text := n.Content(src)
	if len(text) < 2 {
		return "", false
	}
	return text[1 : len(text)-1], true
}
