package parser

import (
	"context"

	sitter "github.com/smacker/go-tree-sitter"
	pythonsitter "github.com/smacker/go-tree-sitter/python"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// dropNonHTTPPythonMatches drops requests_session_call/httpx_client_call
// matches (patterns/python/requests_instance.yaml, httpx_instance.yaml)
// whose receiver was never assigned `requests.Session()` / `httpx.Client(...)`
// / `httpx.AsyncClient(...)` anywhere in the file. Both call-site patterns
// match any `<identifier>.get/post/.../request(url)` shape — same
// over-match risk requests_client.yaml's module-level `requests.get()`
// pattern already disambiguates by checking the receiver is literally
// "requests" (a fixed name). An instance receiver has no fixed name, so
// positive evidence (a same-file Session/Client construction) is the only
// honest gate — same discipline as Tier PC's receiver typing
// (resolvePythonAttributeCalls), deliberately not JS's more permissive
// keep-unless-known-container approach (axios_instance_call/
// dropNonHTTPJSMatches), per Tier PH's design (docs/python-parity-plan.md).
func dropNonHTTPPythonMatches(results []patterns.MatchResult, src []byte) []patterns.MatchResult {
	needsGate := false
	for _, r := range results {
		if r.PatternName == "requests_session_call" || r.PatternName == "httpx_client_call" {
			needsGate = true
			break
		}
	}
	if !needsGate {
		return results
	}

	instances := pythonHTTPInstanceNames(src)

	out := results[:0]
	for _, r := range results {
		if r.PatternName == "requests_session_call" || r.PatternName == "httpx_client_call" {
			if !instances[r.Captures["via_alias"]] {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// pythonHTTPInstanceNames returns the set of identifiers assigned, anywhere
// in the file, via `requests.Session()`, `httpx.Client(...)`, or
// `httpx.AsyncClient(...)` — a whole-file scope (not per-function like Tier
// PC's locals pre-pass) since a session/client is idiomatically constructed
// once (often at module scope) and reused across every function in the file.
func pythonHTTPInstanceNames(src []byte) map[string]bool {
	names := map[string]bool{}
	p := sitter.NewParser()
	p.SetLanguage(pythonsitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return names
	}
	defer tree.Close()

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "assignment" {
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left != nil && right != nil && left.Type() == "identifier" && right.Type() == "call" {
				if fn := right.ChildByFieldName("function"); fn != nil && fn.Type() == "attribute" {
					obj := fn.ChildByFieldName("object")
					attr := fn.ChildByFieldName("attribute")
					if obj != nil && attr != nil && obj.Type() == "identifier" {
						pkg := obj.Content(src)
						method := attr.Content(src)
						if (pkg == "requests" && method == "Session") ||
							(pkg == "httpx" && (method == "Client" || method == "AsyncClient")) {
							names[left.Content(src)] = true
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(tree.RootNode())
	return names
}
