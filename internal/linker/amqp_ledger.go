package linker

// amqp_ledger.go is the shared ledger-retraction infra the retired
// LinkAMQPHandshake (K.6 step 3) used to own outright. Tier FX migrated the
// resolution itself to patterns/generic/amqp_handshake.yaml +
// internal/factpipe/hub_amqp_handshake.go; these three symbols stayed Go
// because internal/indexer/link_passes.go's new dedicated pass (and the
// amqp_handshake_pipeline_test.go fixture tests) still call them directly —
// same "rename off the migrated name, keep the shared infra" shape as
// ruby_http_hosts.go → ruby_host_registry.go.

import (
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

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
