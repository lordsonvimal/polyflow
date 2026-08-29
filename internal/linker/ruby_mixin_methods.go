package linker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkRubyMixinMethods resolves a bare Ruby method call to the method a mixin
// or superclass contributes to the calling class.
//
// `logger_context(__method__)` is the single most common unresolved reference on
// the juniper fleet — 2210 of 7941 `call_ref` entries, with `logger_yes_no`
// (365) and `lean_backtrace` (323) behind it. All three are defined in
// `lib/dx.rb` and reached by the 203 classes that write `include Dx`. The
// parser resolves a bare call only against the calling file, so every one of
// them was recorded as a blind spot: the ledger told an agent to go read 2898
// files to find three methods.
//
// # Why this reads the graph instead of re-parsing
//
// Unlike LinkRailsFilters, nothing here needs source that a node dropped. The
// ancestor chain is already in the graph as `inherits` edges — LinkRubyTypeRelations
// emits one per `include`/`extend`/`prepend` and one per superclass, resolved
// the way Ruby resolves a constant (innermost namespace outward) and confined to
// one service. Method ownership is already on the function nodes as
// `Meta["class"]`, and both class and function nodes carry `end_line`, which is
// what locates the call site's enclosing scope. So this pass is a join, and it
// inherits the correctness properties of the pass it joins against for free —
// including the one that matters most below.
//
// # The vendored-copy trap
//
// Four repos in the fleet each ship their own `lib/dx.rb` defining the same
// three method names. Resolving `logger_context` by name across the workspace
// would bind each of orion's 2210 call sites to all four copies: roughly 8700
// edges, three quarters of them crossing a service boundary that no Ruby process
// can cross. This is the same failure that produced 744 phantom cross-repo
// `inherits` edges before Tier K.7 (see the header of ruby_type_relations.go).
//
// It cannot recur here, because resolution never looks up a name — it walks
// `inherits` edges, which are already per-service. The service equality check in
// emit() is therefore redundant, and is kept as an assertion: if a future change
// to the type linker lets an edge cross services, this pass fails closed rather
// than multiplying the mistake by 2898.
//
// # Ambiguity
//
// Ruby's method lookup is ordered — prepends, then the class, then includes in
// reverse source order, then the superclass — but `inherits` edges do not record
// source order, so two mixins that both define a name cannot be ranked. Rather
// than pick one, the shallowest depth that has any definition contributes a
// candidate edge to each definition at that depth plus a `mixin_method_collision`
// ledger entry (rule 1). Depth still discriminates the common real case: a
// concern's method does not lose to a grandparent's.
func LinkRubyMixinMethods(
	nodes []graph.Node,
	edges []graph.Edge,
	allUnresolved []graph.UnresolvedRef,
) (newEdges []graph.Edge, resolved map[string]bool, unresolvedOut []graph.UnresolvedRef, newNodes []graph.Node) {
	resolved = make(map[string]bool)

	ix := newRubyMixinIndex(nodes, edges)
	if len(ix.ancestors) == 0 && len(ix.helperMethods) == 0 {
		return nil, resolved, nil, nil
	}

	// Ledger order is input order, but the refs themselves are sorted so the
	// edge list does not depend on how the parse phase interleaved files.
	refs := make([]graph.UnresolvedRef, 0, 64)
	for _, u := range allUnresolved {
		// DC.12: a `.erb` view's virtualRuby content (extractRubyVariables)
		// now ledgers its own bare/self call sites as call_ref the same way a
		// `.rb` method body does — isRubyFile itself stays .rb/.rake-only
		// since it also gates raw-source re-parse passes that cannot parse
		// ERB markup, but this filter only reads already-emitted refs.
		if u.Kind == "call_ref" && (isRubyFile(u.File) || isERBFile(u.File)) {
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
		e, u := ix.emit(ref, seen)
		if len(e) == 0 {
			continue
		}
		newEdges = append(newEdges, e...)
		unresolvedOut = append(unresolvedOut, u...)
		resolved[RubyCallRefKey(ref.File, ref.Line, ref.Name)] = true
	}
	// DC.12: a view_helper edge's From is a view's NodeTypeFile node, minted
	// on demand by ix.fileIdx (see emitViewHelperCall) rather than assumed —
	// "ensure_scanned_files" mints that same node for every file, but runs
	// long after this pass, too late for this pass's own edge write.
	newEdges = append(newEdges, ix.fileIdx.mintedEdges...)
	return newEdges, resolved, unresolvedOut, ix.fileIdx.minted
}

// RubyCallRefKey identifies one call site. Keyed by line as well as name,
// unlike the JS global pass: a Ruby file routinely calls a name this pass can
// resolve from one class and the same name from another class in the same file
// that does not mix the module in, and only the first is explained.
func RubyCallRefKey(file string, line int, name string) string {
	return file + "\x00" + strconv.Itoa(line) + "\x00" + name
}

// ---------------------------------------------------------------------------
// index
// ---------------------------------------------------------------------------

// scopeSpan is a class body or method body: a node and the line range it covers.
type scopeSpan struct {
	id    string
	label string
	svc   string
	start int
	end   int
}

type rubyMixinIndex struct {
	classSpans    map[string][]scopeSpan       // file → class bodies, innermost-last
	funcSpans     map[string][]scopeSpan       // file → method bodies, innermost-last
	ancestors     map[string][]string          // classID → direct ancestor classIDs, sorted
	methods       map[string][]string          // classID + "\x00" + name → method node IDs
	serviceOf     map[string]string            // node ID → service
	helperMethods map[string]map[string][]string // service → method name → method node IDs, from every app/helpers/*.rb module
	fileIdx       *fileNodeIndex               // mints the NodeTypeFile node a view_helper edge's From needs

	// byNameService is DC.21's fallback index: service → method name → owning
	// method node IDs, flattened across every class in the service (not keyed
	// by classID the way `methods` is). Only consulted when ix.lookup's
	// ancestor walk finds nothing — a bare call with no receiver and no
	// mixin/inherits path to a definition is only safely resolvable when this
	// map has exactly one candidate for the name; two or more is the same
	// "ambiguous → don't guess" discipline resolveBareCall already applies
	// same-file (see emitBareCallFallback).
	byNameService map[string]map[string][]string
}

// isRubyHelperFile matches Rails' `app/helpers/**/*.rb` convention: every
// module declared there is auto-included into every view's `self` (see
// ActionView::Helpers), independent of any explicit `include`/inherits edge —
// which is why this can't reuse the `ancestors` walk built from `inherits`
// edges the same way a class body's mixins can.
func isRubyHelperFile(file string) bool {
	return isRubyFile(file) && (strings.Contains(file, "/helpers/") || strings.HasPrefix(file, "helpers/"))
}

func newRubyMixinIndex(nodes []graph.Node, edges []graph.Edge) *rubyMixinIndex {
	ix := &rubyMixinIndex{
		classSpans:    map[string][]scopeSpan{},
		funcSpans:     map[string][]scopeSpan{},
		ancestors:     map[string][]string{},
		methods:       map[string][]string{},
		serviceOf:     map[string]string{},
		helperMethods: map[string]map[string][]string{},
		fileIdx:       newFileNodeIndex(nodes),
		byNameService: map[string]map[string][]string{},
	}

	// classByFileLabel resolves a function node's Meta["class"] to the class
	// node that declares it. Keyed by file because a simple name is only unique
	// within one, and a reopened module in another file is a different node with
	// its own methods — which is correct: the ancestor edge points at whichever
	// declaration the `include` resolved to.
	classByFileLabel := map[string][]scopeSpan{}

	for i := range nodes {
		n := &nodes[i]
		if n.Language != "ruby" {
			continue
		}
		end := n.Line
		if v, err := strconv.Atoi(n.Meta["end_line"]); err == nil && v > end {
			end = v
		}
		span := scopeSpan{id: n.ID, label: n.Label, svc: n.Service, start: n.Line, end: end}
		switch n.Type {
		case graph.NodeTypeClass:
			ix.classSpans[n.File] = append(ix.classSpans[n.File], span)
			classByFileLabel[n.File+"\x00"+n.Label] = append(classByFileLabel[n.File+"\x00"+n.Label], span)
			ix.serviceOf[n.ID] = n.Service
		case graph.NodeTypeFunction:
			ix.funcSpans[n.File] = append(ix.funcSpans[n.File], span)
			ix.serviceOf[n.ID] = n.Service
		}
	}

	// Method ownership. Meta["class"] names the declaring class; the line range
	// disambiguates a file that declares the same simple name twice.
	for i := range nodes {
		n := &nodes[i]
		if n.Language != "ruby" || n.Type != graph.NodeTypeFunction || n.Meta["class"] == "" {
			continue
		}
		for _, cls := range classByFileLabel[n.File+"\x00"+n.Meta["class"]] {
			if n.Line < cls.start || n.Line > cls.end {
				continue
			}
			key := cls.id + "\x00" + n.Label
			ix.methods[key] = append(ix.methods[key], n.ID)
		}

		byName := ix.byNameService[n.Service]
		if byName == nil {
			byName = map[string][]string{}
			ix.byNameService[n.Service] = byName
		}
		byName[n.Label] = append(byName[n.Label], n.ID)

		if isRubyHelperFile(n.File) {
			byName := ix.helperMethods[n.Service]
			if byName == nil {
				byName = map[string][]string{}
				ix.helperMethods[n.Service] = byName
			}
			byName[n.Label] = append(byName[n.Label], n.ID)
		}
	}

	for i := range edges {
		e := &edges[i]
		if e.Type != graph.EdgeTypeInherits {
			continue
		}
		// Only ancestors this index knows as Ruby classes; a JS `extends` edge
		// shares the type.
		if _, ok := ix.serviceOf[e.To]; !ok {
			continue
		}
		if _, ok := ix.serviceOf[e.From]; !ok {
			continue
		}
		ix.ancestors[e.From] = append(ix.ancestors[e.From], e.To)
	}

	// bug-class #2: map iteration must never reach output.
	for _, spans := range ix.classSpans {
		sortSpans(spans)
	}
	for _, spans := range ix.funcSpans {
		sortSpans(spans)
	}
	for id := range ix.ancestors {
		ix.ancestors[id] = sortedUnique(ix.ancestors[id])
	}
	for key := range ix.methods {
		ix.methods[key] = sortedUnique(ix.methods[key])
	}
	for _, byName := range ix.byNameService {
		for name := range byName {
			byName[name] = sortedUnique(byName[name])
		}
	}
	return ix
}

func sortSpans(spans []scopeSpan) {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		if spans[i].end != spans[j].end {
			return spans[i].end > spans[j].end
		}
		return spans[i].id < spans[j].id
	})
}

func sortedUnique(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// innermost returns the tightest span containing line, or false.
func innermost(spans []scopeSpan, line int) (scopeSpan, bool) {
	var best scopeSpan
	found := false
	for _, s := range spans {
		if line < s.start || line > s.end {
			continue
		}
		if !found || s.start > best.start || (s.start == best.start && s.end < best.end) {
			best, found = s, true
		}
	}
	return best, found
}

// rubyMixinMaxDepth bounds the ancestor walk. Rails stacks concerns several
// deep (a controller includes a concern that includes another), but nothing
// legitimate runs past this, and a cycle in a reconstructed chain must not hang
// the index.
const rubyMixinMaxDepth = 12

// ---------------------------------------------------------------------------
// resolve
// ---------------------------------------------------------------------------

func (ix *rubyMixinIndex) emit(ref graph.UnresolvedRef, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	cls, ok := innermost(ix.classSpans[ref.File], ref.Line)
	if !ok {
		if isERBFile(ref.File) {
			return ix.emitViewHelperCall(ref, seen)
		}
		return nil, nil
	}

	// The caller. A call in a class body rather than a method — `include`d DSL
	// at load time — is attributed to the class, which is where it runs.
	fromID := cls.id
	if fn, ok := innermost(ix.funcSpans[ref.File], ref.Line); ok {
		fromID = fn.id
	}

	targets, depth := ix.lookup(cls.id, ref.Name)
	if len(targets) == 0 {
		return ix.emitBareCallFallback(ref, fromID, seen)
	}

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	for _, to := range targets {
		// Asserted, not assumed: see the vendored-copy note in the doc comment.
		if ix.serviceOf[fromID] != ix.serviceOf[to] {
			continue
		}
		id := fmt.Sprintf("calls:%s->%s", fromID, to)
		if seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]string{
			"via":   "mixin_method",
			"depth": strconv.Itoa(depth),
		}
		if len(targets) > 1 {
			meta["ambiguous"] = "true"
		}
		edges = append(edges, graph.Edge{
			ID:         id,
			From:       fromID,
			To:         to,
			Type:       graph.EdgeTypeCalls,
			Confidence: graph.ConfidenceInferred,
			Meta:       meta,
		})
	}
	if len(edges) > 0 && len(targets) > 1 {
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: ref.Service, File: ref.File, Line: ref.Line,
			Name: ref.Name, Kind: "mixin_method_collision",
		})
	}
	return edges, unresolved
}

// emitBareCallFallback is DC.21: the class this ref belongs to has no
// ancestor-reachable definition of ref.Name (ix.lookup already came back
// empty), which is exactly the gap Tier BC's per-file `methodsByName` left —
// a private/protected method called bare from an unrelated class, or the
// same class reopened in a different file (a reopened module is a distinct
// node per file, so depth-0 of the ancestor walk does not join them). This
// is the last resort: a flat, service-wide name lookup, safe only when it
// finds exactly one candidate — two or more is genuinely ambiguous (Ruby's
// bare-call resolution has no cross-class ordering to break the tie with),
// so it is left unresolved rather than guessed, same as the same-file
// `methodsByName` fallback in resolveBareCall.
func (ix *rubyMixinIndex) emitBareCallFallback(ref graph.UnresolvedRef, fromID string, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	candidates := ix.byNameService[ref.Service][ref.Name]
	if len(candidates) != 1 {
		return nil, nil
	}
	to := candidates[0]
	if to == fromID {
		return nil, nil
	}
	id := fmt.Sprintf("calls:%s->%s", fromID, to)
	if seen[id] {
		return nil, nil
	}
	seen[id] = true
	return []graph.Edge{{
		ID:         id,
		From:       fromID,
		To:         to,
		Type:       graph.EdgeTypeCalls,
		Confidence: graph.ConfidenceInferred,
		Meta:       map[string]string{"via": "bare_call_cross_class"},
	}}, nil
}

// emitViewHelperCall resolves a call_ref ledgered from a `.erb` view's
// top-level scope (DC.12): a view has no enclosing class, so ix.lookup's
// ancestor walk does not apply — instead every `app/helpers/*.rb` module in
// the service is implicitly mixed into every view (ActionView::Helpers), so
// resolution is a flat name lookup against helperMethods rather than a
// depth-ranked ancestor walk. The view itself has no method/class node to
// attribute the call from; the deterministic NodeTypeFile ID
// (file_nodes.go's `ensure` convention) stands in, since EnsureAllScannedFiles
// mints that same node for every scanned file regardless of this pass's own
// ordering in the link pipeline.
func (ix *rubyMixinIndex) emitViewHelperCall(ref graph.UnresolvedRef, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	targets := ix.helperMethods[ref.Service][ref.Name]
	if len(targets) == 0 {
		return nil, nil
	}
	fromID := ix.fileIdx.ensure(ref.Service, ref.File)

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	for _, to := range sortedUnique(append([]string{}, targets...)) {
		id := fmt.Sprintf("calls:%s->%s", fromID, to)
		if seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]string{"via": "view_helper"}
		if len(targets) > 1 {
			meta["ambiguous"] = "true"
		}
		edges = append(edges, graph.Edge{
			ID:         id,
			From:       fromID,
			To:         to,
			Type:       graph.EdgeTypeCalls,
			Confidence: graph.ConfidenceInferred,
			Meta:       meta,
		})
	}
	if len(edges) > 0 && len(targets) > 1 {
		unresolved = append(unresolved, graph.UnresolvedRef{
			Service: ref.Service, File: ref.File, Line: ref.Line,
			Name: ref.Name, Kind: "mixin_method_collision",
		})
	}
	return edges, unresolved
}

// lookup walks the ancestor chain breadth-first and returns every definition of
// name at the shallowest depth that has one. Depth 0 is the class itself, which
// the parser already resolved in-file — it is included so a call in a subclass
// method body to a method of the same class does not fall through to a mixin
// that happens to define the name too.
func (ix *rubyMixinIndex) lookup(classID, name string) ([]string, int) {
	visited := map[string]bool{classID: true}
	frontier := []string{classID}

	for depth := 0; depth <= rubyMixinMaxDepth && len(frontier) > 0; depth++ {
		var hits []string
		for _, id := range frontier {
			hits = append(hits, ix.methods[id+"\x00"+name]...)
		}
		if len(hits) > 0 {
			return sortedUnique(hits), depth
		}
		var next []string
		for _, id := range frontier {
			for _, anc := range ix.ancestors[id] {
				if visited[anc] {
					continue
				}
				visited[anc] = true
				next = append(next, anc)
			}
		}
		sort.Strings(next)
		frontier = next
	}
	return nil, 0
}
