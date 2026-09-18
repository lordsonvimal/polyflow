package factpipe

import (
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_amqp_handshake.go is the "amqp_handshake" hub provider (see hub.go) —
// the Tier FX migration of internal/linker/amqp_handshake.go's retired
// LinkAMQPHandshake (K.6 step 3).
//
// Unlike hints/config_baseurl, the missing input here isn't external
// config — it's a per-service registry the hub itself builds from the
// SAME node slice it's walking (queue-name methods keyed by
// meta["queue_key"], joined against amqp_field_pair declarations by
// service+method), then a three-way branch (0/1/N declaring services) per
// unresolved broker-field call site. That branch structure — different
// Meta overlays, a conditional Label override, and an unresolved-ledger
// row for two of the three outcomes — is exactly the sequential-decision
// shape hub.go already handles (see hub_hints.go); no new emit primitive
// was needed except `patch:` growing an optional `label:` override (see
// emit.go's patchSpec — the resolved case's "only overwrite a still-
// dynamic label" behavior can't be expressed as a static per-relation
// rule, so the hub decides case-by-case and reports the winning value,
// same "empty means don't touch" convention as Meta).
func init() { RegisterHub("amqp_handshake", amqpHandshakeHub) }

const (
	amqpResolvedPatchPred  = "amqp_handshake_resolved_patch"  // (ID, QueueName, Label)
	amqpAmbiguousPatchPred = "amqp_handshake_ambiguous_patch" // (ID, KeyCandidates)
	amqpUnresolvedPred     = "amqp_handshake_unresolved_fact" // (Service, File, Line, Field, Kind)
)

type amqpHandshakeDecl struct {
	service string
	queue   string
}

func amqpHandshakeHub(nodes []graph.Node, _ []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig, _ []graph.UnresolvedRef) []Fact {
	decls := amqpCollectHandshakeDeclarations(nodes)
	if len(decls) == 0 {
		return nil
	}

	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		field := strings.TrimSpace(n.Meta["broker_field"])
		if field == "" || n.Meta["queue_name"] != "" || n.Meta["key_dynamic"] != "true" {
			continue
		}
		// A reading site in a spec is a fixture, not a live handshake
		// endpoint — the same exclusion the retired InferLinks applied.
		if graph.IsTestFilePath(n.File) {
			continue
		}

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

		origin := Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "amqp_handshake"}
		switch len(queues) {
		case 0:
			out = append(out, Fact{
				Pred:   amqpUnresolvedPred,
				Args:   []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(field), Str("amqp_handshake_unresolved")},
				Origin: origin,
			})
		case 1:
			label := ""
			if n.Label == "dynamic" || n.Label == "" {
				label = queues[0]
			}
			out = append(out, Fact{
				Pred:   amqpResolvedPatchPred,
				Args:   []Atom{Node(n.ID), Str(queues[0]), Str(label)},
				Origin: origin,
			})
		default:
			// Two services declare the same field with different queues.
			// Both are candidates and the engine fans out to each (bug-class
			// #1 — never pick one); the ledger records that the ambiguity is
			// real rather than a resolution failure.
			out = append(out, Fact{
				Pred:   amqpAmbiguousPatchPred,
				Args:   []Atom{Node(n.ID), Str(contract.MarshalKeyCandidates(queues))},
				Origin: origin,
			})
			out = append(out, Fact{
				Pred:   amqpUnresolvedPred,
				Args:   []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(field), Str("amqp_handshake_ambiguous")},
				Origin: origin,
			})
		}
	}
	return out
}

// amqpCollectHandshakeDeclarations ports collectHandshakeDeclarations
// verbatim — resolves every amqp_field_pair declaration to the queue its
// value method builds, using a per-service registry of queue-name methods.
// Per-service because queue-name methods are ordinary names
// (`resolved_queue_name`, `workspace_events_queue`) that different repos
// define differently — a workspace-global table would be the same class of
// bug a prior Ruby class-name lookup had.
func amqpCollectHandshakeDeclarations(nodes []graph.Node) map[string][]amqpHandshakeDecl {
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

	decls := map[string][]amqpHandshakeDecl{}
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
		// One method, one queue, or it is not a declaration. A queue-name
		// method with several literals is a branch; which one this field
		// carries is exactly what static reading cannot say.
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
		decls[field] = append(decls[field], amqpHandshakeDecl{service: n.Service, queue: queue})
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
