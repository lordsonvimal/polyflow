// Package config_resolve is the F.3 evidence provider that reads env files,
// Kubernetes manifests, and Terraform tfvars to resolve dynamic endpoint/topic
// strings that the static pipeline could not — upgrading dynamic_<kind> ledger
// entries to resolved channel-key edges tagged source=config.
//
// Resolution semantics (from evidence-fusion-plan.md F.3):
//   (a) Raw config values have surrounding quotes and whitespace stripped before
//       channel-key construction (bug-class rule 6).
//   (b) When one variable resolves to different values per environment/overlay,
//       one edge is emitted per value (fan-out, bug-class rule 1).
//   (c) Env vars referenced in code but absent from all scanned config files
//       stay in the ledger with reason config_not_found; absence of config is
//       not license to guess (trust contract: recall over precision).
package config_resolve

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/configsrc"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// ConfigProvider implements evidence.Provider for config/IaC files.
// Constructed with the static-pipeline nodes and unresolved refs so it can
// identify dynamic producers and clear their original ledger entries.
type ConfigProvider struct {
	nodes []graph.Node
	unres []graph.UnresolvedRef
}

// NewConfigProvider creates a ConfigProvider from the static-pipeline output.
// Both slices are held by reference; callers must not modify them after
// construction.
func NewConfigProvider(nodes []graph.Node, unres []graph.UnresolvedRef) *ConfigProvider {
	return &ConfigProvider{nodes: nodes, unres: unres}
}

func (p *ConfigProvider) Name() string { return "config" }

// Collect discovers config files for each service, extracts variable bindings,
// and resolves dynamic producer nodes to concrete channel-key edges.
//
// Fan-out (bug-class rule 1): one variable → N environment values → N edges.
// Determinism (bug-class rule 2): config files processed in sorted order; edge
// IDs derived from (service, kind, varName, value) not counters.
func (p *ConfigProvider) Collect(_ context.Context, ws *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	if ws == nil {
		return evidence.Evidence{}, nil
	}

	// Build the set of dynamic nodes indexed by (service, file, line, name)
	// so we can correlate with UnresolvedRef entries.
	type dynamicKey struct{ service, file, name string; line int }
	type dynamicEntry struct {
		node   graph.Node
		unrRef graph.UnresolvedRef
	}
	dynByKey := make(map[dynamicKey]*dynamicEntry)

	// Index dynamic nodes.
	for _, n := range p.nodes {
		if n.Meta["key_dynamic"] != "true" {
			continue
		}
		raw := n.Meta["key_dynamic_raw"]
		k := dynamicKey{n.Service, n.File, raw, n.Line}
		if dynByKey[k] == nil {
			dynByKey[k] = &dynamicEntry{node: n}
		}
	}
	// Match unresolved refs to their dynamic nodes (same service+file+line+name).
	for _, u := range p.unres {
		if !strings.HasPrefix(u.Kind, "dynamic_") {
			continue
		}
		k := dynamicKey{u.Service, u.File, u.Name, u.Line}
		if e := dynByKey[k]; e != nil {
			e.unrRef = u
		}
	}

	// For each service, load config values from all three sources.
	svcMap := make(map[string]map[string][]configsrc.Value, len(ws.Services))
	for _, svc := range ws.Services {
		svcMap[svc.Name] = configsrc.Load(svc.Path)
	}

	// Stable iteration over dynamic entries (bug-class rule 2).
	type entrySlot struct {
		key dynamicKey
		e   *dynamicEntry
	}
	slots := make([]entrySlot, 0, len(dynByKey))
	for k, e := range dynByKey {
		slots = append(slots, entrySlot{k, e})
	}
	sort.Slice(slots, func(i, j int) bool {
		ki, kj := slots[i].key, slots[j].key
		if ki.service != kj.service {
			return ki.service < kj.service
		}
		if ki.file != kj.file {
			return ki.file < kj.file
		}
		if ki.line != kj.line {
			return ki.line < kj.line
		}
		return ki.name < kj.name
	})

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	var clears []graph.UnresolvedRef
	seenEdge := make(map[string]bool)

	for _, sl := range slots {
		n := sl.e.node
		raw := n.Meta["key_dynamic_raw"]

		varName := extractEnvVarName(raw)

		// All values this variable takes across the service's config sources,
		// already merged and de-duplicated in source order by configsrc.Load.
		var resolved []configsrc.Value
		if varName != "" {
			resolved = svcMap[n.Service][varName]
		}

		// Signal that we have handled this dynamic entry regardless of outcome.
		if sl.e.unrRef.Kind != "" {
			clears = append(clears, sl.e.unrRef)
		}

		if len(resolved) == 0 {
			// No config binding found; keep in ledger with config_not_found.
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service,
				File:    n.File,
				Line:    n.Line,
				Name:    raw,
				Kind:    "config_not_found",
			})
			continue
		}

		// Emit one edge per resolved value (fan-out, bug-class rule 1).
		edgeType := edgeTypeForNode(n)
		for _, rv := range resolved {
			label, err := buildChannelLabel(n, rv.Value, edgeType)
			if err != nil || label == "" {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
					Name:    raw,
					Kind:    "config_not_found",
				})
				continue
			}

			edgeID := fmt.Sprintf("config:%s:%s:%s:%s", n.Service, string(edgeType), varName, label)
			if seenEdge[edgeID] {
				continue
			}
			seenEdge[edgeID] = true

			edges = append(edges, graph.Edge{
				ID:    edgeID,
				From:  n.Service,
				To:    "",
				Type:  edgeType,
				Label: label,
				Sources: []graph.SourceRef{{
					Provider:   "config",
					Confidence: graph.ConfidenceDeclared,
					Ref:        rv.Ref,
				}},
			})
		}
	}

	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })

	return evidence.Evidence{
		Edges:            edges,
		Unresolved:       unresolved,
		ClearsUnresolved: clears,
	}, nil
}

// edgeTypeForNode returns the most specific edge type for a dynamic producer.
func edgeTypeForNode(n graph.Node) graph.EdgeType {
	switch n.Type {
	case graph.NodeTypeHTTPClient:
		return graph.EdgeTypeHTTPCall
	case graph.NodeTypePublisher:
		switch n.Meta["kind"] {
		case "kafka":
			return graph.EdgeTypeKafkaPublish
		case "nats":
			return graph.EdgeTypeNATSPublish
		case "redis":
			return graph.EdgeTypeRedisPublish
		default:
			return graph.EdgeTypePublishes
		}
	case graph.NodeTypeWorker:
		return graph.EdgeTypeJobEnqueue
	default:
		return graph.EdgeTypePublishes
	}
}

// httpNormsConfig is the normalizer chain for config-resolved HTTP paths,
// identical to the chain the static producer pipeline uses.
var httpNormsConfig = []string{"case_fold", "url_to_path", "query_strip", "param_wildcard", "trim_slash"}

// buildChannelLabel normalizes a resolved config value into the channel key
// used by the contract-engine join. For HTTP the method is taken from node
// meta; for broker/job channels the resolved value is used as-is (topic
// names need no normalization).
func buildChannelLabel(n graph.Node, resolvedVal string, et graph.EdgeType) (string, error) {
	switch et {
	case graph.EdgeTypeHTTPCall:
		method := strings.ToLower(n.Meta["method"])
		if method == "" {
			method = "get"
		}
		label, err := contract.NormalizeFields(
			[]string{method, resolvedVal},
			httpNormsConfig,
			contract.NormalizeEnv{},
		)
		if err != nil {
			return "", err
		}
		return label, nil
	default:
		return resolvedVal, nil
	}
}

// envVarPattern matches common patterns that reference env variables in code.
// Groups: 1 = bare uppercase name, 2 = quoted name inside function/index.
var envVarFromCall = regexp.MustCompile(`(?i)(?:os\.getenv|getenv|process\.env\.|ENV\[|ENV\.fetch\(|os\.environ(?:\.get)?\[?|System\.getenv)\(?['"]?([A-Z_][A-Z0-9_]*)['"]?`)

// extractEnvVarName attempts to derive a bare environment variable name from a
// raw key_dynamic_raw expression. Returns "" when no match is found — the
// provider then emits config_not_found rather than guessing.
func extractEnvVarName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Try pattern matches first (os.Getenv / process.env / etc.).
	if m := envVarFromCall.FindStringSubmatch(raw); len(m) >= 2 {
		return m[1]
	}
	// Bare SCREAMING_SNAKE_CASE identifier → treat directly as env var name.
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
