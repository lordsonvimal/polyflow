package contract

import (
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// wrapperEntry describes a function detected as a one-hop producer wrapper.
type wrapperEntry struct {
	Kind    string // contract kind ("http" | "kafka" | etc.)
	BaseURL string // prefix applied to call-site argument (may be empty)
	Service string
	File    string
	Line    int
}

// wrapperURLTarget records which parameter of a WB.1-detected wrapper
// function actually carries the URL, keyed by indirKey(service, file,
// wrapperName) — same scoping as aliasTable/wrapperTable. Populated from
// wrapper_url_target/wrapper_url_param_key facts emitted by
// patterns/javascript/producer_wrapper_body.yaml.
type wrapperURLTarget struct {
	ParamIndex int    // -1 if unset (WB.1 only ships member-access/destructure detection, never positional)
	ParamKey   string // "" if unset
}

// objCallPriority is the fallback key-name order WB.3 uses to disambiguate a
// producer_alias_obj_call candidate group when no wrapper_url_param_key fact
// is available for the callee.
var objCallPriority = []string{"url", "uri", "path", "endpoint"}

// spanKey returns the call-site grouping key WB.3 uses to collapse WB.2's
// per-object-key producer_alias_obj_call candidates back to one. graph.Node
// has no column, so two independent wrapper calls sharing a line (minified/
// generated JS) are indistinguishable and will incorrectly group — a known,
// accepted limitation (see docs/js-wrapper-url-backtrack-plan.md Phase WB.2).
func spanKey(n graph.Node) string {
	return fmt.Sprintf("%s\x00%s\x00%d", n.Service, n.File, n.Line)
}

// resolveObjCallGroup picks exactly one node from a group of
// producer_alias_obj_call candidates that share one call-site span:
//
//  1. If the callee resolves in wrapperURLTable with a ParamKey, keep the
//     candidate whose obj_key meta equals it.
//  2. Else fall back to objCallPriority, keeping the first candidate whose
//     key matches, in that order.
//  3. Else keep the first candidate by source order and report it as
//     ambiguous — recall is preserved either way, never silently guessed.
func resolveObjCallGroup(group []graph.Node, wrapperURLTable map[string]wrapperURLTarget) (graph.Node, *graph.UnresolvedRef) {
	if len(group) == 1 {
		return group[0], nil
	}
	if via := group[0].Meta["via_alias"]; via != "" {
		k := indirKey(group[0].Service, group[0].File, via)
		if w, ok := wrapperURLTable[k]; ok && w.ParamKey != "" {
			for _, n := range group {
				if n.Meta["key"] == w.ParamKey {
					return n, nil
				}
			}
		}
	}
	for _, want := range objCallPriority {
		for _, n := range group {
			if n.Meta["key"] == want {
				return n, nil
			}
		}
	}
	winner := group[0]
	return winner, &graph.UnresolvedRef{
		Service: winner.Service, File: winner.File, Line: winner.Line,
		Name: winner.Meta["via_alias"], Kind: "wrapper_key_ambiguous",
	}
}

// resolveObjCallCandidates groups producer_alias_obj_call nodes by call-site
// span and collapses each group to a single winner via resolveObjCallGroup,
// stripping the obj_key meta ("key") off the survivor so it flows through the
// rest of EnrichAliases exactly like a single-key match always has. All other
// nodes pass through unchanged, in original order; resolved winners are
// appended after them (order doesn't matter downstream: edges built earlier
// by MatchToGraph already target these nodes by ID, and every candidate in a
// group shares one ID since http_client nodes are keyed by pattern name, not
// by which object key they captured).
func resolveObjCallCandidates(nodes []graph.Node, wrapperURLTable map[string]wrapperURLTarget) ([]graph.Node, []graph.UnresolvedRef) {
	groups := make(map[string][]graph.Node)
	var order []string
	result := make([]graph.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Meta["pattern"] == "producer_alias_obj_call" {
			k := spanKey(n)
			if _, exists := groups[k]; !exists {
				order = append(order, k)
			}
			groups[k] = append(groups[k], n)
			continue
		}
		result = append(result, n)
	}
	var unresolved []graph.UnresolvedRef
	for _, k := range order {
		winner, ref := resolveObjCallGroup(groups[k], wrapperURLTable)
		result = append(result, stripNodeMeta(winner, "key"))
		if ref != nil {
			unresolved = append(unresolved, *ref)
		}
	}
	return result, unresolved
}

// EnrichAliases resolves producer alias bindings, instance idioms, and
// one-hop wrapper functions before Engine.Link runs. It returns an updated
// node slice and ledger entries for unresolvable indirections.
//
// Meta conventions (set by YAML patterns or tests):
//
// Alias/instance binding node:
//
//	meta["alias_name"]         = variable name bound to a producer function
//	meta["alias_source_kind"]  = "http" | "kafka" | etc.
//	meta["alias_source_method"]= specific method ("POST") for destructured aliases
//	meta["alias_base_url"]     = base URL (for instances; empty for function aliases)
//	meta["instance_name"]      = variable name of an instance created by a factory idiom
//	meta["instance_source_kind"]= producer kind
//	meta["instance_base_url"]  = base URL from instance creation config
//
// Alias/instance call node:
//
//	meta["via_alias"]          = variable name used to make the call
//
// Wrapper function node (real function node, stays in graph):
//
//	meta["wrapper_for"]        = contract kind the function wraps ("http" | etc.)
//	meta["wrapper_base_url"]   = prefix applied to call-site arg (may be "")
//
// Wrapper call site node:
//
//	meta["wrapper_name"]       = function being called
//
// Factory closure node:
//
//	meta["factory_closure"]    = "true"
//
// Ledger kinds emitted: alias_reassigned, factory_dynamic, wrapper_depth.
func EnrichAliases(nodes []graph.Node) ([]graph.Node, []graph.UnresolvedRef) {
	// Pass 1: build alias table (from binding/instance nodes) and wrapper table.
	type aliasEntry struct {
		SourceKind   string
		SourceMethod string
		BaseURL      string
		Service      string
		File         string
		Line         int
		Count        int
	}
	aliasTable := make(map[string]*aliasEntry) // indirKey → entry
	wrapperTable := make(map[string]*wrapperEntry)
	wrapperURLTable := make(map[string]wrapperURLTarget)

	for _, n := range nodes {
		// WB.1: wrapper_url_target/wrapper_url_param_key facts — a wrapper
		// function's own body revealed which of its parameters carries the URL.
		// Distinguished from the pre-existing wrapper_for/wrapper_name call-site
		// mechanism above by pattern prefix, since both happen to use the
		// "wrapper_name" capture name for different things.
		if strings.HasPrefix(n.Meta["pattern"], "wrapper_url_") {
			wname := n.Meta["wrapper_name"]
			if wname != "" {
				k := indirKey(n.Service, n.File, wname)
				if _, exists := wrapperURLTable[k]; !exists {
					wrapperURLTable[k] = wrapperURLTarget{ParamIndex: -1, ParamKey: n.Meta["url_key"]}
				}
			}
		}
		if name := n.Meta["alias_name"]; name != "" {
			k := indirKey(n.Service, n.File, name)
			if e, ok := aliasTable[k]; ok {
				e.Count++
			} else {
				kind := n.Meta["alias_source_kind"]
				if kind == "" {
					kind = inferSourceKindFromPattern(n.Meta["pattern"])
				}
				aliasTable[k] = &aliasEntry{
					SourceKind:   kind,
					SourceMethod: n.Meta["alias_source_method"],
					BaseURL:      n.Meta["alias_base_url"],
					Service:      n.Service,
					File:         n.File,
					Line:         n.Line,
					Count:        1,
				}
			}
		}
		if name := n.Meta["instance_name"]; name != "" {
			k := indirKey(n.Service, n.File, name)
			if e, ok := aliasTable[k]; ok {
				e.Count++
			} else {
				kind := n.Meta["instance_source_kind"]
				if kind == "" {
					kind = inferSourceKindFromPattern(n.Meta["pattern"])
				}
				baseURL := n.Meta["instance_base_url"]
				if baseURL != "" && !isLiteralURL(baseURL) {
					// Non-literal base URL: instance_unresolved ledger added at resolution
					baseURL = ""
				}
				aliasTable[k] = &aliasEntry{
					SourceKind: kind,
					BaseURL:    baseURL,
					Service:    n.Service,
					File:       n.File,
					Line:       n.Line,
					Count:      1,
				}
			}
		}
		if kind := n.Meta["wrapper_for"]; kind != "" {
			k := indirKey(n.Service, n.File, n.Label)
			if _, exists := wrapperTable[k]; !exists {
				wrapperTable[k] = &wrapperEntry{
					Kind:    kind,
					BaseURL: n.Meta["wrapper_base_url"],
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
				}
			}
		}
	}

	// WB.3: collapse WB.2's per-object-key producer_alias_obj_call candidate
	// groups to one winner each, before the existing via_alias handling below
	// runs on them — a resolved winner still carries via_alias and flows
	// through that generic path exactly like any other single-key match.
	nodes, objCallUnresolved := resolveObjCallCandidates(nodes, wrapperURLTable)

	// Pass 2: process nodes.
	result := make([]graph.Node, 0, len(nodes))
	unresolved := objCallUnresolved
	ledgeredAlias := make(map[string]bool) // suppress duplicate alias_reassigned entries

	for _, n := range nodes {
		// Remove alias/instance binding nodes (consumed above; not real producers).
		if n.Meta["alias_name"] != "" || n.Meta["instance_name"] != "" {
			continue
		}

		// WB.1: wrapper_url_target/wrapper_url_param_key bookkeeping nodes are
		// self-describing facts about a wrapper function's own body, already
		// consumed into wrapperURLTable above — not a call site. They happen to
		// reuse the "wrapper_name" capture name, so they must be excluded here
		// before the generic wrapper-call-site branch below, or they're
		// mistaken for a call to an unknown wrapper (bogus factory_dynamic
		// ledger entry) and dropped. Left in the result unmodified, same as
		// gin_group_registrar_* bookkeeping nodes (inert, no calls edges — see
		// internal/patterns/matcher.go's classifyPattern/Pass 2 exclusion).
		if strings.HasPrefix(n.Meta["pattern"], "wrapper_url_") {
			result = append(result, n)
			continue
		}

		// Factory closure → factory_dynamic ledger + drop.
		if n.Meta["factory_closure"] == "true" {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: n.Label, Kind: "factory_dynamic",
			})
			continue
		}

		// Alias/instance call node (via_alias set).
		if alias := n.Meta["via_alias"]; alias != "" {
			k := indirKey(n.Service, n.File, alias)
			e, found := aliasTable[k]
			if found {
				if e.Count > 1 {
					// Reassigned alias → ledger (once per name).
					if !ledgeredAlias[k] {
						ledgeredAlias[k] = true
						unresolved = append(unresolved, graph.UnresolvedRef{
							Service: n.Service, File: n.File, Line: e.Line,
							Name: alias, Kind: "alias_reassigned",
						})
					}
					continue // drop the unresolvable call
				}
				n = applyAliasRes(n, e.SourceKind, e.SourceMethod, e.BaseURL)
			} else {
				// Not a known alias (cross-file or truly unknown): strip via_alias
				// and let the engine handle the node with its existing key fields.
				n = stripNodeMeta(n, "via_alias")
			}
			result = append(result, n)
			continue
		}

		// Wrapper call site (wrapper_name set).
		if wname := n.Meta["wrapper_name"]; wname != "" {
			k := indirKey(n.Service, n.File, wname)
			w, found := wrapperTable[k]
			if found {
				n = applyWrapperRes(n, w)
				result = append(result, n)
			} else {
				// No known wrapper with that name → factory_dynamic ledger.
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: wname, Kind: "factory_dynamic",
				})
			}
			continue
		}

		result = append(result, n)
	}

	return result, unresolved
}

// applyAliasRes updates an alias/instance call node with resolved meta:
// composes base URL + path, sets missing method, stamps via=alias.
func applyAliasRes(n graph.Node, sourceKind, sourceMethod, baseURL string) graph.Node {
	meta := cloneNodeMeta(n.Meta)
	if baseURL != "" {
		path := meta["url"]
		if path == "" {
			path = meta["path"]
		}
		composed := baseURL + path
		meta["url"] = composed
		meta["path"] = composed
	}
	if sourceMethod != "" && meta["method"] == "" {
		meta["method"] = sourceMethod
	}
	delete(meta, "via_alias")
	meta["via"] = "alias"
	n.Type = kindToProducerNodeType(sourceKind)
	n.Meta = meta
	return n
}

// applyWrapperRes updates a wrapper call site node: composes base prefix + arg,
// stamps via=wrapper.
func applyWrapperRes(n graph.Node, w *wrapperEntry) graph.Node {
	meta := cloneNodeMeta(n.Meta)
	if w.BaseURL != "" {
		url := meta["url"]
		if url == "" {
			url = meta["path"]
		}
		composed := w.BaseURL + url
		meta["url"] = composed
		meta["path"] = composed
	}
	delete(meta, "wrapper_name")
	meta["via"] = "wrapper"
	n.Type = kindToProducerNodeType(w.Kind)
	n.Meta = meta
	return n
}

// kindToProducerNodeType maps a contract kind string to the producer NodeType.
func kindToProducerNodeType(kind string) graph.NodeType {
	switch kind {
	case "http":
		return graph.NodeTypeHTTPClient
	case "kafka", "nats", "redis_pubsub", "amqp", "job", "pusher":
		return graph.NodeTypePublisher
	default:
		return graph.NodeTypeHTTPClient
	}
}

// inferSourceKindFromPattern infers the producer kind from a pattern name when
// alias_source_kind / instance_source_kind is not explicitly set.
func inferSourceKindFromPattern(patternName string) string {
	switch {
	case containsAny(patternName, "axios", "fetch", "faraday", "resty", "net_http", "httparty", "jquery", "http"):
		return "http"
	case containsAny(patternName, "kafka"):
		return "kafka"
	case containsAny(patternName, "nats"):
		return "nats"
	case containsAny(patternName, "redis"):
		return "redis_pubsub"
	case containsAny(patternName, "amqp", "bunny"):
		return "amqp"
	case containsAny(patternName, "pusher"):
		return "pusher"
	default:
		return "http"
	}
}

// isLiteralURL reports whether s looks like a literal URL or path (starts with
// "/" or "http") so base URL composition is meaningful.
func isLiteralURL(s string) bool {
	return len(s) > 0 && (s[0] == '/' || len(s) > 4 && (s[:4] == "http"))
}

// indirKey returns the lookup key for alias/wrapper tables.
func indirKey(service, file, name string) string {
	return service + "\x00" + file + "\x00" + name
}

// cloneNodeMeta deep-copies a node's meta map.
func cloneNodeMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// stripNodeMeta returns a copy of n with the named meta key removed.
func stripNodeMeta(n graph.Node, key string) graph.Node {
	if _, ok := n.Meta[key]; !ok {
		return n
	}
	n.Meta = cloneNodeMeta(n.Meta)
	delete(n.Meta, key)
	return n
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
