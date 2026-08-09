package parser

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// normalizeHTMLElementMatches cleans the id=/class= captures the html element
// patterns produce, and drops the ones that name nothing.
//
// The ERB parser runs the HTML pass over a *blanked* copy of the template, in
// which every `<%= … %>` tag is replaced byte-for-byte with spaces so line
// numbers survive. That is the right trade for markup, but it turns an
// interpolated attribute value into whitespace, and the attribute value is
// exactly what names the node:
//
//	class="<%= status_class %>"      → "                  "  → label "."
//	class="<%= tone %> cell"         → "            cell"    → label "."
//	id="task-<%= task.id %>"         → "task-           "    → label "#task-"
//
// nextGen carried 59 element nodes labelled `.`, 37 whose label ended in a
// space, and 44 ids containing one. So three different defects, one cause:
//
//   - A value that blanks away entirely names nothing. Dropped.
//   - A value with a literal class *and* an interpolated one is a real element;
//     only the interpolated token is unknowable. Collapsing the whitespace
//     keeps the literal classes — and fixes the label, which took the text
//     before the first space and so came out empty whenever the interpolation
//     came first.
//   - An id is a single token by definition, so whitespace inside one means it
//     was interpolated. `#task-` is not that element's id; it is a prefix of an
//     id that only exists at runtime, and a `#task-` selector must not resolve
//     to it. Dropped rather than truncated: there is no unresolved ref here to
//     record, because a definition site is not a reference to anything.
//
// Filters MatchResults rather than finished nodes, for the same reason
// dropNonRouteHelperNavMatches does: pass 1b in MatchToGraph lets a node at a
// file:line suppress another at the same one, so a match removed afterwards has
// already had its say.
func normalizeHTMLElementMatches(results []patterns.MatchResult) []patterns.MatchResult {
	out := results[:0]
	for _, r := range results {
		switch r.PatternName {
		case "html_element_class":
			cls := strings.Join(strings.Fields(r.Captures["class"]), " ")
			if cls == "" {
				continue
			}
			r.Captures["class"] = cls
		case "html_element_id":
			id := r.Captures["id"]
			if strings.TrimSpace(id) == "" || strings.ContainsAny(id, " \t\r\n") {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}
