package linker

import (
	"sort"
	"strconv"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkAMQPHandshake is Tier K.6 step 3 — the cross-service half of the AMQP
// registration handshake.
//
// When a queue name is negotiated at runtime rather than checked in, no literal
// is shared by the two repos. nextGen hands the agent a per-organization queue
// name in a REST registration response, keyed by a field symbol:
//
//	amqp_progress_events_queue_name: cdr_progress_events_queue(organization)
//
// and nextGen-CDR-Agent reads it back out of the config that response filled:
//
//	CONFIG[organization_name]&.dig(:amqp_progress_events_queue_name)
//
// The value differs per tenant, so value matching is impossible — but the
// declaring side names the *method* that builds the queue, and that method
// resolves to a literal (`cdr_progress_events`), which is the same literal the
// declaring service's own worker consumes. So the field symbol is a bridge: it
// carries the resolved queue name across the repo boundary to the publish site
// that only knew the symbol.
//
//	nextGen-CDR-Agent            nextGen
//	channel.queue(dynamic)  ~~~  amqp_progress_events_queue_name:  →  "cdr_progress_events"
//	  key_dynamic                  cdr_progress_events_queue(org)        ↑
//	  broker_field ─────────────── broker_field                          │
//	  queue_name := ─────────────────────────────────────────────────────┘
//
// This pass only *resolves the key*; it emits no edges. Once the publish site
// carries a queue_name the existing queue_name contract in contracts/amqp.yaml
// joins it to the consumer, which is why the plan's instruction not to fork a
// second queue matcher is respected literally.
//
// A resolution made this way is never `static`: the field symbol proves the two
// services agreed on a name, not that this call site is reachable from that
// worker in production. `confidence_ceiling` caps every edge the key produces at
// `partial`, per plan-14 trust soundness — only runtime or config evidence may
// promote it.
//
// nodes is mutated in place, matching the other pre-engine enrichment passes
// (EnrichRouteGroups, EnrichAliases): the caller hands in a working copy, so the
// persisted node keeps the honest `key_dynamic` its own source line shows.
// The second return value is the set of call sites this pass resolved, keyed by
// HandshakeSiteKey. Earlier passes ledgered those very lines as unresolvable —
// correctly, since nothing in their own repo names the queue — so leaving the
// entries in place would have polyflow assert an edge and deny it in the same
// index. DropResolvedRefs retracts them.
func LinkAMQPHandshake(nodes []graph.Node) ([]graph.UnresolvedRef, map[string]bool) {
	decls := collectHandshakeDeclarations(nodes)
	if len(decls) == 0 {
		return nil, nil
	}

	resolved := map[string]bool{}
	var unresolved []graph.UnresolvedRef
	for i := range nodes {
		n := &nodes[i]
		field := strings.TrimSpace(n.Meta["broker_field"])
		if field == "" || n.Meta["queue_name"] != "" || n.Meta["key_dynamic"] != "true" {
			continue
		}
		// A reading site in a spec is a fixture, not a live handshake endpoint —
		// the same exclusion InferLinks applies to broker_field overlap.
		if graph.IsTestFilePath(n.File) {
			continue
		}

		// The declaring service must not be this one. A handshake crosses the
		// repo boundary by definition; a same-service match would mean the queue
		// name was resolvable in-repo, which is Tier 2's job and would mask the
		// real cross-repo link behind a local shortcut.
		var queues []string
		seen := map[string]bool{}
		for _, d := range decls[field] {
			if d.service == n.Service || seen[d.queue] {
				continue
			}
			seen[d.queue] = true
			queues = append(queues, d.queue)
		}
		sort.Strings(queues)

		switch len(queues) {
		case 0:
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: field, Kind: "amqp_handshake_unresolved",
			})
		case 1:
			delete(n.Meta, "key_dynamic")
			delete(n.Meta, "key_dynamic_raw")
			n.Meta["queue_name"] = queues[0]
			n.Meta["key_resolved_via"] = "amqp_handshake"
			n.Meta["confidence_ceiling"] = graph.ConfidencePartial
			if n.Label == "dynamic" || n.Label == "" {
				n.Label = queues[0]
			}
			resolved[HandshakeSiteKey(n.Service, n.File, n.Line)] = true
		default:
			// Two services declare the same field with different queues. Both
			// are candidates and the engine fans out to each (bug-class #1 —
			// never pick one); the ledger records that the ambiguity is real
			// rather than a resolution failure.
			n.Meta["key_candidates"] = contract.MarshalKeyCandidates(queues)
			n.Meta["key_resolved_via"] = "amqp_handshake"
			n.Meta["confidence_ceiling"] = graph.ConfidencePartial
			n.Label = "branch_enum"
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: field, Kind: "amqp_handshake_ambiguous",
			})
		}
	}
	return unresolved, resolved
}

// HandshakeSiteKey identifies one source line across the passes that disagree
// about it.
func HandshakeSiteKey(service, file string, line int) string {
	return service + "\x00" + file + "\x00" + strconv.Itoa(line)
}

// DropResolvedRefs removes ledger entries whose line a later pass resolved. A
// ledger entry is a claim that polyflow tried and failed (bug-class #12); once
// the handshake supplies the queue name the claim is simply false, and an agent
// reading `status --unresolved` would go hand-verify a link that is already in
// the graph.
//
// Only the queue-name kinds are retracted. A different unresolved clue on the
// same line — an unresolved mixin, say — is untouched, because this pass says
// nothing about it.
func DropResolvedRefs(refs []graph.UnresolvedRef, resolved map[string]bool) []graph.UnresolvedRef {
	if len(resolved) == 0 || len(refs) == 0 {
		return refs
	}
	out := refs[:0]
	for _, r := range refs {
		if handshakeRetractableKind(r.Kind) && resolved[HandshakeSiteKey(r.Service, r.File, r.Line)] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// handshakeRetractableKind lists the ledger kinds that mean "this call site's
// queue/config key could not be resolved" — exactly the claim a completed
// handshake falsifies.
func handshakeRetractableKind(kind string) bool {
	switch kind {
	case "config_not_found", "dynamic_queue", "dynamic_url", "amqp_handshake_unresolved":
		return true
	default:
		return false
	}
}

// handshakeDecl is one service's claim that a handshake field carries a
// particular queue name.
type handshakeDecl struct {
	service string
	queue   string
}

// collectHandshakeDeclarations resolves every `amqp_field_pair` declaration to
// the queue its value method builds, using a per-service registry of queue-name
// methods. The registry is per service because queue-name methods are ordinary
// names (`resolved_queue_name`, `workspace_events_queue`) that different repos
// define differently — a workspace-global table is the bug K.7a fixed for Ruby
// class names, and it would be the same bug here.
func collectHandshakeDeclarations(nodes []graph.Node) map[string][]handshakeDecl {
	// service → method name → queue keys it resolves to.
	queueMethods := map[string]map[string]map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction {
			continue
		}
		key := n.Meta["queue_key"]
		if key == "" || graph.IsTestFilePath(n.File) {
			continue
		}
		if queueMethods[n.Service] == nil {
			queueMethods[n.Service] = map[string]map[string]bool{}
		}
		if queueMethods[n.Service][n.Label] == nil {
			queueMethods[n.Service][n.Label] = map[string]bool{}
		}
		queueMethods[n.Service][n.Label][key] = true
	}

	decls := map[string][]handshakeDecl{}
	seen := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.Meta["pattern"] != "amqp_field_pair" || graph.IsTestFilePath(n.File) {
			continue
		}
		field := strings.TrimSpace(n.Meta["broker_field"])
		method := n.Meta["queue_method"]
		if field == "" || method == "" {
			continue
		}
		keys := queueMethods[n.Service][method]
		// One method, one queue, or it is not a declaration. A queue-name method
		// with several literals is a branch; which one this field carries is
		// exactly what static reading cannot say.
		if len(keys) != 1 {
			continue
		}
		var queue string
		for k := range keys {
			queue = k
		}
		dedup := field + "\x00" + n.Service + "\x00" + queue
		if seen[dedup] {
			continue
		}
		seen[dedup] = true
		decls[field] = append(decls[field], handshakeDecl{service: n.Service, queue: queue})
	}

	// Map iteration filled these slices; sort so the pass is deterministic
	// regardless of node order (bug-class #2).
	for field := range decls {
		d := decls[field]
		sort.Slice(d, func(a, b int) bool {
			if d[a].service != d[b].service {
				return d[a].service < d[b].service
			}
			return d[a].queue < d[b].queue
		})
	}
	return decls
}
