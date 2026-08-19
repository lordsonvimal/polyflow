package linker

import (
	"fmt"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkAMQPMessageTypeDispatch is the AH follow-up: the AMQP analogue of
// Tier WD's WS-message-type dispatch, and the mechanism that actually answers
// "what breaks if I change this message's shape" — distinct from, and
// unblocked by, the queue-name handshake LinkAMQPHandshake resolves.
//
// Like the handshake, no literal is shared across repos: the producer sets
// `message_type: MT_CREATE_USER` in the publish payload and the consumer
// dispatches on `message_hash["message_type"]` against a `when MT_CREATE_USER`
// branch. Each repo defines its own `MT_CREATE_USER = "..."` in its own
// messaging-constants file, so — exactly like amqp_handshake's broker_field —
// the constant NAME is the one static token both sides agree on. Resolving it
// to its literal value isn't needed to make the join meaningful: the shared
// name is what proves it's the same message, so this pass joins directly on
// it, in the same spirit as the handshake but without a value-resolution step
// since neither side ever captures a literal to resolve to.
//
// A name-only cross-repo match is never `static` confidence, matching
// amqp_handshake's own trust posture — agreement on a name is not proof of
// reachability.
func LinkAMQPMessageTypeDispatch(nodes []graph.Node) []graph.Edge {
	type site struct {
		service, id string
	}
	// service -> message_type constant name -> producer node IDs
	producers := map[string]map[string][]string{}
	// message_type constant name -> consumer dispatch sites
	consumers := map[string][]site{}

	for i := range nodes {
		n := &nodes[i]
		if graph.IsTestFilePath(n.File) {
			continue
		}
		switch n.Meta["pattern"] {
		case "amqp_message_type_pair":
			mt := n.Meta["message_type"]
			if mt == "" {
				continue
			}
			if producers[n.Service] == nil {
				producers[n.Service] = map[string][]string{}
			}
			producers[n.Service][mt] = append(producers[n.Service][mt], n.ID)
		case "amqp_message_type_dispatch":
			mt := n.Meta["message_type"]
			if mt == "" {
				continue
			}
			consumers[mt] = append(consumers[mt], site{n.Service, n.ID})
		}
	}
	if len(producers) == 0 || len(consumers) == 0 {
		return nil
	}

	var edges []graph.Edge
	for svc, byType := range producers {
		for mt, pubIDs := range byType {
			for _, c := range consumers[mt] {
				// A handshake crosses the repo boundary by definition; a
				// same-service match means the dispatch was already
				// resolvable in-repo and isn't this pass's concern.
				if c.service == svc {
					continue
				}
				for _, pubID := range pubIDs {
					edges = append(edges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:amqp_message_type", pubID, c.id),
						From:       pubID,
						To:         c.id,
						Type:       graph.EdgeTypePublishes,
						Confidence: graph.ConfidencePartial,
						Meta: map[string]string{
							"resolved_via": "amqp_message_type",
							"message_type": mt,
						},
					})
				}
			}
		}
	}
	// Map iteration filled these; sort so the pass is deterministic
	// regardless of node order (bug-class #2).
	sort.Slice(edges, func(a, b int) bool {
		if edges[a].From != edges[b].From {
			return edges[a].From < edges[b].From
		}
		return edges[a].To < edges[b].To
	})
	return edges
}
