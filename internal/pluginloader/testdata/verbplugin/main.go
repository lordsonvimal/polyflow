// Command verbplugin is FX.9's reference fixture (Tier FX,
// docs/declarative-framework-pipeline-plan.md): a linkplugin.Plugin that also
// implements linkplugin.VerbProvider, proving the handshake -> Verbs() ->
// ExtractVerb() -> value round-trip end-to-end. It advertises exactly one
// verb, ancestor_matching(kind, predicate), a generic AST operation with no
// framework knowledge whatsoever — it walks a node's ancestor chain and
// reports whether any ancestor's node type equals kind and whose source text
// contains predicate. This is deliberately the same shape as the in-tree
// enclosing()/enclosingName() verbs (internal/patterns/extract.go), so a
// plugin author has a real worked example of a verb that could have been
// in-tree but doesn't need to be.
//
// It is not a template for a real plugin author's Link()/Requires() —
// see testdata/fakeplugin for that half. This fixture's Link is a stub.
package main

import (
	"strings"

	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
)

// ancestorMatchingVerb is the one verb this plugin advertises. Never a
// framework name — FX.9's generic-only rule applies to a plugin's own
// reference verb same as any other.
const ancestorMatchingVerb = "ancestor_matching"

type verbPlugin struct{}

func (verbPlugin) Name() string { return "verbplugin" }

func (verbPlugin) Requires(string) []linkplugin.Capability { return nil }

// Link is unused by FX.9's own test — this fixture exists to exercise
// VerbProvider, not LinkPlugin — but Plugin requires it.
func (verbPlugin) Link(*linkplugin.LinkContext) (linkplugin.Result, error) {
	return linkplugin.Result{}, nil
}

func (verbPlugin) Verbs() []string { return []string{ancestorMatchingVerb} }

// ExtractVerb implements ancestor_matching(kind,predicate): "true" (mirroring
// the in-tree has_keyword verb's boolean-as-string convention) if any
// ancestor's Type equals kind and whose Text contains predicate, else "".
// arg is the verb's parenthesized argument, "kind,predicate" — the same
// comma-split-first-field convention runVerb's own multi-arg verbs use
// (e.g. call_ref's "field,names").
func (verbPlugin) ExtractVerb(verb, arg string, node linkplugin.VerbNode, file, grammar string) ([]linkplugin.VerbValue, bool) {
	if verb != ancestorMatchingVerb {
		return nil, false
	}
	kind, predicate, _ := strings.Cut(arg, ",")
	kind = strings.TrimSpace(kind)
	predicate = strings.TrimSpace(predicate)

	found := ""
	for _, a := range node.Ancestors {
		if a.Type == kind && strings.Contains(a.Text, predicate) {
			found = "true"
			break
		}
	}
	return []linkplugin.VerbValue{{Str: found}}, true
}

func main() {
	linkplugin.Serve(verbPlugin{})
}
