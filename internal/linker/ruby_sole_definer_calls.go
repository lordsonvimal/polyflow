package linker

import (
	"fmt"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkRubySoleDefinerCalls resolves a Ruby typed-receiver call
// (`file.clear_lock!`) ledgered as typed_call_ref by ruby_variables.go's
// case "call" default-receiver branch (DC.32). Ruby's dynamism rules out
// real type inference here — the receiver's actual class is unknown — but
// when exactly one method in the whole service is named this, that
// ambiguity collapses on its own: there is nothing else the call could
// possibly reach. This is the same sole-candidate discipline
// LinkRubyMixinMethods' emitBareCallFallback (DC.21) already applies to a
// bare call with no ancestor-reachable definition, reused here via the same
// ix.byNameService index — the only difference is the call site's receiver,
// which this pass never inspects: a typed receiver's own class is
// irrelevant once the name is known to be unique service-wide.
//
// Unlike every other unresolved-ref consumer in this package, a
// typed_call_ref that fails to resolve is never carried forward (see
// link_passes.go's wiring, which drops every typed_call_ref regardless of
// outcome). The parser's rubyCommonMethodNames denylist already screens out
// Ruby/Rails/Enumerable vocabulary before ledgering, but the long tail of
// gem and ActiveRecord methods this pass will never see a second definition
// of is still ledgered on the optimistic chance the app also defines the
// name — and when it does not, no other pass can ever explain the miss, so
// keeping it around would only inflate deadcode's "verify N manually"
// ledger footer with entries nothing will ever resolve.
func LinkRubySoleDefinerCalls(
	nodes []graph.Node,
	edges []graph.Edge,
	allUnresolved []graph.UnresolvedRef,
) (newEdges []graph.Edge, resolved map[string]bool) {
	resolved = make(map[string]bool)

	ix := newRubyMixinIndex(nodes, edges)
	if len(ix.byNameService) == 0 {
		return nil, resolved
	}

	refs := make([]graph.UnresolvedRef, 0, 32)
	for _, u := range allUnresolved {
		if u.Kind == "typed_call_ref" && isRubyFile(u.File) {
			refs = append(refs, u)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		if refs[i].Line != refs[j].Line {
			return refs[i].Line < refs[j].Line
		}
		return refs[i].Name < refs[j].Name
	})

	seen := make(map[string]bool)
	for _, ref := range refs {
		e := ix.emitSoleDefiner(ref, seen)
		if e == nil {
			continue
		}
		newEdges = append(newEdges, *e)
		resolved[RubyCallRefKey(ref.File, ref.Line, ref.Name)] = true
	}
	return newEdges, resolved
}

// emitSoleDefiner binds ref to the one method in its service named
// ref.Name, if and only if there is exactly one — two or more is exactly
// the ambiguity static type inference would have resolved and a heuristic
// must not guess at, so it is left unresolved instead (rule 9).
func (ix *rubyMixinIndex) emitSoleDefiner(ref graph.UnresolvedRef, seen map[string]bool) *graph.Edge {
	candidates := ix.byNameService[ref.Service][ref.Name]
	if len(candidates) != 1 {
		return nil
	}
	to := candidates[0]

	fromID := ""
	if cls, ok := innermost(ix.classSpans[ref.File], ref.Line); ok {
		fromID = cls.id
	}
	if fn, ok := innermost(ix.funcSpans[ref.File], ref.Line); ok {
		fromID = fn.id
	}
	if fromID == "" || fromID == to {
		return nil
	}
	// Asserted, not assumed: byNameService is built per-service, so `to`
	// already shares ref.Service; fromID is the enclosing scope of a ref
	// parsed from a file in that same service. A mismatch here would mean
	// the index itself is broken, not that this call site is ambiguous.
	if ix.serviceOf[fromID] != ix.serviceOf[to] {
		return nil
	}

	id := fmt.Sprintf("calls:%s->%s", fromID, to)
	if seen[id] {
		return nil
	}
	seen[id] = true
	return &graph.Edge{
		ID:         id,
		From:       fromID,
		To:         to,
		Type:       graph.EdgeTypeCalls,
		Confidence: graph.ConfidenceInferred,
		Meta:       map[string]string{"via": "sole_definer"},
	}
}
