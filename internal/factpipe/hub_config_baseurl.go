package factpipe

import (
	"net/url"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/configsrc"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_config_baseurl.go is the "config_baseurl" hub provider (see hub.go) —
// the Tier FX FX.8.30 migration of internal/linker/config_baseurl.go's
// retired ResolveConfigBaseURLPaths (Tier CB).
//
// The roster's original caveat called this "not FX-shaped — meta-mutation
// only, no edge path exists in emit.go" — that framing was already false as
// of `patch:` landing in FX.8.8 (2026-09-15), which needs only an id, no
// edge/mint. What actually blocked this pass until now is a real, separate
// gap `patch:` alone doesn't close: composing a config-supplied path prefix
// needs `internal/configsrc.Load`, which walks a service's OWN checked-in
// config tree (.env files, a k8s/terraform subdirectory search, deploy
// shell scripts) — real filesystem I/O rooted at the service's checkout
// directory, not a single file any node already points at. A HubProvider
// only ever received (nodes, files) before this; FX.8.30 is the reason
// hub.go's signature grew a third `svcPath` parameter (graph.Snapshot.
// ServicePath verbatim) — every hub before this one ignores it.
//
// The retired pass also conditionally DELETES two existing meta keys
// (path_evidence/confidence_ceiling) on a re-grade — `patch:`'s Meta overlay
// only ever adds/overwrites, so this is the reason patchSpec grew a
// `delete_meta:` field (a literal key list, unconditional per relation);
// the conditionality lives in which relation a row lands in, computed here
// in Go, not in a per-row flag.
func init() { RegisterHub("config_baseurl", configBaseURLHub) }

const (
	cbPatchPred   = "config_baseurl_patch"   // (ID, Path, PrefixFrom, PrefixRef)
	cbRegradePred = "config_baseurl_regrade" // (ID) — only rows whose stale weak stamp must clear
)

func configBaseURLHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	if svcPath == "" {
		return nil
	}
	var loaded map[string][]configsrc.Value
	loadedOnce := false

	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		envVar, literal := cbSource(n)
		if envVar == "" && literal == "" {
			continue
		}

		var prefix, ref string
		if literal != "" {
			prefix = cbURLPathPrefix(literal)
			ref = "host_default_literal"
			if prefix == "" {
				continue
			}
		} else {
			if !loadedOnce {
				loaded = configsrc.Load(svcPath)
				loadedOnce = true
			}
			var ok bool
			prefix, ref, ok = cbPathPrefix(loaded[envVar])
			if !ok || prefix == "" {
				continue
			}
		}

		path := n.Meta["path"]
		if path == "*"+prefix || strings.HasPrefix(path, "*"+prefix+"/") {
			continue
		}
		composed := "*" + prefix + strings.TrimPrefix(path, "*")

		prefixFrom := envVar
		if prefixFrom == "" {
			prefixFrom = literal
		}
		out = append(out, Fact{
			Pred:   cbPatchPred,
			Args:   []Atom{Node(n.ID), Str(composed), Str(prefixFrom), Str(ref)},
			Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: cbPatchPred},
		})

		if n.Meta["path_evidence"] == graph.PathEvidenceWeak &&
			(graph.PathEvidence(composed) == graph.PathEvidenceStrong || cbPrefixHasConcreteSegment(prefix)) {
			out = append(out, Fact{
				Pred:   cbRegradePred,
				Args:   []Atom{Node(n.ID)},
				Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: cbRegradePred},
			})
		}
	}
	return out
}

// --- ported structural helpers (cb-prefixed, from config_baseurl.go) ---

func cbSource(n *graph.Node) (envVar, literal string) {
	if n.Type != graph.NodeTypeHTTPClient {
		return "", ""
	}
	if n.Meta["key_dynamic"] == "true" {
		return "", ""
	}
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

func cbPrefixHasConcreteSegment(prefix string) bool {
	for _, seg := range strings.Split(prefix, "/") {
		if seg == "" || seg == "*" || strings.HasPrefix(seg, ":") {
			continue
		}
		return true
	}
	return false
}

func cbPathPrefix(vals []configsrc.Value) (prefix, ref string, ok bool) {
	if len(vals) == 0 {
		return "", "", false
	}
	prefix = cbURLPathPrefix(vals[0].Value)
	for _, v := range vals[1:] {
		if cbURLPathPrefix(v.Value) != prefix {
			return "", "", false
		}
	}
	return prefix, vals[0].Ref, true
}

func cbURLPathPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
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
