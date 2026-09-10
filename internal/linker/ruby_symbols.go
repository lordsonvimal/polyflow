package linker

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

// symbolName / symbolList were rails_filters.go helpers; rails_devise.go still
// uses them (the filter chain itself is Tier FX now — patterns/ruby/
// rails_filters.yaml). They stay here, unchanged.

func symbolName(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), ":") }

// symbolList reads `:create`, `[:create, :update]` and `%i[create update]`.
func symbolList(n *sitter.Node, src []byte) []string {
	if n == nil {
		return nil
	}
	switch n.Type() {
	case "simple_symbol":
		return []string{symbolName(n.Content(src))}
	case "array", "symbol_array":
		var out []string
		for i := 0; i < int(n.NamedChildCount()); i++ {
			ch := n.NamedChild(i)
			switch ch.Type() {
			case "simple_symbol":
				out = append(out, symbolName(ch.Content(src)))
			case "bare_symbol", "string": // %i[…] elements
				out = append(out, symbolName(strings.Trim(ch.Content(src), `"'`)))
			}
		}
		return out
	}
	return nil
}
