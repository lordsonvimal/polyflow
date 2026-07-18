package trace_ingest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// MapSpans maps parsed OTLP spans to flow records and ingest ledger entries.
//
// spans must be pre-sorted (ParseOTLPFile guarantees this). session names the
// capture session for provenance refs. ws provides the service-name mapping
// (evidence.runtime.service_names) and workspace-link hints for base_url_strip.
//
// Output ordering — deterministic per bug-class rule 2:
//   - flow records sorted by (kind, key, from_service, to_service)
//   - refs within each record sorted by (session, trace_id)
//   - ledger entries sorted by (trace_id, span_id)
func MapSpans(spans []Span, session string, ws *workspace.WorkspaceConfig) ([]FlowRecord, []IngestLedgerEntry) {
	wsServices := workspaceServiceSet(ws)
	serviceNames := runtimeServiceNames(ws)
	links := workspaceLinks(ws)
	sseRoutes := runtimeSSERoutes(ws)

	// Build span lookup by (traceID, spanID) for O(1) parent resolution.
	type traceSpanKey struct{ traceID, spanID string }
	byID := make(map[traceSpanKey]*Span, len(spans))
	for i := range spans {
		byID[traceSpanKey{spans[i].TraceID, spans[i].SpanID}] = &spans[i]
	}

	// Flow record aggregation by (kind, key, from_service, to_service).
	type flowKey struct{ kind, key, from, to string }
	flowByKey := make(map[flowKey]*FlowRecord)
	var flowOrder []flowKey // insertion-order slice for deterministic sort

	var ledger []IngestLedgerEntry

	// ledgered tracks spans that already produced a ledger entry, and
	// consumedClient tracks CLIENT spans consumed as a SERVER span's parent,
	// so the final exhaustiveness sweep never double-books and never drops
	// (trust contract: every span reaches a flow or the ledger).
	ledgered := make(map[*Span]bool)
	consumedClient := make(map[*Span]bool)

	addLedger := func(s *Span, reason string) {
		if ledgered[s] {
			return
		}
		ledgered[s] = true
		svc := resolveService(s.Service, serviceNames, wsServices)
		if svc == "" {
			svc = "unknown"
		}
		ledger = append(ledger, IngestLedgerEntry{
			Session: session,
			TraceID: s.TraceID,
			SpanID:  s.SpanID,
			Service: svc,
			Reason:  reason,
		})
	}

	addFlow := func(kind, key, from, to, causality string, ref FlowRef) {
		fk := flowKey{kind, key, from, to}
		if rec, exists := flowByKey[fk]; exists {
			// Dedup refs by (session, trace_id) — same trace can't add twice.
			rec.Refs = appendRef(rec.Refs, ref)
		} else {
			flowByKey[fk] = &FlowRecord{
				Kind:        contract.Kind(kind),
				Key:         key,
				FromService: from,
				ToService:   to,
				Causality:   causality,
				Refs:        []FlowRef{ref},
			}
			flowOrder = append(flowOrder, fk)
		}
	}

	// Walk spans: SERVER spans are the anchor; CLIENT parents are found by lookup.
	for i := range spans {
		sp := &spans[i]
		if sp.Kind != "SERVER" {
			continue // messaging spans are handled in the second pass below
		}

		// Service name mapping.
		toSvc := resolveService(sp.Service, serviceNames, wsServices)
		if toSvc == "" {
			addLedger(sp, "unknown_service")
			continue
		}

		// HTTP detection (SSE is a specialisation of HTTP and also carries
		// http.request.method — check HTTP attrs first, then decide kind).
		if !isHTTPSpan(sp) {
			addLedger(sp, "unsupported_span_kind")
			continue
		}

		method, rawPath, ok := extractHTTPInfo(sp)
		if !ok {
			addLedger(sp, "no_route_or_path")
			continue
		}

		// Determine caller (from_service) and causality.
		fromSvc := ""
		causality := "server_only"

		if sp.ParentSpanID != "" {
			parentKey := traceSpanKey{sp.TraceID, sp.ParentSpanID}
			if parent, found := byID[parentKey]; found &&
				parent.Kind == "CLIENT" && parent.Service != sp.Service {
				consumedClient[parent] = true
				mappedFrom := resolveService(parent.Service, serviceNames, wsServices)
				if mappedFrom != "" {
					fromSvc = mappedFrom
					causality = "parent_child"
				} else {
					// CLIENT is from an unknown service — ledger it, emit server_only.
					addLedger(parent, "unknown_service")
				}
			}
		}

		env := contract.NormalizeEnv{
			FromService: fromSvc,
			ToService:   toSvc,
			Links:       links,
		}

		ref := FlowRef{
			Session:    session,
			TraceID:    sp.TraceID,
			ObservedAt: int64(sp.StartUnixNano / 1_000_000_000),
			CodeFile:   sp.Attrs["code.filepath"],
			CodeFunc:   sp.Attrs["code.function"],
		}

		// SSE detection: content-type response header or workspace-listed route.
		// Long-lived duration is never the signal (see mapping table in plan doc).
		if isSSESpan(sp, rawPath, sseRoutes) {
			// SSE connection edge uses path-only key (no method prefix — SSE is
			// always GET and the static sse_endpoint edges key on path alone).
			sseKey, err := contract.NormalizeFields(
				[]string{rawPath},
				sseNormChain,
				env,
			)
			if err != nil {
				addLedger(sp, "no_route_or_path")
				continue
			}
			addFlow("sse", sseKey, fromSvc, toSvc, causality, ref)
			continue
		}

		// Build normalized HTTP channel key using the contract engine's normalizer
		// chain — identical to what static HTTP edges use, ensuring the join
		// key matches (bug-class rule 6 / divergent-normalization guard).
		key, err := contract.NormalizeFields(
			[]string{method, rawPath},
			httpNormChain,
			env,
		)
		if err != nil {
			addLedger(sp, "no_route_or_path")
			continue
		}

		addFlow("http", key, fromSvc, toSvc, causality, ref)
	}

	// ── Messaging spans (PRODUCER / CONSUMER) ──────────────────────────────────
	// Pass 1: collect producers and consumers, resolve service + key.
	type msgInfo struct {
		sp      *Span
		kind    contract.Kind
		key     string
		service string
	}
	var msgProducers, msgConsumers []msgInfo

	for i := range spans {
		sp := &spans[i]
		if sp.Kind != "PRODUCER" && sp.Kind != "CONSUMER" {
			continue
		}
		svc := resolveService(sp.Service, serviceNames, wsServices)
		if svc == "" {
			addLedger(sp, "unknown_service")
			continue
		}
		kind := messagingKind(sp.Attrs["messaging.system"])
		key, err := buildMessagingKey(sp, kind)
		if err != nil || key == "" {
			addLedger(sp, "no_route_or_path")
			continue
		}
		info := msgInfo{sp: sp, kind: kind, key: key, service: svc}
		switch {
		case isPublishOp(sp):
			msgProducers = append(msgProducers, info)
		case isConsumeOp(sp):
			msgConsumers = append(msgConsumers, info)
		default:
			addLedger(sp, "unsupported_span_kind")
		}
	}

	// Pass 2: build producer index by (kind, key) for key_match — multi-valued
	// (bug-class rule 1: never first-match).
	type msgChanKey struct{ kind, key string }
	producerByKey := make(map[msgChanKey][]msgInfo)
	for _, p := range msgProducers {
		k := msgChanKey{string(p.kind), p.key}
		producerByKey[k] = append(producerByKey[k], p)
	}
	matchedProd := make(map[*Span]bool)

	// Pass 3: match consumers — link-based causality first, key_match fallback.
	for _, c := range msgConsumers {
		ref := FlowRef{
			Session:    session,
			TraceID:    c.sp.TraceID,
			ObservedAt: int64(c.sp.StartUnixNano / 1_000_000_000),
		}
		// Link-based: span links reference the producer span's (traceId, spanId).
		linked := false
		for _, lk := range c.sp.Links {
			prod := byID[traceSpanKey{lk.TraceID, lk.SpanID}]
			if prod == nil || prod.Kind != "PRODUCER" {
				continue
			}
			fromSvc := resolveService(prod.Service, serviceNames, wsServices)
			if fromSvc == "" {
				addLedger(prod, "unknown_service")
				continue
			}
			matchedProd[prod] = true
			addFlow(string(c.kind), c.key, fromSvc, c.service, "link", ref)
			linked = true
			// No break — fan-out: every linked producer gets an edge.
		}
		if linked {
			continue
		}
		// Key-match fallback: any producer in the window publishing to the same
		// (kind, key) — fan-out across all of them (bug-class rule 1).
		k := msgChanKey{string(c.kind), c.key}
		prods := producerByKey[k]
		if len(prods) == 0 {
			addLedger(c.sp, "no_causality")
			continue
		}
		for _, p := range prods {
			matchedProd[p.sp] = true
			addFlow(string(c.kind), c.key, p.service, c.service, "key_match", ref)
		}
	}

	// Pass 4: producer-only observations — no consumer in-window, no fabricated edge.
	for _, p := range msgProducers {
		if matchedProd[p.sp] {
			continue
		}
		ref := FlowRef{
			Session:    session,
			TraceID:    p.sp.TraceID,
			ObservedAt: int64(p.sp.StartUnixNano / 1_000_000_000),
		}
		addFlow(string(p.kind), p.key, p.service, "", "key_match", ref)
	}

	// ── Exhaustiveness sweep ───────────────────────────────────────────────────
	// Every span must reach a flow record or the ledger (trust contract: no
	// silent drops). SERVER/PRODUCER/CONSUMER spans are fully handled by the
	// passes above. What remains:
	//   - CLIENT spans consumed as a SERVER span's parent → accounted for.
	//   - CLIENT spans whose server side was never captured (uninstrumented or
	//     external callee) → ledgered no_causality: the observation is real but
	//     polyflow will not guess the receiving service.
	//   - INTERNAL and unknown-kind spans → ledgered unsupported_span_kind.
	for i := range spans {
		sp := &spans[i]
		switch sp.Kind {
		case "SERVER", "PRODUCER", "CONSUMER":
			continue
		case "CLIENT":
			if consumedClient[sp] {
				continue
			}
			if isHTTPSpan(sp) {
				addLedger(sp, "no_causality")
			} else {
				addLedger(sp, "unsupported_span_kind")
			}
		default: // INTERNAL, unknown
			addLedger(sp, "unsupported_span_kind")
		}
	}

	// Sort and emit flow records (bug-class rule 2: never map-iteration order).
	flows := make([]FlowRecord, 0, len(flowOrder))
	for _, fk := range flowOrder {
		rec := flowByKey[fk]
		sortRefs(rec.Refs)
		flows = append(flows, *rec)
	}
	sort.Slice(flows, func(i, j int) bool {
		a, b := flows[i], flows[j]
		if string(a.Kind) != string(b.Kind) {
			return string(a.Kind) < string(b.Kind)
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.FromService != b.FromService {
			return a.FromService < b.FromService
		}
		return a.ToService < b.ToService
	})

	// Sort ledger entries (bug-class rule 2).
	sort.Slice(ledger, func(i, j int) bool {
		if ledger[i].TraceID != ledger[j].TraceID {
			return ledger[i].TraceID < ledger[j].TraceID
		}
		return ledger[i].SpanID < ledger[j].SpanID
	})

	return flows, ledger
}

// httpNormChain is the normalizer sequence applied to [method, path] fields
// for HTTP flow records — identical to the static HTTP contract rule chain so
// the join key is always compatible (divergent normalization is a silent miss).
var httpNormChain = []string{
	"case_fold", "url_to_path", "base_url_strip", "query_strip",
	"param_wildcard", "trim_slash",
}

// sseNormChain is the normalizer sequence applied to [path] for SSE connection
// flow records.  No method prefix: SSE is always GET, and static sse_endpoint
// edges key on path alone.
var sseNormChain = []string{
	"case_fold", "url_to_path", "query_strip",
	"param_wildcard", "trim_slash",
}

// FlowsToEdges converts a slice of FlowRecords to graph.Edges suitable for
// the evidence.Evidence returned by RuntimeProvider.Collect.
func FlowsToEdges(flows []FlowRecord) []FlowEdge {
	edges := make([]FlowEdge, 0, len(flows))
	for _, flow := range flows {
		edges = append(edges, FlowEdge{
			Flow:  flow,
			EdgeID: fmt.Sprintf("runtime:%s:%s:%s->%s", string(flow.Kind), flow.Key, flow.FromService, flow.ToService),
		})
	}
	return edges
}

// FlowEdge pairs a FlowRecord with its deterministic graph edge ID.
type FlowEdge struct {
	Flow   FlowRecord
	EdgeID string
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// resolveService maps a raw OTel service.name to a polyflow workspace service
// name. Returns "" when the name is unknown (not in the mapping and not a
// direct workspace service name).
func resolveService(raw string, serviceNames map[string]string, wsServices map[string]bool) string {
	name := raw
	if mapped, ok := serviceNames[raw]; ok {
		name = mapped
	}
	if wsServices[name] {
		return name
	}
	return ""
}

// isHTTPSpan reports whether the span carries any HTTP attribute.
func isHTTPSpan(sp *Span) bool {
	return sp.Attrs["http.request.method"] != "" || sp.Attrs["http.method"] != ""
}

// extractHTTPInfo returns the HTTP method and raw path/URL from a span.
// For SERVER spans, prefers http.route (already a route pattern) over url.path
// and http.target. For CLIENT spans, uses url.full (url_to_path normalizer
// extracts the path). Returns ok=false when either field is absent.
func extractHTTPInfo(sp *Span) (method, rawPath string, ok bool) {
	method = sp.Attrs["http.request.method"]
	if method == "" {
		method = sp.Attrs["http.method"]
	}
	if method == "" {
		return "", "", false
	}

	if sp.Kind == "SERVER" {
		rawPath = sp.Attrs["http.route"]
		if rawPath == "" {
			rawPath = sp.Attrs["url.path"]
		}
		if rawPath == "" {
			rawPath = sp.Attrs["http.target"]
		}
	} else {
		rawPath = sp.Attrs["url.full"]
	}

	if rawPath == "" {
		return "", "", false
	}
	return method, rawPath, true
}

// isSSESpan reports whether sp is an SSE connection span.  Detection uses the
// http.response.header.content-type attribute (new semconv) or the workspace
// sse_routes list.  Long-lived duration is NOT a detection signal.
func isSSESpan(sp *Span, rawPath string, sseRoutes []string) bool {
	ct := sp.Attrs["http.response.header.content-type"]
	if strings.Contains(strings.ToLower(ct), "text/event-stream") {
		return true
	}
	for _, route := range sseRoutes {
		if route == rawPath {
			return true
		}
	}
	return false
}

// runtimeSSERoutes returns the evidence.runtime.sse_routes list, or nil when
// not configured.
func runtimeSSERoutes(ws *workspace.WorkspaceConfig) []string {
	if ws == nil {
		return nil
	}
	return ws.Evidence.Runtime.SSERoutes
}

// workspaceServiceSet builds a set of declared workspace service names for O(1)
// lookup during span mapping.
func workspaceServiceSet(ws *workspace.WorkspaceConfig) map[string]bool {
	if ws == nil {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(ws.Services))
	for _, svc := range ws.Services {
		set[svc.Name] = true
	}
	return set
}

// runtimeServiceNames returns the evidence.runtime.service_names mapping, or
// an empty map when it is not configured.
func runtimeServiceNames(ws *workspace.WorkspaceConfig) map[string]string {
	if ws == nil || len(ws.Evidence.Runtime.ServiceNames) == 0 {
		return map[string]string{}
	}
	return ws.Evidence.Runtime.ServiceNames
}

// workspaceLinks returns the workspace links for NormalizeEnv (base_url_strip).
func workspaceLinks(ws *workspace.WorkspaceConfig) []workspace.Link {
	if ws == nil {
		return nil
	}
	return ws.Links
}

// appendRef appends ref to existing, deduped by (Session, TraceID).
func appendRef(existing []FlowRef, ref FlowRef) []FlowRef {
	for _, r := range existing {
		if r.Session == ref.Session && r.TraceID == ref.TraceID {
			return existing
		}
	}
	return append(existing, ref)
}

// sortRefs sorts refs in-place by (session, trace_id) for determinism.
func sortRefs(refs []FlowRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Session != refs[j].Session {
			return refs[i].Session < refs[j].Session
		}
		return refs[i].TraceID < refs[j].TraceID
	})
}

// ─── Messaging helpers ────────────────────────────────────────────────────────

// msgNormChain applies quote_strip only: messaging destination names are already
// lowercase strings; no URL normalisation is needed.
var msgNormChain = []string{"quote_strip"}

// messagingKind maps the OTel messaging.system attribute to a contract.Kind.
func messagingKind(system string) contract.Kind {
	switch strings.ToLower(system) {
	case "rabbitmq":
		return contract.KindAMQP
	case "kafka":
		return contract.KindKafka
	case "nats":
		return contract.KindNATS
	default:
		return contract.KindJob
	}
}

// buildMessagingKey constructs the normalised channel key for a messaging span.
// AMQP uses [destination, routing_key]; all other systems use [destination] only,
// mirroring the corresponding contract rule's key fields.
func buildMessagingKey(sp *Span, kind contract.Kind) (string, error) {
	dest := sp.Attrs["messaging.destination.name"]
	if dest == "" {
		return "", nil
	}
	var fields []string
	if kind == contract.KindAMQP {
		if rk := sp.Attrs["messaging.rabbitmq.destination.routing_key"]; rk != "" {
			fields = []string{dest, rk}
		} else {
			fields = []string{dest}
		}
	} else {
		fields = []string{dest}
	}
	return contract.NormalizeFields(fields, msgNormChain, contract.NormalizeEnv{})
}

// isPublishOp reports whether the span is a messaging publish/send operation.
// Accepts both new semconv (messaging.operation.type) and old (messaging.operation).
func isPublishOp(sp *Span) bool {
	op := sp.Attrs["messaging.operation.type"]
	if op == "" {
		op = sp.Attrs["messaging.operation"]
	}
	return op == "publish" || op == "send"
}

// isConsumeOp reports whether the span is a messaging process/receive operation.
func isConsumeOp(sp *Span) bool {
	op := sp.Attrs["messaging.operation.type"]
	if op == "" {
		op = sp.Attrs["messaging.operation"]
	}
	return op == "process" || op == "receive"
}
