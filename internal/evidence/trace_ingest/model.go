// Package trace_ingest is the R.0–R.5 runtime evidence provider.
// It parses OTLP traces (JSON, JSONL, binary protobuf), maps spans to
// flow records in contract-engine channel-key vocabulary, and emits
// verified edges through the F.0 evidence substrate.
package trace_ingest

import "github.com/lordsonvimal/polyflow/internal/contract"

// Span is the normalized internal span representation.
// Only attributes listed in the mapping tables are retained at parse time
// (PII boundary — everything else is dropped here).
type Span struct {
	TraceID, SpanID, ParentSpanID string
	Kind                          string // "CLIENT"|"SERVER"|"PRODUCER"|"CONSUMER"|"INTERNAL"
	Service                       string // resource attr service.name (raw, pre-mapping)
	Name                          string
	StartUnixNano, EndUnixNano    uint64
	Links                         []SpanLink
	Attrs                         map[string]string // ALLOWLISTED attrs only
}

// SpanLink is a reference to another span (for messaging causality).
type SpanLink struct{ TraceID, SpanID string }

// FlowRecord is the mapper's output (R.1+) — channel-granular, in
// contract-engine key vocabulary. This is what reconciliation joins.
type FlowRecord struct {
	Kind        contract.Kind
	Key         string // normalized channel key, e.g. "GET /games/*"
	FromService string // "" when the caller was never observed (server_only)
	ToService   string
	Causality   string // "parent_child" | "link" | "key_match" | "server_only"
	Refs        []FlowRef
}

// FlowRef is the provenance one observation contributes; it becomes the
// runtime SourceRef ("<session>/<trace_id>") on the fused edge.
type FlowRef struct {
	Session    string
	TraceID    string
	ObservedAt int64
	CodeFile   string // from code.filepath — presence upgrades granularity to "site"
	CodeFunc   string // from code.function
}

// IngestLedgerEntry persists through the existing unresolved_refs store as a
// graph.UnresolvedRef with this pinned mapping:
//
//	Service: mapped polyflow service ("unknown" when unmapped)
//	File:    ".polyflow/captures/<session>/spans.otlp.json"
//	Line:    0
//	Name:    "<trace_id>/<span_id>"
//	Kind:    "otlp_" + Reason   (e.g. "otlp_unknown_service")
type IngestLedgerEntry struct {
	Session, TraceID, SpanID string
	Reason                   string // unknown_service | no_route_or_path |
	// unsupported_span_kind | malformed | no_causality
}
