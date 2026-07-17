package evidence

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ProposedRule is a candidate contract rule generated from an observed_only_gap edge.
// The filename is derived from (Kind, Key) — never from a counter or map-iteration
// order (bug-class rule 2). Two runs on the same gaps produce identical output.
type ProposedRule struct {
	// Filename is the suggested output filename (no directory).
	// Derived from slugified (kind, key) so it is stable across runs.
	Filename string
	// Content is the YAML text that could be placed in contracts/proposed/.
	Content string
}

// ProposeRules generates one candidate contract rule YAML per unique (kind, key)
// channel observed in gaps. Multiple gap edges sharing the same channel are merged
// into one proposal (they represent the same observed-but-unmodeled relationship).
// The returned slice is sorted by Filename (bug-class rule 2).
func ProposeRules(gaps []EdgeSummary) []ProposedRule {
	type channelKey struct{ kind, key string }
	type channelInfo struct {
		froms   map[string]bool
		tos     map[string]bool
		sources map[string]bool
	}

	byChannel := make(map[channelKey]*channelInfo)
	var channelOrder []channelKey

	for _, g := range gaps {
		k := channelKey{g.Kind, g.Key}
		ch, exists := byChannel[k]
		if !exists {
			ch = &channelInfo{
				froms:   make(map[string]bool),
				tos:     make(map[string]bool),
				sources: make(map[string]bool),
			}
			byChannel[k] = ch
			channelOrder = append(channelOrder, k)
		}
		if g.From != "" {
			ch.froms[g.From] = true
		}
		if g.To != "" {
			ch.tos[g.To] = true
		}
		for _, s := range g.Sources {
			ch.sources[s] = true
		}
	}

	// Sort channels by (kind, key) for deterministic output (bug-class rule 2).
	sort.Slice(channelOrder, func(i, j int) bool {
		if channelOrder[i].kind != channelOrder[j].kind {
			return channelOrder[i].kind < channelOrder[j].kind
		}
		return channelOrder[i].key < channelOrder[j].key
	})

	now := time.Now().UTC().Format(time.RFC3339)
	proposals := make([]ProposedRule, 0, len(channelOrder))
	for _, ck := range channelOrder {
		ch := byChannel[ck]
		filename := proposedFilename(ck.kind, ck.key)

		froms := sortedKeys(ch.froms)
		tos := sortedKeys(ch.tos)
		sources := sortedKeys(ch.sources)

		content := buildProposedYAML(ck.kind, ck.key, froms, tos, sources, now)
		proposals = append(proposals, ProposedRule{Filename: filename, Content: content})
	}
	return proposals
}

// proposedFilename derives a stable filename from (kind, key).
// e.g., kind="http_call" key="GET /api/users" → "http_call-get-api-users.yaml"
func proposedFilename(kind, key string) string {
	slug := slugify(kind) + "-" + slugify(key)
	// Trim to reasonable length so paths stay usable.
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug + ".yaml"
}

// slugify converts a string to lowercase-kebab with non-alphanumeric chars replaced by "-".
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prev := '-'
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prev = r
		} else if prev != '-' {
			b.WriteRune('-')
			prev = '-'
		}
	}
	return strings.Trim(b.String(), "-")
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func buildProposedYAML(kind, key string, froms, tos, sources []string, generatedAt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Auto-proposed by polyflow reconcile — observed_only_gap\n")
	fmt.Fprintf(&b, "# Review and move to contracts/ after verification.\n")
	fmt.Fprintf(&b, "# Generated: %s\n", generatedAt)
	fmt.Fprintf(&b, "#\n")
	fmt.Fprintf(&b, "# Channel: %s %s\n", kind, key)
	if len(froms) > 0 {
		fmt.Fprintf(&b, "# From (observed): %s\n", strings.Join(froms, ", "))
	}
	if len(tos) > 0 {
		fmt.Fprintf(&b, "# To (observed): %s\n", strings.Join(tos, ", "))
	}
	if len(sources) > 0 {
		fmt.Fprintf(&b, "# Evidence providers: %s\n", strings.Join(sources, ", "))
	}
	b.WriteString("#\n")
	b.WriteString("# This channel was observed at runtime or declared in a spec but not found\n")
	b.WriteString("# in the static graph. To activate, configure the producer/consumer node\n")
	b.WriteString("# types below to match your codebase and add to contracts/.\n")
	b.WriteString("proposed: true\n")
	b.WriteString("version: \"1\"\n")
	b.WriteString("contracts:\n")
	fmt.Fprintf(&b, "  - kind: %s\n", kind)
	fmt.Fprintf(&b, "    # channel: %q\n", key)
	b.WriteString("    producer:\n")
	b.WriteString("      node: # TODO: set to the producer node type (e.g. http_client)\n")
	b.WriteString("      key: [] # TODO: set to the key fields (e.g. [url])\n")
	b.WriteString("    consumer:\n")
	b.WriteString("      node: # TODO: set to the consumer node type (e.g. http_handler)\n")
	b.WriteString("      key: [] # TODO: set to the key fields (e.g. [path])\n")
	b.WriteString("    edge:\n")
	fmt.Fprintf(&b, "      type: %s\n", kind)
	return b.String()
}
