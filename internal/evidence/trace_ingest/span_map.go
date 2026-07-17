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

	addLedger := func(s *Span, reason string) {
		ledger = append(ledger, IngestLedgerEntry{
			Session: session,
			TraceID: s.TraceID,
			SpanID:  s.SpanID,
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
			continue
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
