package linker

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// attrDef is one producer definition for a stable-selector attribute value.
type attrDef struct {
	value    string // static value, or (if isPrefix) the known-static prefix
	isPrefix bool   // true when value is a prefix — the runtime suffix is unknown
	compID   string // templ component node ID that declares the attribute
}

// reAttrSelectorToken finds `[attr="value"]`-shaped tokens anywhere inside a
// (possibly compound/descendant) CSS selector string, including inside a JS
// template literal, so “ `[data-color="${c}"] [data-testid="btn-${p}"]` “
// yields two independent tokens. `${…}` interpolation is left in the
// captured value; splitAtInterpolation trims it to a static prefix.
var reAttrSelectorToken = regexp.MustCompile(`\[\s*([a-zA-Z][\w-]*)\s*=\s*"([^"]*)"\s*\]`)

// LinkDOMContracts links stable-selector data attributes declared by templ
// producers (data-testid, id, other data-*) to the JS
// querySelector/getElementById sites that read them via a matching attribute
// selector — the producer -> consumer "DOM contract" edge (IA.5). Unlike
// defined_in (JS -> templ, id=/class= equality only, and via a minted
// element node), this runs directly component -> consumer, so
// investigate/walkFlows reach the JS clone/read site in the single hop out
// of the rendering component that resolveNode already landed on — no
// disconnected intermediate node to traverse through.
//
// `${…}` (JS template literal) and Go string-concatenation (`"prefix-" +
// expr`) values are normalised to their static prefix and matched by
// strings.HasPrefix — a known literal on either side is enough to resolve an
// unknown suffix on the other. A fully dynamic token (no static prefix at
// all, e.g. `[data-color="${c}"]`) carries no signal and is skipped rather
// than matched against every producer for that attribute. A selector with no
// token that resolves anywhere is surfaced in unresolved (kind
// "dom_contract_ref") instead of silently dropped; a selector with at least
// one resolving token is not flagged even if other tokens in the same
// compound selector go unmatched — recall over precision (per
// [[polyflow-plan-14-agent-trust]]: no phantom edges, but no silent drops
// either).
func LinkDOMContracts(nodes []graph.Node) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	attrDefs := map[string][]attrDef{} // "svc\x00attr" -> defs

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeComponent || n.Language != "templ" {
			continue
		}
		for _, entry := range strings.Split(n.Meta["dom_data_attrs"], "\n") {
			attr, value, isPrefix, ok := splitAttrEntry(entry)
			if !ok {
				continue
			}
			key := n.Service + "\x00" + attr
			attrDefs[key] = append(attrDefs[key], attrDef{value: value, isPrefix: isPrefix, compID: n.ID})
		}
		// id="…" is a stable selector attribute too (`[id="…"]`); reuse the
		// existing dom_ids capture (defined_in's producer index) instead of
		// duplicating it under dom_data_attrs.
		for _, entry := range strings.Split(n.Meta["dom_ids"], "\n") {
			id, _ := splitIDLine(entry)
			if id == "" {
				continue
			}
			key := n.Service + "\x00id"
			attrDefs[key] = append(attrDefs[key], attrDef{value: id, compID: n.ID})
		}
	}

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	seenEdge := map[string]bool{}

	addEdge := func(fromID, toID, conf string) {
		edgeID := fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeDOMContract), fromID, toID)
		if seenEdge[edgeID] {
			return
		}
		seenEdge[edgeID] = true
		edges = append(edges, graph.Edge{
			ID: edgeID, From: fromID, To: toID,
			Type: graph.EdgeTypeDOMContract, Confidence: conf,
		})
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget {
			continue
		}
		raw := n.Meta["selector"]
		if raw == "" || !strings.Contains(raw, "[") {
			continue
		}
		tokens := reAttrSelectorToken.FindAllStringSubmatch(raw, -1)
		if len(tokens) == 0 {
			continue
		}
		resolvedAny := false
		for _, tok := range tokens {
			attr := strings.ToLower(tok[1])
			prefix, hasDynamic := splitAtInterpolation(tok[2])
			if prefix == "" && hasDynamic {
				continue // fully dynamic value, no static signal to match on
			}
			for _, d := range attrDefs[n.Service+"\x00"+attr] {
				if !attrMatches(d, prefix, hasDynamic) {
					continue
				}
				resolvedAny = true
				conf := graph.ConfidencePartial
				if !d.isPrefix && !hasDynamic {
					conf = graph.ConfidenceStatic // both sides fully static: exact match
				}
				addEdge(d.compID, n.ID, conf)
			}
		}
		if !resolvedAny {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: stripQuote(raw), Kind: "dom_contract_ref",
			})
		}
	}
	return nil, edges, unresolved
}

// splitAttrEntry parses one "attr=value@line" or "attr=value*@line"
// dom_data_attrs entry ("*" immediately before "@" marks value as a known-
// static prefix, not the attribute's full runtime value). The line suffix is
// discarded — the edge attributes to the component, not a per-attribute line.
func splitAttrEntry(entry string) (attr, value string, isPrefix bool, ok bool) {
	eq := strings.IndexByte(entry, '=')
	if eq < 0 {
		return "", "", false, false
	}
	attr = entry[:eq]
	v, _ := splitIDLine(entry[eq+1:])
	if v == "" {
		return "", "", false, false
	}
	if strings.HasSuffix(v, "*") {
		return attr, strings.TrimSuffix(v, "*"), true, true
	}
	return attr, v, false, true
}

// splitAtInterpolation splits a JS template-literal attribute value at its
// first `${`: "promotion-button-${move.promotion}" -> ("promotion-button-",
// true). A value with no interpolation is returned whole, hasDynamic=false.
func splitAtInterpolation(val string) (prefix string, hasDynamic bool) {
	if i := strings.Index(val, "${"); i >= 0 {
		return val[:i], true
	}
	return val, false
}

// attrMatches decides whether a producer definition satisfies a consumer
// token. Two fully static values must match exactly; if either side is a
// prefix (dynamic suffix), a HasPrefix check in either direction is enough —
// a known literal on one side pins the unknown suffix on the other (recall
// over precision, mirrors the ${…} queue-name normaliser from
// [[polyflow-juniper-crossyield-investigation]] X.10c).
func attrMatches(d attrDef, consumerVal string, consumerDynamic bool) bool {
	if !d.isPrefix && !consumerDynamic {
		return d.value == consumerVal
	}
	return strings.HasPrefix(d.value, consumerVal) || strings.HasPrefix(consumerVal, d.value)
}
