package graph

import "fmt"

// UnresolvedInFiles scopes the blind-spot ledger to a traversal: it returns
// the refs whose file appears in the given set, preserving input order. The
// result is never nil — an empty slice encodes as [] so agents can tell
// "no known blind spots here" apart from "section missing".
func UnresolvedInFiles(refs []UnresolvedRef, files map[string]bool) []UnresolvedRef {
	out := make([]UnresolvedRef, 0)
	for _, r := range refs {
		if files[r.File] {
			out = append(out, r)
		}
	}
	return out
}

// retractableKinds maps a ledger kind to the edge type that, if present,
// proves the reference was resolved after all. Only kinds whose satisfaction
// is unambiguously witnessed by a single edge type appear here: a `job` or
// `config_not_found` ref has no such witness and is left alone.
var retractableKinds = map[string]EdgeType{
	"inherits_unresolved":     EdgeTypeInherits,
	"call_ref":                EdgeTypeCalls,
	"instantiates_unresolved": EdgeTypeInstantiates,
	"import_ref":              EdgeTypeImports,
}

// RetractResolvedRefs drops ledger entries that a later pass went on to
// resolve, returning the surviving refs in input order.
//
// The ledger is written during parsing, when a reference is genuinely
// unresolved; the linkers then run and resolve many of them. Nothing retracted
// the entry, so the ledger accumulated false alarms — on the juniper fleet
// 1324 of 1668 `inherits_unresolved` entries (79%) had a real `inherits` edge
// by the end of the run. `lros_controller.rb:7 ApiBaseController` was listed as
// a blind spot while LrosController -[inherits]-> ApiBaseController sat in the
// edge table.
//
// This is not cosmetic. Every trace and impact answer ends with "verify these N
// unresolved references manually", which is an instruction to an agent to go
// open files. A false alarm there converts directly into wasted tokens — the
// exact cost polyflow exists to remove.
//
// A ref is retracted when some node in its file has an edge of the witnessing
// type to a node with its name. Matching is at file granularity because an edge
// leaves the *enclosing declaration*, whose line is its `def`, not the line of
// the call site the ref recorded — so a line-exact join is not available. That
// is sound for these kinds: the linkers resolve a name per file, not per site,
// so if `foo` resolves from this file at all it resolves at every site in it.
func RetractResolvedRefs(refs []UnresolvedRef, nodes []Node, edges []Edge) []UnresolvedRef {
	if len(refs) == 0 {
		return refs
	}
	byID := make(map[string]*Node, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	// witnessed: service \x00 file \x00 name \x00 edgeType
	witnessed := make(map[string]bool, len(edges))
	for i := range edges {
		e := &edges[i]
		from, ok := byID[e.From]
		if !ok {
			continue
		}
		to, ok := byID[e.To]
		if !ok {
			continue
		}
		witnessed[from.Service+"\x00"+from.File+"\x00"+to.Label+"\x00"+string(e.Type)] = true
	}

	out := make([]UnresolvedRef, 0, len(refs))
	for _, r := range refs {
		if et, ok := retractableKinds[r.Kind]; ok &&
			witnessed[r.Service+"\x00"+r.File+"\x00"+r.Name+"\x00"+string(et)] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// UnresolvedNote renders the agent-facing warning attached to query output
// alongside a non-empty unresolved section. Empty when there is nothing to
// verify.
func UnresolvedNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "verify this 1 unresolved reference manually — the indexer could not resolve it, so edges may be missing from this answer"
	}
	return fmt.Sprintf("verify these %d unresolved references manually — the indexer could not resolve them, so edges may be missing from this answer", n)
}
