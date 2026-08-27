package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkRubyOverrideDispatch resolves the downward join LinkRubyMixinMethods
// deliberately leaves alone. LinkRubyMixinMethods answers "a subclass or
// includer calls a bare name and an ancestor/mixin defines it" (upward,
// toward the root of the hierarchy); this pass answers the mirror-image
// question: "a base class or mixin calls a bare/self-qualified name and one
// or more subclasses/includers OVERRIDE it" (downward, toward the leaves).
// Confirmed live in three shapes, all in the same service:
//
//  1. `UserProvisioning::Products::Base#perform` calls `perform_create`
//     (bare, implicit self); only its subclasses define it.
//  2. `AuditCommentable` (a concern) calls `audit_record_identifier` on
//     self inside a method the concern itself defines; seven including
//     models each override it.
//  3. `user.mini_orange_sync_action`, where `user` is a local variable of
//     inferred type `User` (not self) and subclasses of `User` override the
//     method. This shape reduces to the same join key ("a call site
//     inside/via class A naming method foo") but the call site never
//     surfaces as a `call_ref` — it is already resolved by
//     ruby_receiver_types.go's inferred-receiver machinery, so it is fed
//     through emitClassMethodCall's optional fan-out hook instead of a
//     second scan here (see ruby_class_method_calls.go).
//
// Like LinkRubyMixinMethods, this is a join against data other passes
// already computed — the same `inherits` edges (LinkRubyTypeRelations) and
// the same Meta["class"] method ownership — just traversed in the opposite
// direction: descendants instead of ancestors.
//
// # Two input sources for shapes 1 and 2
//
// A base/mixin call only reaches call_ref (the ledger LinkRubyMixinMethods
// also reads) when extractRubyVariables could NOT resolve it in-file — but
// shape 1's real case, `Base#perform_create`, is not abstract: it raises
// NotImplementedError, a real same-file, same-class method the parser
// already resolves, producing a `calls` edge with no Meta before this pass
// ever runs. "In addition to A's own foo" (the deliverable's phrase) is not
// an edge case, it is shape 1's actual shape, so this pass has a second
// input source alongside the call_ref scan: every already-resolved,
// same-file, same-class `calls` edge (recognized by empty Meta —
// resolveBareCall in ruby_variables.go is the only same-file Ruby
// call-resolution path that ever emits one), fanned out the same way. The
// two sources are disjoint by construction — a call either resolved
// same-file (this source) or it didn't (call_ref) — so there is no
// double-processing to guard against.
//
// # Fan-out, never first-match
//
// A base class's own call site can be reached at runtime by any subtype, not
// just the lexically nearest override — bug-class #1 applies directly. If
// three subclasses override `perform_create`, the call site gets three
// edges, not one guessed edge to whichever subclass the index visits first.
//
// # Why literal `Const.method` calls do not fan out
//
// A call written against an exact, literal class name (`Product.find`,
// resolved by LinkRubyClassMethodCalls) can never dispatch to a subclass at
// that call site — the literal name already pins the runtime class. Fan-out
// only makes sense where the call site's true runtime type could be some
// subtype of the class actually named: `self` inside a base/mixin (shapes 1
// and 2 above), or a variable whose type was *inferred* rather than written
// literally (shape 3). That is why this pass only ever processes bare/self
// call_ref entries directly, and why emitClassMethodCall's fan-out hook is
// wired only into LinkRubyReceiverTypeCalls (inferred receivers), never into
// LinkRubyClassMethodCalls (literal receivers).
func LinkRubyOverrideDispatch(
	nodes []graph.Node,
	edges []graph.Edge,
	allUnresolved []graph.UnresolvedRef,
) (newEdges []graph.Edge, resolved map[string]bool, unresolvedOut []graph.UnresolvedRef) {
	resolved = make(map[string]bool)

	ix := newRubyMixinIndex(nodes, edges)
	if len(ix.ancestors) == 0 {
		return nil, resolved, nil
	}
	ox := newRubyOverrideIndex(ix)

	refs := make([]graph.UnresolvedRef, 0, 64)
	for _, u := range allUnresolved {
		if u.Kind == "call_ref" && isRubyFile(u.File) {
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
		cls, ok := innermost(ix.classSpans[ref.File], ref.Line)
		if !ok {
			continue
		}
		fromID := cls.id
		if fn, ok := innermost(ix.funcSpans[ref.File], ref.Line); ok {
			fromID = fn.id
		}
		e, u := ox.emit(fromID, cls.id, ref.Name, cls.svc, ref.File, ref.Line, seen)
		if len(e) == 0 && len(u) == 0 {
			continue
		}
		newEdges = append(newEdges, e...)
		unresolvedOut = append(unresolvedOut, u...)
		// Marked resolved even in the capped case: the raw call_ref is
		// replaced by the more specific ruby_override_fanout_capped ledger
		// entry, not left behind as a plain unexplained miss.
		resolved[RubyCallRefKey(ref.File, ref.Line, ref.Name)] = true
	}

	// A base class that itself declares a default/stub implementation (`def
	// perform_create; raise NotImplementedError; end`) never produces a
	// call_ref at all: extractRubyVariables resolves the bare/self call to
	// that same-file, same-class method directly (internal/parser/
	// ruby_variables.go's resolveBareCall), emitting a `calls` edge with no
	// Meta — confirmed live against orion-atlas's UserProvisioning::Products
	// ::Base#perform, whose `perform_create` call resolves to Base's own
	// NotImplementedError stub this way, not through the ledger. That edge
	// is the "in addition to A's own foo" case the deliverable calls out, so
	// it needs its own input source: every already-resolved, same-file,
	// same-class `calls` edge (Meta empty is resolveBareCall's signature —
	// it is the only same-file Ruby call-resolution path that ever emits a
	// nil-Meta edge) is a candidate self-call to fan out from, keyed off the
	// class that line-range-owns the target method, not off Meta["class"]
	// text — a literal `OtherClass.method` call in the same file is resolved
	// by this exact same code path too, and must NOT fan out (an explicit
	// class name pins the runtime class, same boundary
	// LinkRubyClassMethodCalls draws): comparing the caller's and callee's
	// owning classID by line containment is what tells the two apart.
	nodeByID := make(map[string]*graph.Node, len(nodes))
	for i := range nodes {
		nodeByID[nodes[i].ID] = &nodes[i]
	}
	for i := range edges {
		e := &edges[i]
		if e.Type != graph.EdgeTypeCalls || len(e.Meta) != 0 {
			continue
		}
		fromNode, toNode := nodeByID[e.From], nodeByID[e.To]
		if fromNode == nil || toNode == nil || fromNode.Language != "ruby" || toNode.Language != "ruby" {
			continue
		}
		callerClassID, ok1 := innermostClassID(ix, fromNode.File, fromNode.Line)
		targetClassID, ok2 := innermostClassID(ix, toNode.File, toNode.Line)
		if !ok1 || !ok2 || callerClassID != targetClassID {
			continue
		}
		fe, fu := ox.emit(e.From, targetClassID, toNode.Label, fromNode.Service, fromNode.File, fromNode.Line, seen)
		newEdges = append(newEdges, fe...)
		unresolvedOut = append(unresolvedOut, fu...)
	}

	return newEdges, resolved, unresolvedOut
}

// innermostClassID returns the classID whose span (by line range) contains
// line in file, i.e. the class that declares whatever is at that line.
func innermostClassID(ix *rubyMixinIndex, file string, line int) (string, bool) {
	cls, ok := innermost(ix.classSpans[file], line)
	if !ok {
		return "", false
	}
	return cls.id, true
}

// ---------------------------------------------------------------------------
// override index
// ---------------------------------------------------------------------------

// rubyOverrideFanoutCap bounds how many subclass/includer overrides a single
// call site fans out to, mirroring templ_layer.go's maxClassFanout: a class
// hierarchy wide enough to exceed this carries no more real signal about
// which override actually runs at a given call site than a utility CSS class
// shared by dozens of elements does about which element a selector means.
// Picked as the same round number pending measurement against a corpus with
// a genuinely wide override hierarchy — revisit if one shows the cap firing
// too eagerly or not eagerly enough.
const rubyOverrideFanoutCap = 20

// rubyOverrideIndex answers "which descendants of classID override name",
// the mirror image of rubyMixinIndex.lookup's "which ancestor of classID
// defines name". Built from the same index rather than a second full scan of
// nodes/edges — only the ancestors map needs inverting.
type rubyOverrideIndex struct {
	*rubyMixinIndex
	descendants map[string][]string // classID -> direct child/includer classIDs
}

func newRubyOverrideIndex(ix *rubyMixinIndex) *rubyOverrideIndex {
	descendants := map[string][]string{}
	for child, parents := range ix.ancestors {
		for _, parent := range parents {
			descendants[parent] = append(descendants[parent], child)
		}
	}
	for id := range descendants {
		descendants[id] = sortedUnique(descendants[id])
	}
	return &rubyOverrideIndex{rubyMixinIndex: ix, descendants: descendants}
}

// overrides returns every descendant classID, at any depth up to
// rubyMixinMaxDepth, that itself declares a method named `name`. Unlike
// rubyMixinIndex.lookup (which stops at the shallowest ancestor match, since
// that is the one Ruby's method-resolution order actually runs), a call
// site inside a base class can be reached by ANY subtype at runtime — a
// grandchild's override is just as reachable as a direct child's, so every
// depth that has a match contributes, not just the nearest one.
func (ox *rubyOverrideIndex) overrides(classID, name string) []string {
	visited := map[string]bool{classID: true}
	frontier := []string{classID}
	var out []string
	for depth := 0; depth <= rubyMixinMaxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, child := range ox.descendants[id] {
				if visited[child] {
					continue
				}
				visited[child] = true
				next = append(next, child)
				if len(ox.methods[child+"\x00"+name]) > 0 {
					out = append(out, child)
				}
			}
		}
		sort.Strings(next)
		frontier = next
	}
	return sortedUnique(out)
}

// emit resolves one call site's downward fan-out: every override target,
// capped, as `calls` edges from fromID; or a single ledger entry if the
// override set exceeds the cap. Shared by LinkRubyOverrideDispatch (bare/self
// call_ref entries) and emitClassMethodCall's fan-out hook (inferred
// receiver-typed calls), since both reduce to the same (classID, name) join.
func (ox *rubyOverrideIndex) emit(
	fromID, classID, name, svc, file string,
	line int,
	seen map[string]bool,
) ([]graph.Edge, []graph.UnresolvedRef) {
	targets := ox.overrides(classID, name)
	if len(targets) == 0 {
		return nil, nil
	}
	if len(targets) > rubyOverrideFanoutCap {
		return nil, []graph.UnresolvedRef{{
			Service: svc, File: file, Line: line, Name: name,
			Kind:    "ruby_override_fanout_capped",
			Targets: formatOverrideFanoutTargets(targets),
		}}
	}

	var edges []graph.Edge
	for _, tClassID := range targets {
		for _, mID := range ox.methods[tClassID+"\x00"+name] {
			// Asserted, not assumed: newRubyMixinIndex's own ancestors walk
			// carries no same-service check (that is ruby_mixin_methods.go's
			// emit's job for the upward direction) — see the vendored-copy
			// trap in ruby_mixin_methods.go's doc comment for why a name-only
			// join must never cross a service boundary.
			if ox.serviceOf[fromID] != ox.serviceOf[mID] {
				continue
			}
			id := fmt.Sprintf("calls:%s->%s", fromID, mID)
			if seen[id] {
				continue
			}
			seen[id] = true
			edges = append(edges, graph.Edge{
				ID:         id,
				From:       fromID,
				To:         mID,
				Type:       graph.EdgeTypeCalls,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"via": "override_dispatch"},
			})
		}
	}
	return edges, nil
}

// formatOverrideFanoutTargets renders the classIDs a fan-out cap suppressed,
// capped by maxFanoutTargetsListed (templ_layer.go's convention for the same
// situation) so a single ledger entry for an extreme hierarchy doesn't become
// a token dump.
func formatOverrideFanoutTargets(classIDs []string) string {
	n := len(classIDs)
	if n > maxFanoutTargetsListed {
		n = maxFanoutTargetsListed
	}
	lines := append([]string{}, classIDs[:n]...)
	if rest := len(classIDs) - n; rest > 0 {
		lines = append(lines, fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(lines, "\n")
}
