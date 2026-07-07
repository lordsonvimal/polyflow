package linker

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// ApplyHints pre-processes client call nodes using workspace link hints:
//   - hint (ENV_VAR=URL): annotates matching edges with target_service meta
//   - base_url: strips the prefix from client node paths so the linker can match them
func ApplyHints(links []workspace.Link, nodes []graph.Node, edges []graph.Edge) []graph.Node {
	// Build env-var -> service name map from hints
	baseURLs := make(map[string]string) // base URL string -> service name
	basePaths := make(map[string]string) // from-service+to-service -> base_url prefix to strip

	for _, link := range links {
		if link.Hint != "" {
			parts := strings.SplitN(link.Hint, "=", 2)
			if len(parts) == 2 {
				baseURLs[parts[1]] = link.To
			}
		}
		if link.BaseURL != "" {
			basePaths[link.From+"|"+link.To] = link.BaseURL
		}
	}

	// Annotate client nodes with target_service from ENV_VAR hints and strip base_url prefixes
	result := make([]graph.Node, len(nodes))
	copy(result, nodes)

	for i := range result {
		n := &result[i]
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}

		// Resolve target_service from base URL hint
		url := ""
		if n.Meta != nil {
			url = n.Meta["url"]
		}
		for base, svc := range baseURLs {
			if strings.HasPrefix(url, base) {
				n.Meta = ensureMeta(n.Meta)
				n.Meta["target_service"] = svc
				break
			}
		}

		// Strip base_url prefix from path for matching
		targetSvc := ""
		if n.Meta != nil {
			targetSvc = n.Meta["target_service"]
		}
		if targetSvc != "" && n.Service != "" {
			key := n.Service + "|" + targetSvc
			if prefix, ok := basePaths[key]; ok {
				if n.Meta != nil {
					path := n.Meta["path"]
					if strings.HasPrefix(path, prefix) {
						n.Meta["path"] = path[len(prefix):]
						if n.Meta["path"] == "" {
							n.Meta["path"] = "/"
						}
					}
				}
			}
		}
	}

	return result
}

func ensureMeta(m map[string]string) map[string]string {
	if m == nil {
		return make(map[string]string)
	}
	return m
}
