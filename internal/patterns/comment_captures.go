package patterns

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Comments are "extras" in every tree-sitter grammar we load: they are named
// nodes that may appear between any two siblings. That makes them invisible to
// a human reading a query but very visible to the `.` anchor, which means
// "immediately-adjacent *named* sibling" — a comment counts. So a query like
//
//	arguments: (argument_list . (_) @ctx . (_) @exchange . (_) @routing_key ...)
//
// silently binds a trailing `// exchange` comment to a capture as soon as the
// call is written one-argument-per-line with annotations:
//
//	p.channel.PublishWithContext(
//	    ctx,
//	    p.exchange,  // exchange
//	    routingKey,  // routing key
//	    ...
//
// Every capture after the first comment shifts by one, so `routing_key` becomes
// the literal text "// exchange". Thirty-plus pattern files across five
// languages use anchors this way, so this is repaired centrally rather than by
// hand-hardening each query (tree-sitter has no "skip extras" anchor).
//
// The repair is positional: the anchored captures were meant to name N
// consecutive named siblings, so re-bind them to the parent's N consecutive
// *non-comment* named children starting at the first capture's slot.

// isCommentNode reports whether n is a comment in any grammar we load. Comments
// are declared as extras, so IsExtra covers the common case; the type checks
// catch grammars that also allow a comment in a concrete production.
func isCommentNode(n *sitter.Node) bool {
	if n == nil {
		return false
	}
	if n.IsExtra() {
		return true
	}
	t := n.Type()
	return t == "comment" || strings.HasSuffix(t, "_comment")
}

// repairCommentCaptures re-aligns captures that an interleaved comment shifted.
//
// It returns the corrected capture list and whether the match is usable. A
// match is dropped only when a capture that contributes text (one whose name is
// not underscore-prefixed) still binds a comment after the repair, since such a
// capture would otherwise become node meta reading "// exchange".
func repairCommentCaptures(caps []sitter.QueryCapture, nameFor func(uint32) string) ([]sitter.QueryCapture, bool) {
	shifted := false
	for i := range caps {
		if isCommentNode(caps[i].Node) {
			shifted = true
			break
		}
	}
	if !shifted {
		return caps, true
	}

	out := make([]sitter.QueryCapture, len(caps))
	copy(out, caps)

	// Group the captures by parent: only siblings of the intruding comment were
	// shifted, and only they get re-bound.
	byParent := map[uintptr][]int{}
	parents := map[uintptr]*sitter.Node{}
	for i := range out {
		p := out[i].Node.Parent()
		if p == nil {
			continue
		}
		byParent[p.ID()] = append(byParent[p.ID()], i)
		parents[p.ID()] = p
	}

	for id, idxs := range byParent {
		hasComment := false
		for _, i := range idxs {
			if isCommentNode(out[i].Node) {
				hasComment = true
				break
			}
		}
		if !hasComment {
			continue
		}
		parent := parents[id]

		// The parent's named children with comments removed: the sequence the
		// query author believed they were anchoring across.
		var slots []*sitter.Node
		for i := 0; i < int(parent.NamedChildCount()); i++ {
			if c := parent.NamedChild(i); !isCommentNode(c) {
				slots = append(slots, c)
			}
		}

		sort.SliceStable(idxs, func(a, b int) bool {
			return out[idxs[a]].Node.StartByte() < out[idxs[b]].Node.StartByte()
		})

		// Anchor on the first capture in the group. If it is itself a comment we
		// have no fixed point to count from and leave the group alone.
		start := -1
		for s, c := range slots {
			if c.Equal(out[idxs[0]].Node) {
				start = s
				break
			}
		}
		if start < 0 {
			continue
		}
		if start+len(idxs) > len(slots) {
			// More captures than real arguments: the match spanned comments that
			// no argument backs, so there is nothing to re-bind to.
			continue
		}
		for k, i := range idxs {
			out[i].Node = slots[start+k]
		}
	}

	for i := range out {
		if isCommentNode(out[i].Node) && !strings.HasPrefix(nameFor(out[i].Index), "_") {
			return nil, false
		}
	}
	return out, true
}
