package linker

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// ApplyHints pre-processes client call nodes using workspace link hints:
//   - hint (ENV_VAR=URL): sets target_service on clients whose url matches
//   - hint (bare ENV_VAR): sets target_service on clients whose base URL was
//     traced back to that env var by ResolveGoHTTPHosts / ResolveRubyHTTPHosts (J.2)
//   - base_url: sets target_service on clients whose url or path starts with the prefix,
//     then strips the prefix from the path so the linker can match it to a handler
func ApplyHints(links []workspace.Link, nodes []graph.Node, edges []graph.Edge) []graph.Node {
	type linkRule struct {
		from    string
		to      string
		baseURL string // non-empty: path prefix that identifies this service
		hintURL string // non-empty: absolute URL base from ENV_VAR=URL hint
		envVar  string // non-empty: bare env-var name from a value-less hint (J.2a)
	}

	rules := make([]linkRule, 0, len(links))
	for _, link := range links {
		r := linkRule{from: link.From, to: link.To, baseURL: link.BaseURL}
		if link.Hint != "" {
			// `NAME=URL` pins an absolute base; a bare `NAME` is an env-var name.
			// `polyflow init` writes the bare form, so discarding it (the pre-J.2
			// behaviour) made every generated hint inert.
			if _, url, ok := strings.Cut(link.Hint, "="); ok {
				r.hintURL = url
			} else {
				r.envVar = strings.TrimSpace(link.Hint)
			}
		}
		rules = append(rules, r)
	}

	result := make([]graph.Node, len(nodes))
	copy(result, nodes)

	for i := range result {
		n := &result[i]
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}

		url := ""
		path := ""
		if n.Meta != nil {
			url = stripQuotes(n.Meta["url"])
			path = stripQuotes(n.Meta["path"])
		}

		for _, r := range rules {
			if r.from != n.Service {
				continue
			}

			// Match via absolute URL hint
			if r.hintURL != "" && strings.HasPrefix(url, r.hintURL) {
				n.Meta = ensureMeta(n.Meta)
				n.Meta["target_service"] = r.to
			}

			// Match via base_url prefix on path or url
			if r.baseURL != "" {
				matched := strings.HasPrefix(path, r.baseURL) || strings.HasPrefix(url, r.baseURL)
				if matched {
					n.Meta = ensureMeta(n.Meta)
					n.Meta["target_service"] = r.to
					// Strip prefix from path
					stripped := path
					if strings.HasPrefix(path, r.baseURL) {
						stripped = path[len(r.baseURL):]
					} else if strings.HasPrefix(url, r.baseURL) {
						// url like "/api/graph?..." — extract path portion after prefix
						rest := url[len(r.baseURL):]
						if qi := strings.Index(rest, "?"); qi >= 0 {
							rest = rest[:qi]
						}
						if !strings.HasPrefix(rest, "/") {
							rest = "/" + rest
						}
						stripped = rest
					}
					if stripped == "" {
						stripped = "/"
					}
					n.Meta["path"] = stripped
				}
			}
		}

		// J.2c: env-var allowlist. Runs after the URL/base_url rules and only
		// when they left the node unattributed — a base_url or absolute-URL hint
		// is direct evidence of the target, an env-var name is an inference from
		// the deploy config, so the direct evidence wins.
		if n.Meta["target_service"] != "" {
			continue
		}
		nodeEnv := ""
		if n.Meta != nil {
			// env_var: Go (J.2b). host_env_var: Ruby (Tier L). Same meaning.
			if nodeEnv = n.Meta["env_var"]; nodeEnv == "" {
				nodeEnv = n.Meta["host_env_var"]
			}
		}
		if nodeEnv == "" {
			continue
		}
		for _, r := range rules {
			if r.from != n.Service || r.envVar == "" || r.envVar != nodeEnv {
				continue
			}
			n.Meta = ensureMeta(n.Meta)
			n.Meta["target_service"] = r.to
			break
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

// stripQuotes removes surrounding single quotes, double quotes, or backticks.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
