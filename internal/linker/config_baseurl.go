package linker

import (
	"net/url"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/configsrc"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ResolveConfigBaseURLPaths composes the path component of a client's
// config-supplied base URL onto its own path, so a client deployed behind
// `API_URL=https://api.example.com/v2` can match the `/v2/...` route it really
// calls.
//
// A client's path is only half in the source. `fmt.Sprintf("%s/user-apps",
// c.baseURL)` renders as `*/user-apps`, but the handler is
// `/api/v1/user-apps` — the `/api/v1` lives in the *value* of the environment
// variable `baseURL` was read from, and until now that value was read only by
// the config_resolve provider, which builds a channel label out of the resolved
// value alone and never consults the node's own path. The prefix was therefore
// thrown away, and every client whose deployment supplies the API prefix was a
// silent false negative.
//
// ResolveGoHTTPHosts (J.2b) and ResolveRubyHTTPHosts (Tier L) have already
// traced such a client back to the single env var its base URL is read from and
// stamped it (Meta["env_var"] / Meta["host_env_var"]); this pass is the
// consumer of that stamp for path purposes.
//
// The host is untouched and stays `*`: this pass adds path evidence, not
// service identity. Attributing the callee remains ApplyHints' job via a
// workspace `links: hint:`, and dynamic_host_strip still removes the `*`
// downstream.
//
// It abstains far more often than it fires, deliberately:
//
//   - only values checked into the repo are read (the same .env / k8s / tfvars
//     sources config_resolve scans) — a real deploy secret stays a named miss;
//   - a value with no path component (`http://localhost:3000`, the common
//     shape) is a no-op, as is a value it cannot parse as an http(s) URL;
//   - two config sources disagreeing about the path component means abstain,
//     because a wrong path is a fabricated route, not a hedged one.
//
// Returns the mutated nodes so the caller can re-persist them; metas are also
// mutated in place in the passed slice.
func ResolveConfigBaseURLPaths(nodes []graph.Node, svcPaths map[string]string) []graph.Node {
	var changed []graph.Node
	// One Load per service, and only for services that have a candidate node.
	loaded := make(map[string]map[string][]configsrc.Value)

	for i := range nodes {
		n := &nodes[i]
		envVar, literal := configBaseURLSource(n)
		if envVar == "" && literal == "" {
			continue
		}

		var prefix, ref string
		if literal != "" {
			// Tier JH case 2: a module-level literal default, not a config file
			// value — there is no configsrc.Load lookup, the value already is
			// the "config". confidence_ceiling stays whatever ResolveJSHTTPHosts
			// already stamped (partial); this pass only adds path evidence.
			prefix = configURLPathPrefix(literal)
			ref = "host_default_literal"
			if prefix == "" {
				continue
			}
		} else {
			dir, ok := svcPaths[n.Service]
			if !ok {
				continue
			}
			vals, done := loaded[n.Service]
			if !done {
				vals = configsrc.Load(dir)
				loaded[n.Service] = vals
			}
			var ok2 bool
			prefix, ref, ok2 = configPathPrefix(vals[envVar])
			if !ok2 || prefix == "" {
				continue
			}
		}

		path := n.Meta["path"]
		// Guard against double application. The same pass shape without this
		// check produced `/api/v1/admin/api/v1/admin/users/:id` when route-prefix
		// composition ran twice (persistComposedRoutes gained the same guard).
		// Compared segment-wise so a `/api` prefix does not swallow `*/apiv2/x`.
		if path == "*"+prefix || strings.HasPrefix(path, "*"+prefix+"/") {
			continue
		}

		composed := "*" + prefix + strings.TrimPrefix(path, "*")
		n.Meta["path"] = composed
		if envVar != "" {
			n.Meta["path_prefix_from"] = envVar
		} else {
			n.Meta["path_prefix_from"] = literal
		}
		n.Meta["path_prefix_ref"] = ref

		// Re-grade: a path graded `weak` (one literal segment behind an opaque
		// host) is suppressed by the contract engine whenever it resolves in
		// more than one service. Composing the prefix on is exactly what makes
		// it discriminating, so leaving the stale stamp would suppress the very
		// edge this pass exists to create — and a stale confidence_ceiling would
		// cap a now-ordinary match at `partial`. Only the ceiling that came with
		// the weak stamp is cleared; other passes set it for other reasons.
		if n.Meta["path_evidence"] == graph.PathEvidenceWeak &&
			graph.PathEvidence(composed) == graph.PathEvidenceStrong {
			delete(n.Meta, "path_evidence")
			if n.Meta["confidence_ceiling"] == graph.ConfidencePartial {
				delete(n.Meta, "confidence_ceiling")
			}
		}

		changed = append(changed, *n)
	}
	return changed
}

// configBaseURLSource returns the environment variable, or (for Tier JH case
// 2) the module-level literal default, n's base URL was traced to. Exactly
// one of the two return values is non-empty, or both are "" when n is not a
// candidate for prefix composition.
func configBaseURLSource(n *graph.Node) (envVar, literal string) {
	if n.Type != graph.NodeTypeHTTPClient {
		return "", ""
	}
	// A node whose URL never resolved has no path to prefix; it is still
	// config_resolve's to resolve or to ledger.
	if n.Meta["key_dynamic"] == "true" {
		return "", ""
	}
	// `*` marks "the host was opaque, this path was composed onto it". A client
	// that named its host literally is not composing onto a configured base and
	// must not be touched.
	if !strings.HasPrefix(n.Meta["path"], "*") {
		return "", ""
	}
	if env := n.Meta["env_var"]; env != "" {
		return env, ""
	}
	if env := n.Meta["host_env_var"]; env != "" {
		return env, ""
	}
	return "", n.Meta["host_default_literal"]
}

// configPathPrefix returns the single path component shared by every checked-in
// value of one variable, plus the ref of the value it came from. ok is false
// when the values disagree — one node gets one path or none, never a fan-out,
// because an alternative path is a fabricated route rather than a hedge.
// (config_resolve fans out one edge per value; it can, because it emits a
// service-less record, not a join.)
func configPathPrefix(vals []configsrc.Value) (prefix, ref string, ok bool) {
	if len(vals) == 0 {
		return "", "", false
	}
	prefix = configURLPathPrefix(vals[0].Value)
	for _, v := range vals[1:] {
		if configURLPathPrefix(v.Value) != prefix {
			return "", "", false
		}
	}
	return prefix, vals[0].Ref, true
}

// configURLPathPrefix returns the path component of a config value, normalised
// to a leading slash and no trailing slash, or "" when the value contributes no
// path. Deliberately strict: anything it cannot parse cleanly contributes
// nothing rather than a guess.
//
//	https://api.example.com/v2   -> /v2
//	http://localhost:3000        -> ""      (the fleet's common case)
//	http://localhost:3000/       -> ""
//	http://host/api/v1/          -> /api/v1
//	api.example.com/v2           -> ""      (no scheme: not confidently a URL)
//	${SOME_OTHER_VAR}/api        -> ""      (unexpanded interpolation)
//	amqp://guest@host/vhost      -> ""      (non-HTTP scheme)
func configURLPathPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Unexpanded interpolation is a recurring shape in .env.example files and
	// must never become route text.
	if strings.Contains(raw, "${") || strings.Contains(raw, "%(") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return ""
	}
	if u.Host == "" {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
