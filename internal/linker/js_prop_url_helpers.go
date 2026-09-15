package linker

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// js_prop_url_helpers.go is what remains of internal/linker/react_prop_urls.go
// after its own pass (LinkReactPropURLs) migrated to Tier FX
// (patterns/generic/react_prop_urls.yaml + internal/factpipe/
// hub_react_prop_urls.go, which ported these three functions' logic
// independently rather than importing them — internal/factpipe cannot import
// internal/linker). jsDestructuresName/jsWrapperMethod/resolveJSPropURL are
// shared by OTHER linker passes (js_prop_urls.go, js_wrapper_calls.go,
// valuegraph_adapter.go) that were never part of the migration, so this file
// stays — trimmed and renamed off "react_prop_urls" — the same shape as
// ruby_http_hosts.go's rename to ruby_host_registry.go.

// jsDestructuresName reports whether name appears as a key in any object
// destructuring pattern in the tree (`({ name }) => …`, `const { name } = x`).
func jsDestructuresName(root *sitter.Node, src []byte, name string) bool {
	found := false
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		if n.Type() == "object_pattern" {
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				switch c.Type() {
				case "shorthand_property_identifier_pattern":
					if c.Content(src) == name {
						found = true
						return
					}
				case "pair_pattern":
					if k := c.ChildByFieldName("key"); k != nil && k.Content(src) == name {
						found = true
						return
					}
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

// jsWrapperMethod maps an api-wrapper name to its HTTP verb
// (`apiPost` → POST, `apiDelete` → DELETE, `get` → GET).
func jsWrapperMethod(w string) string {
	l := strings.ToLower(w)
	l = strings.TrimPrefix(l, "api")
	switch l {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return strings.ToUpper(l)
	case "del":
		return "DELETE"
	}
	return ""
}

var reTemplateSubst = regexp.MustCompile(`\$\{[^{}]*\}`)

// resolveJSPropURL turns a JS string / template-literal source into a
// root-relative wildcard path: `"/app/lro/5"` → `/app/lro/5`,
// “ `/app/lro/${id}?study_id=${s}` “ → `/app/lro/*`. Anything not starting
// with a literal `/` (a bare identifier, an interpolation-led template) fails.
func resolveJSPropURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != raw[len(raw)-1] {
		return "", false
	}
	var body string
	switch raw[0] {
	case '"', '\'':
		body = raw[1 : len(raw)-1]
	case '`':
		body = reTemplateSubst.ReplaceAllString(raw[1:len(raw)-1], "*")
	default:
		return "", false
	}
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	if i := strings.IndexAny(body, "?#"); i >= 0 {
		body = body[:i]
	}
	segs := strings.Split(body, "/")
	for i, s := range segs {
		if strings.Contains(s, "*") {
			segs[i] = "*"
		}
	}
	body = strings.TrimRight(strings.Join(segs, "/"), "/")
	if body == "" {
		return "", false
	}
	return body, true
}
