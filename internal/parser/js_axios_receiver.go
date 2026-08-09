package parser

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// dropNonHTTPJSMatches discards two families of JavaScript match that produce
// an http_client node with no HTTP behind it. Both are cases where the
// pattern's query carries no HTTP evidence of its own and the compensating
// gate is too coarse to separate two call sites in the same file.
//
//  1. axios_instance.yaml matches `anything.get(x)`, gated only on the
//     *service* depending on axios. So `updatedDetailsMap.get(d.id)`
//     (JobDetailModal.jsx:152) was indexed as an outbound HTTP request — 44 of
//     118 axios_instance_call nodes on this fleet were container operations.
//
//  2. producer_alias.yaml's `producer_alias_url_call` matches any
//     `identifier("literal")` call, expecting EnrichAliases to recognise the
//     identifier as an HTTP alias. When it does not, alias.go:161-164 keeps the
//     node anyway. `useState("")` and `setSyncStatus("")` therefore became
//     HTTP clients: 79 of the fleet's 89 producer-alias nodes had an empty URL.
//
// Filtering MatchResults rather than finished nodes follows
// dropNonRoutesFileRouteMatches: the gate must run before MatchToGraph's pass
// 1b, where an http_client competes with other node kinds at the same
// file:line.
func dropNonHTTPJSMatches(results []patterns.MatchResult) []patterns.MatchResult {
	out := results[:0]
	for _, r := range results {
		switch r.PatternName {
		case "axios_instance_call":
			if isContainerReceiverCall(r) {
				continue
			}
		case "producer_alias_url_call", "producer_alias_obj_call":
			// An empty URL literal is not an address. The node could never
			// match a route, so it is pure search and footer noise — and,
			// being typed http_client, it reads to an agent as a real
			// outbound call from this component.
			if strings.Trim(r.Captures["url"], `"'`+"`") == "" {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func isContainerReceiverCall(r patterns.MatchResult) bool {
	recv := r.Captures["via_alias"]
	if recv == "" || len(r.Src) == 0 {
		return false
	}
	// The URL argument is the only capture whose tree-sitter node the matcher
	// retains, so the receiver is reached by climbing from it to the call.
	arg := r.KeyNodes["url"]
	if arg == nil {
		return false
	}
	recvNode := jsCallReceiver(arg, r.Src, recv)
	if recvNode == nil {
		return false
	}
	return contract.JSReceiverIsLocalContainer(recvNode, r.Src, recv)
}

// jsCallReceiver walks up from a call's argument to the identifier naming the
// call's receiver, confirming it is the expected name before returning it.
func jsCallReceiver(arg *sitter.Node, src []byte, want string) *sitter.Node {
	for cur := arg.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() != "call_expression" {
			continue
		}
		fn := cur.ChildByFieldName("function")
		if fn == nil || fn.Type() != "member_expression" {
			return nil
		}
		obj := fn.ChildByFieldName("object")
		if obj == nil || obj.Type() != "identifier" {
			return nil
		}
		if string(src[obj.StartByte():obj.EndByte()]) != want {
			return nil
		}
		return obj
	}
	return nil
}
