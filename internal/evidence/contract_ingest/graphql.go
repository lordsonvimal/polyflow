package contract_ingest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

var (
	// reGQLRootType matches "type Query {", "type Mutation {", "type Subscription {".
	// Extend keyword: "extend type Query" is also valid GraphQL.
	reGQLRootType = regexp.MustCompile(`^\s*(?:extend\s+)?type\s+(Query|Mutation|Subscription)\s*(?:implements[^{]*)?\{`)
	// reGQLField matches a resolver field line: "  fieldName(...): ReturnType"
	reGQLField = regexp.MustCompile(`^\s+(\w+)\s*[\(:!]`)
)

// parseGraphQL reads a GraphQL SDL file and emits one graphql_call Evidence
// edge per root-type field.  The channel key is the bare field name, which
// matches the "operation" meta stored by the graphql_client/graphql_resolver
// patterns.
func parseGraphQL(path, serviceName string) (edges []graph.Edge, _ []graph.UnresolvedRef, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("graphql: open %s: %w", path, err)
	}
	defer f.Close()

	rel := filepath.Base(path)

	inRoot := false
	depth := 0

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		// Strip inline comments.
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}

		if reGQLRootType.MatchString(line) {
			inRoot = true
			depth = 1
			continue
		}

		if inRoot {
			for _, ch := range line {
				switch ch {
				case '{':
					depth++
				case '}':
					depth--
				}
			}
			if depth <= 0 {
				inRoot = false
				depth = 0
				continue
			}

			// Only parse top-level fields (depth == 1 means we're directly in the root type).
			if depth == 1 {
				if match := reGQLField.FindStringSubmatch(line); match != nil {
					field := match[1]
					ref := fmt.Sprintf("%s#%s", rel, field)
					edgeID := fmt.Sprintf("contract:graphql:%s:%s", serviceName, field)
					edges = append(edges, graph.Edge{
						ID:    edgeID,
						From:  serviceName,
						To:    "",
						Type:  graph.EdgeTypeGraphQLCall,
						Label: field,
						Sources: []graph.SourceRef{{
							Provider:   "contract",
							Confidence: graph.ConfidenceDeclared,
							Ref:        ref,
						}},
					})
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("graphql: scan %s: %w", path, err)
	}
	return edges, nil, nil
}
