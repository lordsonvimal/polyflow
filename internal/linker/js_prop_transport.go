package linker

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// Tier UB.3 — the transport function crosses a JSX prop.
//
// The mirror image of UB.2. Here the parent owns the transport: a wrapper
// function whose URL is one of its parameters is handed to a child as a prop,
// and the child supplies the argument at its own call site.
//
//	// parent
//	postToServer = (transport, data, updateURL) =>
//	  transport.ajax("Saving...", { url: updateURL, data, method: "PUT" });
//	// ...
//	<Grid postToServer={this.postToServer} />
//
//	// child
//	this.props.postToServer(this.props.transport, data, `/api/things/${id}`);
//
// Tier CW recognises the parent's transport call, finds its URL argument is a
// bare parameter, and ledgers `prop_client_dynamic_url`. This pass supplies the
// missing half: it indexes every JSX attribute whose value references a local
// function, keyed by the referenced symbol, then for each ledger row whose URL
// argument is a parameter it (1) finds which prop carries that function on which
// component, (2) reads the argument at that parameter's index at the child's
// `props.<prop>(…)` call sites, and (3) mints one http_client per distinct
// resolved URL at the parent's transport call — where the request is issued and
// where the ledger row already sits.
//
// Same resolution rule as UB.2: one node per distinct URL, fan-out exactly 1,
// never one node with several http_call edges; above a cap it ledgers instead.
// Argument expressions that are not a literal/template path or a bare identifier
// resolvable in their own function ledger `prop_transport_unresolved`.
//
// Must run AFTER js_prop_urls (it consumes the same ledger, already thinned by
// UB.2) and after js_local_urls (it reuses the local-binding walk).
//
// The pass itself is LinkJSPropTransport in js_prop_crossings.go: since VG.5 the
// indexing and value walk described above are one reverse crossing rule in
// internal/valuegraph/javascript.yaml. What remains here is the shared JS call
// machinery its mint site and UB.2's both use.

const (
	ledgerPropTransportUnresolved = "prop_transport_unresolved"
	ledgerPropTransportHighFanout = "prop_transport_high_fanout"
)

// positionalArg returns the i-th positional argument, or nil if a spread
// element appears at or before that position (which shifts every index).
func positionalArg(args *sitter.Node, i int) *sitter.Node {
	idx := 0
	for c := 0; c < int(args.NamedChildCount()); c++ {
		n := args.NamedChild(c)
		if n.Type() == "comment" {
			continue
		}
		if n.Type() == "spread_element" {
			return nil
		}
		if idx == i {
			return n
		}
		idx++
	}
	return nil
}

// jsParamIndex returns the positional index of the parameter named `name`.
func jsParamIndex(fn *sitter.Node, name string, src []byte) (int, bool) {
	if fn == nil {
		return 0, false
	}
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		if p := fn.ChildByFieldName("parameter"); p != nil && paramName(p, src) == name {
			return 0, true
		}
		return 0, false
	}
	idx := 0
	for i := 0; i < int(params.NamedChildCount()); i++ {
		c := params.NamedChild(i)
		if c.Type() == "comment" {
			continue
		}
		if paramName(c, src) == name {
			return idx, true
		}
		idx++
	}
	return 0, false
}
