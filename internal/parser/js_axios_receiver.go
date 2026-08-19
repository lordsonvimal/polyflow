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
			// jQuery's global `$`/`jQuery` is never an axios instance, but
			// axios_instance_call's query is any-identifier-shaped
			// (`identifier.get/post/...(url)`) and is gated only at the
			// service-dependency level (patterns/javascript/axios_instance.
			// yaml's `package: axios`), not per call site — a service that
			// depends on axios anywhere still has jQuery elsewhere in its
			// legacy asset pipeline. jquery.yaml's jquery_ajax pattern
			// already matches `$.get(url)`/`$.post(url)` correctly with the
			// real "jquery" package and a jquery_ajax_method role; without
			// this drop, Pass 1b's same-file:line dedup keeps whichever
			// pattern's match happened to land in `nodes` first (alphabetical
			// file order put axios_instance.yaml before jquery.yaml), so the
			// correct jQuery match silently lost to the wrong axios one.
			if r.Captures["via_alias"] == "$" || r.Captures["via_alias"] == "jQuery" {
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
			// producer_alias_url_call's query is `identifier(...string...)` —
			// no gating on the callee name or the string's shape at all, so it
			// matches any bare-identifier call with a string argument anywhere,
			// not just producer/HTTP calls. Confirmed indexing GitNexus's own
			// repo: it() test descriptions ("resolves user.save() in main()...")
			// and plain call arguments (getRelationships(result, 'CALLS')) were
			// captured and, once inside a recognised test-DSL wrapper (X.0),
			// escaped EnrichAliases entirely — demoteTestDSL retypes the node to
			// "function" before alias resolution ever runs, so the "unresolved
			// alias" fallback that (per the empty-URL case above) already keeps
			// bad candidates never gets a chance to drop these either. A real
			// URL or producer topic is a single unspaced token with at least one
			// structural separator (`/api/x`, `user.created`); a test
			// description or bare identifier is not.
			if isNonAddressJSString(r.Captures["url"]) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// isNonAddressJSString reports whether s (a producer_alias_url_call/obj_call
// capture, quotes stripped) is too shapeless to be a URL or producer topic:
// natural-language text (contains a space — every it()/describe() test
// description does) or a bare token with no structural separator at all
// (enum-like constants such as "CALLS", single words like "root"/"java").
// Real addresses always have at least one of / . : and never a raw space.
func isNonAddressJSString(raw string) bool {
	s := strings.Trim(raw, `"'`+"`")
	if s == "" {
		return false // empty already handled by the caller
	}
	if strings.ContainsAny(s, " \t\n") {
		return true
	}
	return !strings.ContainsAny(s, "/.:")
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
