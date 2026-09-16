package linker

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// js_prop_client_shared.go is what remains of internal/linker/js_prop_client.go
// after its own pass (LinkJSPropClients, SPA.4/SPA.5) migrated to Tier FX
// (patterns/generic/schema_url_link.yaml + internal/factpipe/
// hub_schema_url_link.go, which ported these four functions' logic
// independently, sul-prefixed — internal/factpipe cannot import
// internal/linker). paramName/propClientMethodNames/propClientVerb/
// collectPropsBindings are shared by OTHER linker passes (js_prop_urls.go,
// js_prop_transport.go) that were never part of the migration, so this file
// stays — trimmed and renamed off "js_prop_client" — the same shape as
// ruby_http_hosts.go's rename to ruby_host_registry.go and react_prop_urls.go's
// to js_prop_url_helpers.go.

// paramName returns the identifier name of a parameter node (handles
// `required_parameter`/`identifier`/typed forms).
func paramName(p *sitter.Node, src []byte) string {
	if p == nil {
		return ""
	}
	if p.Type() == "identifier" {
		return p.Content(src)
	}
	if pat := p.ChildByFieldName("pattern"); pat != nil && pat.Type() == "identifier" {
		return pat.Content(src)
	}
	for i := 0; i < int(p.NamedChildCount()); i++ {
		if c := p.NamedChild(i); c.Type() == "identifier" {
			return c.Content(src)
		}
	}
	return ""
}

// propClientMethodNames are the method names a prop-client may expose. Kept
// as a gate so a wrapper with an unrelated `compute`/`select` method never
// widens into an HTTP client.
var propClientMethodNames = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
	"del": true, "ajax": true, "request": true, "fetch": true, "head": true,
}

func propClientVerb(name string) string {
	switch name {
	case "get", "post", "put", "patch", "delete", "head":
		return strings.ToUpper(name)
	case "del":
		return "DELETE"
	}
	return ""
}

// collectPropsBindings returns every name that provably originates from
// `this.props` in a file: destructured (`const { a, b } = this.props`) or
// accessed (`this.props.a`).
func collectPropsBindings(root *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "variable_declarator":
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name != nil && name.Type() == "object_pattern" && val != nil &&
				val.Type() == "member_expression" &&
				strings.TrimSpace(val.Content(src)) == "this.props" {
				for i := 0; i < int(name.NamedChildCount()); i++ {
					c := name.NamedChild(i)
					switch c.Type() {
					case "shorthand_property_identifier_pattern", "shorthand_property_identifier":
						out[c.Content(src)] = true
					case "pair_pattern":
						if k := c.ChildByFieldName("key"); k != nil {
							out[k.Content(src)] = true
						}
					}
				}
			}
		case "member_expression":
			inner := n.ChildByFieldName("object")
			prop := n.ChildByFieldName("property")
			if inner != nil && prop != nil && inner.Type() == "member_expression" &&
				strings.TrimSpace(inner.Content(src)) == "this.props" {
				out[prop.Content(src)] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}
