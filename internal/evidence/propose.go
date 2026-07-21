package evidence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/lordsonvimal/polyflow/internal/contract"
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

// Proposal is a full proposal emitted by ProposeWithFixtures: rule YAML + fixture skeleton,
// both ready to write. Position is the 1-based index in the sorted (kind, key) order.
type Proposal struct {
	Position        int
	YAMLFilename    string // "<n>-<slug>.yaml"
	YAMLContent     string // passes contract.ParseAndValidateBytes
	FixtureFilename string // "<n>-<slug>.json"
	FixtureContent  string // JSON: FixtureCase
}

// FixtureNode is a node entry in a proposal fixture.
type FixtureNode struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Service string            `json:"service"`
	Label   string            `json:"label,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// FixtureEdge is an expected edge entry in a proposal fixture.
type FixtureEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

// FixtureCase is the JSON structure for a proposal test fixture. The operator
// fills in node types and meta fields, then runs `polyflow rules promote <yaml>`.
type FixtureCase struct {
	Comment       string        `json:"comment,omitempty"`
	Nodes         []FixtureNode `json:"nodes"`
	ExpectedEdges []FixtureEdge `json:"expected_edges"`
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

	proposals := make([]ProposedRule, 0, len(channelOrder))
	for _, ck := range channelOrder {
		ch := byChannel[ck]
		filename := proposedFilename(ck.kind, ck.key)

		froms := sortedKeys(ch.froms)
		tos := sortedKeys(ch.tos)
		sources := sortedKeys(ch.sources)

		content := buildProposedYAML(ck.kind, ck.key, froms, tos, sources)
		proposals = append(proposals, ProposedRule{Filename: filename, Content: content})
	}
	return proposals
}

// ProposeWithFixtures generates proposals with 1-based position numbers and
// companion fixture skeletons. The returned slice is sorted by (kind, key).
// Each proposal's YAML is validated through the contract loader before inclusion
// (rule 3, docs/phases.md); a proposal that would fail load is skipped and its
// error returned. Running twice on the same gaps produces byte-identical output
// (bug-class rule 2 — no timestamps or map-iteration counters).
func ProposeWithFixtures(gaps []EdgeSummary) ([]Proposal, error) {
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

	// Sort channels by (kind, key) — position is the index in this sorted order.
	sort.Slice(channelOrder, func(i, j int) bool {
		if channelOrder[i].kind != channelOrder[j].kind {
			return channelOrder[i].kind < channelOrder[j].kind
		}
		return channelOrder[i].key < channelOrder[j].key
	})

	out := make([]Proposal, 0, len(channelOrder))
	for i, ck := range channelOrder {
		ch := byChannel[ck]
		pos := i + 1 // 1-based
		slug := proposalSlug(ck.kind, ck.key)
		yamlFilename := fmt.Sprintf("%d-%s.yaml", pos, slug)
		fixFilename := fmt.Sprintf("%d-%s.json", pos, slug)

		froms := sortedKeys(ch.froms)
		tos := sortedKeys(ch.tos)
		sources := sortedKeys(ch.sources)

		yamlContent := buildProposedYAML(ck.kind, ck.key, froms, tos, sources)

		// Validate before including (rule 3 — must pass loader).
		if _, err := contract.ParseAndValidateBytes([]byte(yamlContent)); err != nil {
			return nil, fmt.Errorf("proposal %s failed contract validation (template bug): %w", yamlFilename, err)
		}

		fixContent, err := buildFixtureSkeleton(ck.kind, ck.key, froms, tos)
		if err != nil {
			return nil, fmt.Errorf("build fixture for %s: %w", yamlFilename, err)
		}

		out = append(out, Proposal{
			Position:        pos,
			YAMLFilename:    yamlFilename,
			YAMLContent:     yamlContent,
			FixtureFilename: fixFilename,
			FixtureContent:  fixContent,
		})
	}
	return out, nil
}

// proposedFilename derives a stable filename from (kind, key) for ProposeRules
// (no position prefix — used by reconcile --propose-dir).
func proposedFilename(kind, key string) string {
	slug := proposalSlug(kind, key)
	return slug + ".yaml"
}

// proposalSlug returns the stable kebab-case slug for a (kind, key) pair.
func proposalSlug(kind, key string) string {
	slug := Slugify(kind) + "-" + Slugify(key)
	if len(slug) > 80 {
		slug = slug[:80]
	}
	return slug
}

// Slugify converts a string to lowercase-kebab with non-alphanumeric chars replaced by "-".
// Exported so the promote command and tests can derive filenames consistently.
func Slugify(s string) string {
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

// buildProposedYAML generates a contract rule YAML that passes contract.ParseAndValidateBytes.
// No timestamp is included — output is deterministic across runs (bug-class rule 2).
func buildProposedYAML(kind, key string, froms, tos, sources []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Auto-proposed by polyflow — observed_only_gap\n")
	fmt.Fprintf(&b, "# Review, fill in node types and key fields, then run:\n")
	fmt.Fprintf(&b, "#   polyflow rules promote <this-file>\n")
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
	b.WriteString("# This channel was observed at runtime or declared in a spec but not\n")
	b.WriteString("# found in the static graph. Fill in producer/consumer to activate it.\n")
	b.WriteString("proposed: true\n")
	b.WriteString("version: \"1\"\n")
	b.WriteString("contracts:\n")
	fmt.Fprintf(&b, "  - kind: %s\n", kind)
	b.WriteString("    producer:\n")
	b.WriteString("      node: \"\"  # TODO: producer node type (e.g. http_client, job_enqueue)\n")
	b.WriteString("      key: []   # TODO: key fields    (e.g. [method, path])\n")
	b.WriteString("    consumer:\n")
	b.WriteString("      node: \"\"  # TODO: consumer node type (e.g. http_handler, job_consumer)\n")
	b.WriteString("      key: []   # TODO: key fields    (e.g. [method, path])\n")
	b.WriteString("    normalizers: []  # TODO: normalizers (e.g. [param_wildcard])\n")
	b.WriteString("    match: [normalized, wildcard_anchored]\n")
	b.WriteString("    edge:\n")
	fmt.Fprintf(&b, "      type: %s\n", kind)
	b.WriteString("      id_prefix: proposed\n")
	b.WriteString("      same_service: skip\n")
	b.WriteString("    unmatched: ledger\n")
	return b.String()
}

// buildFixtureSkeleton generates a JSON fixture skeleton for a proposal.
// The operator fills in node types and meta fields before running promote.
func buildFixtureSkeleton(kind, key string, froms, tos []string) (string, error) {
	// Use the first observed from/to if available; otherwise use generic placeholders.
	fromSvc := "svc-producer"
	if len(froms) > 0 {
		fromSvc = froms[0]
	}
	toSvc := "svc-consumer"
	if len(tos) > 0 {
		toSvc = tos[0]
	}

	label := fmt.Sprintf("observed gap (%s)", key)
	fc := FixtureCase{
		Comment: fmt.Sprintf(
			"Fixture skeleton for proposed rule: %s %s. "+
				"Fill in node types and meta fields, then run: polyflow rules promote <proposal.yaml>",
			kind, key,
		),
		Nodes: []FixtureNode{
			{
				ID:      "producer-0",
				Type:    "TODO_producer_node_type",
				Service: fromSvc,
				Label:   label,
				Meta:    map[string]string{},
			},
			{
				ID:      "consumer-0",
				Type:    "TODO_consumer_node_type",
				Service: toSvc,
				Label:   label,
				Meta:    map[string]string{},
			},
		},
		ExpectedEdges: []FixtureEdge{
			{From: "producer-0", To: "consumer-0", Type: kind},
		},
	}

	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
