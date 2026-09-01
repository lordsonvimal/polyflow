package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FoldDOMSelectors collapses read-only jQuery / querySelector "selector nodes"
// into a direct edge from the calling function to the DOM element they name.
//
// A dom_target node for a bare selector — $("#categories"),
// querySelector(".x"), getElementById("y") — with no event handler carries no
// information of its own: its only edges are an inbound dom_read/dom_write from
// the enclosing scope and an outbound defined_in to the element(s) it resolves
// to. It sits between the two as a node labelled after the selector, of which
// orion alone has ~1220. Replacing each with a direct
//
//	caller --dom_read--> element
//
// edge keeps the behavioural fact ("this function touches #categories") while
// removing the pivot node that clutters the canvas and floods search.
//
// Event bindings ($(sel).on("click", fn), delegation, shorthand) keep their
// node — the handler and the dom_listen edge hang off it.
//
// Returns the redirect edges to add and the set of dom_target node IDs to
// delete. The caller runs deleteNodes, which cascades removal of each folded
// node's own inbound dom_read/dom_write and outbound defined_in edges.
func FoldDOMSelectors(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, map[string]bool) {
	definedIn := map[string][]string{}   // domTargetID -> []elementID
	inbound := map[string][]graph.Edge{} // domTargetID -> inbound dom_read/dom_write edges
	hasListen := map[string]bool{}       // node touched by a dom_listen edge (either end)
	for _, e := range edges {
		switch e.Type {
		case graph.EdgeTypeDefinedIn:
			definedIn[e.From] = append(definedIn[e.From], e.To)
		case graph.EdgeTypeDOMRead, graph.EdgeTypeDOMWrite:
			inbound[e.To] = append(inbound[e.To], e)
		case graph.EdgeTypeDOMListen:
			hasListen[e.From] = true
			hasListen[e.To] = true
		}
	}

	byID := make(map[string]*graph.Node, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	remove := map[string]bool{}
	var add []graph.Edge
	seen := map[string]bool{}

	ids := make([]string, 0, len(inbound))
	for id := range inbound {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		n := byID[id]
		if n == nil || n.Type != graph.NodeTypeDOMTarget {
			continue
		}
		if !isFoldableSelectorPattern(n.Meta["pattern"]) {
			continue
		}
		if n.Meta["handler_node"] != "" || n.Meta["event"] != "" || hasListen[id] {
			continue
		}
		elems := definedIn[id]
		if len(elems) == 0 {
			continue // unresolved selector — keep the node so the miss stays visible
		}
		sort.Strings(elems)
		for _, in := range inbound[id] {
			for _, elemID := range elems {
				eid := fmt.Sprintf("%s:%s->%s", string(in.Type), in.From, elemID)
				if seen[eid] {
					continue
				}
				seen[eid] = true
				add = append(add, graph.Edge{
					ID:         eid,
					From:       in.From,
					To:         elemID,
					Type:       in.Type,
					Confidence: graph.ConfidenceStatic,
					Meta:       map[string]string{"via": "dom_selector_fold", "selector": n.Meta["selector"]},
				})
			}
		}
		remove[id] = true
	}
	return add, remove
}

func isFoldableSelectorPattern(p string) bool {
	return strings.HasPrefix(p, "jquery_selector") ||
		strings.HasPrefix(p, "dom_access") ||
		strings.HasPrefix(p, "query_selector") ||
		strings.HasPrefix(p, "get_element")
}

// PruneOrphanElements deletes element nodes that nothing in the graph
// references — no selector defined_in, no dom_listen, no dom_contract, no
// folded dom_read/dom_write. The parser mints an element node for every id= or
// class= attribute and every top-level CSS rule; on a real Rails + jQuery app
// the majority (orion: 3998 of 6155) are markup no JS ever selects, which is
// pure canvas and search noise.
//
// Runs after all DOM linking and after FoldDOMSelectors (which adds the
// dom_read/dom_write edges that rescue the elements a selector actually names),
// and before the containment pass — so an "orphan" is simply a node with zero
// edges.
func PruneOrphanElements(nodes []graph.Node, edges []graph.Edge) map[string]bool {
	touched := make(map[string]bool, len(edges)*2)
	for _, e := range edges {
		touched[e.From] = true
		touched[e.To] = true
	}
	remove := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeElement && !touched[n.ID] {
			remove[n.ID] = true
		}
	}
	return remove
}
