package patterns

import sitter "github.com/smacker/go-tree-sitter"

// verbprovider.go is Tier FX's FX.9 seam
// (docs/declarative-framework-pipeline-plan.md): a plugin can register a
// named `extract:` verb that runVerb (extract.go) dispatches to exactly like
// an in-tree verb, so a new framework's pattern YAML can name a generic AST
// operation without a core release. internal/pluginloader is the only
// registrant — it already imports this package (loading a plugin
// component's pattern YAML via LoadFile), so the dependency can only run
// this direction: this package must never import internal/pluginloader or
// sdk/linkplugin, or the two packages would cycle. RegisterVerbProvider's
// shape (package-level map + registration func) mirrors
// internal/factpipe/hub.go's RegisterHub/HubProvider.
//
// A VerbProviderFunc is plain data this package owns (VerbNode/VerbResult),
// never a proto or SDK type — internal/pluginloader's registered closure is
// what marshals to/from the wire on each call.

// VerbNode is a serializable snapshot of one tree-sitter node handed to a
// plugin verb — the same fields sdk/linkplugin.VerbNode mirrors on the wire.
// Ancestors is node's parent chain, immediate parent first, tree root last;
// buildAncestorChain populates it once per plugin-verb call, only when a
// verb provider is actually registered for the name being run (a verb no
// plugin advertises never pays this walk).
type VerbNode struct {
	Type      string
	Text      string
	StartLine int64
	EndLine   int64
	Ancestors []VerbNode
}

// VerbResult is one value a plugin verb yields — the plain-data mirror of
// verbVal restricted to what a plugin can produce: it never holds a live
// *sitter.Node, so there is no node-kind case.
type VerbResult struct {
	Str   string
	Int   int64
	IsInt bool
}

// VerbProviderFunc is a registered plugin verb — ok mirrors an in-tree verb's
// own "declined" convention (verb.go's normalizeHTTPVerb doc): false means
// "this input does not apply", not an error.
type VerbProviderFunc func(node VerbNode, arg, file, grammar string) ([]VerbResult, bool)

var verbProviderRegistry = map[string]VerbProviderFunc{}

// RegisterVerbProvider adds a named plugin verb, called by
// internal/pluginloader once per accepted (post generic-only-check)
// advertised verb name. A duplicate name is a programming/configuration
// error, not a data error — same discipline as RegisterHub.
func RegisterVerbProvider(name string, fn VerbProviderFunc) {
	if _, dup := verbProviderRegistry[name]; dup {
		panic("patterns: duplicate verb provider " + name)
	}
	verbProviderRegistry[name] = fn
}

// ResetVerbProviders clears every registered plugin verb — test-only, so
// package-level registration state doesn't leak between independent test
// binaries/subtests that each launch their own plugin subprocess.
func ResetVerbProviders() {
	verbProviderRegistry = map[string]VerbProviderFunc{}
}

// lookupVerbProvider is runVerb's hook into the registry — nil if nothing is
// registered under name, which is exactly the "unknown verb" case runVerb's
// default already handles.
func lookupVerbProvider(name string) VerbProviderFunc {
	return verbProviderRegistry[name]
}

// RunRegisteredVerb calls a registered plugin verb directly — the public
// counterpart to RegisterVerbProvider, for a caller (internal/pluginloader's
// own tests; a future `polyflow doctor`-style verification) that wants to
// confirm what got registered without going through a full pattern-match
// pass. ok is false both when nothing is registered under name and when the
// registered verb itself declines this input — the two are indistinguishable
// on purpose, matching runVerb's own "unregistered == fall through" handling.
func RunRegisteredVerb(name string, node VerbNode, arg, file, grammar string) ([]VerbResult, bool) {
	fn := lookupVerbProvider(name)
	if fn == nil {
		return nil, false
	}
	return fn(node, arg, file, grammar)
}

// buildAncestorChain walks n.Parent() up to the tree root, converting each
// ancestor to a VerbNode with no further Ancestors (only the leaf node
// carries the chain — an ancestor's own ancestors would just be a suffix of
// the same list, redundant to ship).
func buildAncestorChain(n *sitter.Node, src []byte) []VerbNode {
	var out []VerbNode
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		out = append(out, VerbNode{
			Type:      cur.Type(),
			Text:      cur.Content(src),
			StartLine: int64(cur.StartPoint().Row) + 1,
			EndLine:   int64(cur.EndPoint().Row) + 1,
		})
	}
	return out
}
