package linker

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// Tier UB.2 — the URL crosses a JSX prop.
//
// The parent component computes the endpoint and passes it down as a prop; the
// child performs the request. Tier CW (LinkJSPropClients) recognises the child's
// transport call — the receiver is a prop-injected transport — and finds the URL
// argument unreadable because it is a bare prop identifier, so it ledgers
// `prop_client_dynamic_url` and mints nothing. This pass supplies the missing
// half: it indexes every JSX attribute value in the service, keyed by
// (component, prop name), and joins it back to each ledger site.
//
// It mints one http_client node per DISTINCT resolved URL rather than one node
// with several http_call edges — a reusable modal genuinely has many endpoints,
// and Tier UL settled that shape: no single node may reach more than one
// handler. This deliberately differs from LinkReactPropURLs, which abstains when
// a prop resolves to two different paths across render sites. For the
// Rails→React boundary a prop disagreement usually means the resolver bound the
// wrong component; for the JSX→JSX boundary, disagreement is the normal and
// correct situation (one modal, many create URLs).
//
// Must run AFTER js_prop_clients (it consumes that pass's ledger) and after
// js_local_urls (it reuses the local-binding resolution for producer values).
// Producer values that are builder calls, member expressions or server-supplied
// (`schema.create_url`) are NOT resolved here — they ledger `prop_url_unresolved`
// and are Tier MS.3 / SPA.5 territory.
//
// The pass itself is LinkJSPropURLs in js_prop_crossings.go: since VG.5 the
// producer index and value walk described above are one forward crossing rule in
// internal/valuegraph/javascript.yaml. What remains here is the shared JSX and
// call machinery its mint site and UB.3's both use.

const (
	ledgerPropURLUnresolved = "prop_url_unresolved"
	ledgerPropURLHighFanout = "prop_url_high_fanout"

	// maxPropURLFanout caps how many distinct URLs one consumer prop may
	// resolve to. Cedar's widest genuine case is 18 (a create modal rendered
	// from 18 sites); 24 is slack above the observed maximum rather than a
	// working limit. A component above it is a generic transport, not a feature.
	maxPropURLFanout = 24
)

// PropURLRetractKey is the key LinkJSPropURLs' retract set uses and the caller
// must reconstruct to drop a now-resolved prop_client_dynamic_url row.
func PropURLRetractKey(file string, line int) string {
	return fmt.Sprintf("%s\x00%d", file, line)
}

// findPropClientCallAtLine returns the first `<recv>.<method>(...)` call
// starting on line whose method name is one a prop-client may expose.
func findPropClientCallAtLine(root *sitter.Node, src []byte, line int) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		if n.Type() == "call_expression" && int(n.StartPoint().Row)+1 == line {
			if callee := n.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
				if pr := callee.ChildByFieldName("property"); pr != nil && propClientMethodNames[pr.Content(src)] {
					found = n
					return
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return found
}

// propURLConsumerProp returns the bare prop identifier a call reads its URL
// from: a positional identifier argument, or the `url` key / shorthand of an
// options object argument.
func propURLConsumerProp(call *sitter.Node, src []byte) string {
	n := propURLConsumerPropNode(call, src)
	if n == nil {
		return ""
	}
	return n.Content(src)
}

// propURLConsumerPropNode is propURLConsumerProp's answer as the node itself,
// which is what the value engine resolves (VG.4). Which argument is the URL is
// recognition and stays here; what that argument can be is the engine's
// question.
func propURLConsumerPropNode(call *sitter.Node, src []byte) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch a.Type() {
		case "identifier":
			return a
		case "object":
			if v := jsObjectKeyValue(a, src, "url"); v != nil {
				switch v.Type() {
				case "identifier", "shorthand_property_identifier", "property_identifier":
					return v
				}
			}
		}
	}
	return nil
}

// fileHasProp reports whether name is read as a prop somewhere in the file:
// `this.props.name`, `const { name } = this.props`, or a `{ name }` destructure
// in a function-component parameter.
func fileHasProp(root *sitter.Node, src []byte, name string) bool {
	if collectPropsBindings(root, src)[name] {
		return true
	}
	return jsDestructuresName(root, src, name)
}

// propURLCallVerb derives the HTTP verb from the transport call: the method name
// (`.post` → POST) or, for `.ajax`/`.request`, the `method`/`type` key of an
// options object argument. Defaults to GET.
func propURLCallVerb(call *sitter.Node, src []byte) string {
	if callee := call.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
		if pr := callee.ChildByFieldName("property"); pr != nil {
			if v := propClientVerb(pr.Content(src)); v != "" {
				return v
			}
		}
	}
	if args := call.ChildByFieldName("arguments"); args != nil {
		for i := 0; i < int(args.NamedChildCount()); i++ {
			a := args.NamedChild(i)
			if a.Type() != "object" {
				continue
			}
			for _, k := range []string{"method", "type"} {
				if vn := jsObjectKeyValue(a, src, k); vn != nil && vn.Type() == "string" {
					return strings.ToUpper(strings.Trim(vn.Content(src), `"'`))
				}
			}
		}
	}
	return "GET"
}

// jsEnclosingFnName returns the name of the nearest enclosing function, method,
// or arrow bound to a declarator/field — for the `calls` edge lookup.
func jsEnclosingFnName(n *sitter.Node, src []byte) string {
	fn := enclosingJSFunction(n)
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "function_declaration", "generator_function_declaration", "method_definition":
		if nm := fn.ChildByFieldName("name"); nm != nil {
			return nm.Content(src)
		}
	}
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "variable_declarator", "public_field_definition", "field_definition":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		case "function_declaration", "method_definition":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		}
	}
	return ""
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
