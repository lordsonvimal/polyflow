package workspace

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// InferLinks proposes cross-service Links from external_service base URLs, env hints
// (*_SERVICE_URL / *_HOST / *_BASE_URL), and broker exchange/channel overlap.
// Deterministic, multi-valued (a base URL matching two services proposes both).
//
// Two sources of evidence, both cheap because they reuse existing static output:
//  1. HTTP env-hint: every dynamic_url ledger entry (X.1's residue for a call whose
//     base could not be resolved statically) is checked for an env-var name shaped
//     like <NAME>_SERVICE_URL / <NAME>_HOST / <NAME>_BASE_URL; <NAME> is matched
//     (case/underscore-insensitive substring) against every other workspace service
//     name. Every match is proposed — a hint naming two services proposes both
//     (bug-class #1: fan-out, never first-match).
//  2. Broker exchange overlap: every indexed "channel" node (Pass 4's AMQP/broker
//     synthesis) carrying a non-empty "exchange" meta is grouped by (exchange value,
//     service); any two distinct services sharing an exchange value get a proposed
//     link in both directions (a channel node doubles as producer and consumer, same
//     as contracts/amqp.yaml itself treats it).
//
// Proposals are returned in sorted (From, To, Exchange) order — never map iteration
// order — so two runs over the same store produce byte-identical output.
func InferLinks(ctx context.Context, s *graph.SQLiteStore, cfg *WorkspaceConfig) ([]Link, error) {
	unresolved, err := s.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, err
	}
	idx, err := s.BuildIndex(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var proposals []Link
	addProposal := func(l Link, dedupKey string) {
		if seen[dedupKey] {
			return
		}
		seen[dedupKey] = true
		proposals = append(proposals, l)
	}

	// Sort unresolved refs before iterating — determinism (bug-class #2); the
	// store does not guarantee row order.
	sortedUnresolved := make([]graph.UnresolvedRef, len(unresolved))
	copy(sortedUnresolved, unresolved)
	sort.Slice(sortedUnresolved, func(i, j int) bool {
		a, b := sortedUnresolved[i], sortedUnresolved[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})

	for _, u := range sortedUnresolved {
		if u.Kind != "dynamic_url" {
			continue
		}
		varName := extractEnvVarName(u.Name)
		if varName == "" {
			continue
		}
		hint := serviceHintFromEnvVar(varName)
		if hint == "" {
			continue
		}
		for _, svc := range cfg.Services {
			if svc.Name == u.Service {
				continue
			}
			if !serviceNameMatchesHint(svc.Name, hint) {
				continue
			}
			addProposal(Link{From: u.Service, To: svc.Name, Hint: varName}, "http:"+u.Service+"->"+svc.Name)
		}
	}

	// Broker exchange overlap over channel nodes. Iterate node IDs sorted, not
	// map order (bug-class #2).
	nodeIDs := make([]string, 0, len(idx.Nodes))
	for id := range idx.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	exchToServices := make(map[string][]string)
	exchSvcSeen := make(map[string]bool)
	for _, id := range nodeIDs {
		n := idx.Nodes[id]
		if n.Type != graph.NodeTypeChannel {
			continue
		}
		exch := strings.Trim(n.Meta["exchange"], `"'`)
		if exch == "" {
			continue
		}
		key := exch + "\x00" + n.Service
		if exchSvcSeen[key] {
			continue
		}
		exchSvcSeen[key] = true
		exchToServices[exch] = append(exchToServices[exch], n.Service)
	}
	exchanges := make([]string, 0, len(exchToServices))
	for exch := range exchToServices {
		exchanges = append(exchanges, exch)
	}
	sort.Strings(exchanges)
	for _, exch := range exchanges {
		svcs := append([]string(nil), exchToServices[exch]...)
		sort.Strings(svcs)
		for _, from := range svcs {
			for _, to := range svcs {
				if from == to {
					continue
				}
				addProposal(Link{From: from, To: to, Via: "rabbitmq", Exchange: exch}, "broker:"+from+"->"+to+":"+exch)
			}
		}
	}

	sort.Slice(proposals, func(i, j int) bool {
		a, b := proposals[i], proposals[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Exchange < b.Exchange
	})
	return proposals, nil
}

// envVarFromExpr matches common patterns that reference env variables in code
// (mirrors internal/evidence/config_resolve's extraction so a raw ledger key
// like `ENV.fetch("ORDER_SERVICE_URL")` yields the bare variable name).
var envVarFromExpr = regexp.MustCompile(`(?i)(?:os\.getenv|getenv|process\.env\.|ENV\[|ENV\.fetch\(|os\.environ(?:\.get)?\[?|System\.getenv)\(?['"]?([A-Z_][A-Z0-9_]*)['"]?`)

// extractEnvVarName derives a bare environment variable name from a raw
// dynamic_url ledger key. Returns "" when no match is found.
func extractEnvVarName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if m := envVarFromExpr.FindStringSubmatch(raw); len(m) >= 2 {
		return m[1]
	}
	if isBareEnvVarName(raw) {
		return raw
	}
	return ""
}

// isBareEnvVarName returns true for names like MY_VAR, API_URL, DATABASE_URL.
func isBareEnvVarName(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		upper := c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		under := c == '_'
		if i == 0 && !upper && !under {
			return false
		}
		if !upper && !digit && !under {
			return false
		}
	}
	return true
}

// reEnvHostHint extracts the service-name token from an env var shaped like
// ORDER_SERVICE_URL / ORDER_SERVICE_HOST / ORDER_BASE_URL / ORDER_HOST / ORDER_URL.
var reEnvHostHint = regexp.MustCompile(`(?i)^([A-Z0-9]+(?:_[A-Z0-9]+)*?)_(?:SERVICE_URL|SERVICE_HOST|BASE_URL|HOST|URL)$`)

// serviceHintFromEnvVar returns the lowercased, underscore-stripped service
// hint token from an env var name, or "" if the name does not match a known
// *_SERVICE_URL / *_HOST / *_BASE_URL shape.
func serviceHintFromEnvVar(varName string) string {
	m := reEnvHostHint.FindStringSubmatch(varName)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(strings.ReplaceAll(m[1], "_", ""))
}

// serviceNameMatchesHint reports whether svcName plausibly refers to hint —
// a case/separator-insensitive substring match in either direction (so
// "order" matches "order-service" and "orders" alike).
func serviceNameMatchesHint(svcName, hint string) bool {
	if hint == "" {
		return false
	}
	norm := strings.ToLower(svcName)
	norm = strings.NewReplacer("-", "", "_", "").Replace(norm)
	return strings.Contains(norm, hint) || strings.Contains(hint, norm)
}
