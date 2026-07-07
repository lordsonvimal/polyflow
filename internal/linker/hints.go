package linker

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// ApplyHints uses workspace link hints to annotate or filter edges.
// Hints have the form "ENV_VAR=http://host:port", which allows the linker
// to resolve base URLs used in HTTP client calls.
func ApplyHints(links []workspace.Link, nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	// Build env-var -> service name map from hints
	baseURLs := make(map[string]string) // base URL -> service name
	for _, link := range links {
		if link.Hint == "" {
			continue
		}
		// hint format: "ENV_VAR=http://host:port"
		parts := strings.SplitN(link.Hint, "=", 2)
		if len(parts) == 2 {
			baseURLs[parts[1]] = link.To
		}
	}

	// Annotate edges where the client URL matches a known base URL
	var result []graph.Edge
	for _, e := range edges {
		if e.Type == graph.EdgeTypeHTTPCall {
			for base, svc := range baseURLs {
				if strings.HasPrefix(e.Label, base) || matchesMeta(e, base) {
					e.Meta = ensureMeta(e.Meta)
					e.Meta["target_service"] = svc
					break
				}
			}
		}
		result = append(result, e)
	}
	return result
}

func matchesMeta(e graph.Edge, base string) bool {
	if e.Meta == nil {
		return false
	}
	return strings.HasPrefix(e.Meta["url"], base)
}

func ensureMeta(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string)
	}
	return m
}
