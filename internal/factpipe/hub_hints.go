package factpipe

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_hints.go is the "hints" hub provider (see hub.go) — the Tier FX
// FX.8.31 migration of internal/linker/hints.go's retired ApplyHints
// (J.2/J.2a/J.2b/J.2c, Tier JH).
//
// The roster's original caveat ("not FX-shaped — meta-mutation only, no
// edge path exists") was already false once `patch:` landed (FX.8.8). The
// REAL gap, same shape as config_baseurl's svcPath gap, is that ApplyHints
// needs workspace.WorkspaceConfig.Links — a fleet-wide config naming TWO
// services (From/To) that lives in the workspace root config, not under
// either service's own checkout, so no HubProvider before this one could
// see it. hub.go's HubProvider signature grew a fourth `links` parameter
// (graph.Snapshot.Links, a graph-local LinkHint mirror of workspace.Link —
// graph cannot import workspace, which already imports graph).
//
// Unlike config_baseurl/js_http_grade, this hub does NOT need a two-relation
// "don't clobber" split: the retired Go's rule precedence (base_url/hint
// match wins over env-var fallback, which wins over the literal-host gate)
// is entirely sequential Go logic over one working node, so the hub computes
// each node's FINAL winning target_service/path/key_dynamic once, the same
// way the original function did, and emits at most one patch fact per node.
// `patch:`'s Meta overlay already skips any column that resolves empty, so
// a single relation with optional columns covers every case (URL-hint-only,
// base_url-with-path-strip, env-var-only, literal-host-dynamic-fallback).
func init() { RegisterHub("hints", hintsHub) }

const hintsPatchPred = "hints_patch" // (ID, TargetService, Path, KeyDynamic)

type hintRule struct {
	from    string
	to      string
	baseURL string // non-empty: path prefix that identifies this service
	hintURL string // non-empty: absolute URL base from ENV_VAR=URL hint
	envVar  string // non-empty: bare env-var name from a value-less hint (J.2a)
}

func hintsHub(nodes []graph.Node, _ []string, _ string, links []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	// No early return on empty links: the literal-host gate (below) still
	// fires unconditionally on every lit-host node with no claiming hint,
	// defaulting it to key_dynamic="true" even when the fleet has zero
	// link rules configured — dropping to nil here would silently skip
	// that default.
	rules := make([]hintRule, 0, len(links))
	for _, link := range links {
		r := hintRule{from: link.From, to: link.To, baseURL: link.BaseURL}
		if link.Hint != "" {
			if _, url, ok := strings.Cut(link.Hint, "="); ok {
				r.hintURL = url
			} else {
				r.envVar = strings.TrimSpace(link.Hint)
			}
		}
		rules = append(rules, r)
	}

	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		targetService, path, keyDynamic := hintsResolveNode(n, rules)
		if targetService == "" && path == "" && keyDynamic == "" {
			continue
		}
		out = append(out, Fact{
			Pred:   hintsPatchPred,
			Args:   []Atom{Node(n.ID), Str(targetService), Str(path), Str(keyDynamic)},
			Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: hintsPatchPred},
		})
	}
	return out
}

// hintsResolveNode ports ApplyHints' per-node body verbatim, minus the
// direct n.Meta mutation — it returns the winning target_service/path
// (empty when the rule loop never strips a prefix) and, only for the
// literal-host gate's negative branch, "true" for key_dynamic.
func hintsResolveNode(n *graph.Node, rules []hintRule) (targetService, path, keyDynamic string) {
	url := ""
	origTargetService := ""
	if n.Meta != nil {
		url = hintsStripQuotes(n.Meta["url"])
		path = hintsStripQuotes(n.Meta["path"])
		origTargetService = n.Meta["target_service"]
	}
	origPath := path
	// Every stage below only acts when target_service is still unset — an
	// earlier pass (or an earlier stage in this same function, mirroring
	// ApplyHints' direct n.Meta mutation) may already have claimed the
	// node, and that claim must not be overwritten.
	targetService = origTargetService

	for _, r := range rules {
		if r.from != n.Service {
			continue
		}
		if r.hintURL != "" && strings.HasPrefix(url, r.hintURL) {
			targetService = r.to
		}
		if r.baseURL != "" {
			matched := strings.HasPrefix(origPath, r.baseURL) || strings.HasPrefix(url, r.baseURL)
			if matched {
				targetService = r.to
				stripped := origPath
				if strings.HasPrefix(origPath, r.baseURL) {
					stripped = origPath[len(r.baseURL):]
				} else if strings.HasPrefix(url, r.baseURL) {
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
				path = stripped
			}
		}
	}
	if path == origPath {
		path = "" // no base_url rewrite happened — nothing to patch
	}

	if targetService == "" {
		nodeEnv := ""
		if n.Meta != nil {
			if nodeEnv = n.Meta["env_var"]; nodeEnv == "" {
				nodeEnv = n.Meta["host_env_var"]
			}
		}
		if nodeEnv != "" {
			for _, r := range rules {
				if r.from != n.Service || r.envVar == "" || r.envVar != nodeEnv {
					continue
				}
				targetService = r.to
				break
			}
		}
	}

	// Tier JH literal-host gate: independent of the loops above, and only
	// when this node still has no target_service by the time it runs (an
	// n.Meta read, matching the retired function's post-loop re-check).
	lit := ""
	if n.Meta != nil {
		lit = n.Meta["host_default_literal"]
	}
	if lit != "" && n.Meta["key_dynamic"] != "true" {
		effectiveTarget := targetService
		if effectiveTarget == "" {
			for _, r := range rules {
				if r.from != n.Service || r.hintURL == "" {
					continue
				}
				if strings.HasPrefix(lit, r.hintURL) {
					effectiveTarget = r.to
					break
				}
			}
			if effectiveTarget != "" {
				targetService = effectiveTarget
			} else {
				keyDynamic = "true"
			}
		}
	}

	if targetService == origTargetService {
		targetService = "" // unchanged from the node's existing Meta — nothing to patch
	}
	return targetService, path, keyDynamic
}

// hintsStripQuotes removes surrounding single quotes, double quotes, or
// backticks — ported from the retired internal/linker/hints.go's
// stripQuotes (its only other caller, goDynamicHTTPNode, no longer exists).
func hintsStripQuotes(s string) string {
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}
