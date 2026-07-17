package trace_ingest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// attrAllowlist is the exhaustive set of OTel attribute keys retained at
// parse time. Everything else is discarded here — this is the PII/secrets
// boundary. The set is derived from the span→flow mapping tables in the
// runtime-flow plan.
var attrAllowlist = map[string]bool{
	// HTTP (new semconv)
	"http.request.method": true,
	"url.full":            true,
	"url.path":            true,
	"http.route":          true,
	"http.response.header.content-type": true,
	// HTTP (old semconv — accepted in both directions per semconv drift rule)
	"http.method": true,
	"http.target": true,
	// Messaging (new semconv)
	"messaging.system":              true,
	"messaging.operation.type":      true,
	"messaging.destination.name":    true,
	"messaging.rabbitmq.destination.routing_key": true,
	// Messaging (old semconv)
	"messaging.operation": true,
	// Code attribution — presence upgrades verified_granularity to "site"
	"code.filepath": true,
	"code.function": true,
}

// spanKindString maps the OTLP integer kind to our string representation.
// JSON spans carry kind as an int; proto spans carry a typed enum.
func spanKindString(k int) string {
	switch k {
	case 2:
		return "SERVER"
	case 3:
		return "CLIENT"
	case 4:
		return "PRODUCER"
	case 5:
		return "CONSUMER"
	default:
		return "INTERNAL"
	}
}

// ParseOTLPFile reads an OTLP trace file (JSON, JSONL, or binary protobuf)
// and returns the normalized spans, sorted deterministically by
// (trace_id, start_unix_nano, span_id) — never map order (bug-class rule 2).
//
// Auto-detection: if the first non-whitespace byte is '{', the file is JSON
// (either a single ExportTraceServiceRequest document or JSONL — one per
// line); otherwise it is treated as binary protobuf.
//
// Only attributes in attrAllowlist are retained on each Span; all other
// attribute values are silently dropped at this boundary (PII guard).
// A metrics-only OTLP file (no trace spans) returns an empty slice and nil
// error.
func ParseOTLPFile(path string) ([]Span, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("otlp: read %s: %w", path, err)
	}
	return ParseOTLPBytes(data)
}

// ParseOTLPBytes is the byte-slice variant of ParseOTLPFile. Callers that
// already have the data in memory (e.g. the OTLP/HTTP receiver) use this
// directly to avoid an extra copy.
func ParseOTLPBytes(data []byte) ([]Span, error) {
	if len(data) == 0 {
		return nil, nil
	}
	// Trim leading whitespace to find the first meaningful byte.
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, nil
	}
	var spans []Span
	var err error
	if trimmed[0] == '{' {
		spans, err = parseJSON(data)
	} else {
		spans, err = parseProto(data)
	}
	if err != nil {
		return nil, err
	}
	sortSpans(spans)
	return spans, nil
}

// ─── JSON parsing ─────────────────────────────────────────────────────────────

// otlpJSONRequest mirrors ExportTraceServiceRequest for encoding/json.
// Hex-string trace/span IDs (the OTLP/JSON format) are preserved as strings
// and validated by the parser. startTimeUnixNano / endTimeUnixNano arrive as
// quoted decimal uint64 strings in the OTLP JSON format.
type otlpJSONRequest struct {
	ResourceSpans []jsonResourceSpan `json:"resourceSpans"`
}

type jsonResourceSpan struct {
	Resource   jsonResource    `json:"resource"`
	ScopeSpans []jsonScopeSpan `json:"scopeSpans"`
}

type jsonResource struct {
	Attributes []jsonKV `json:"attributes"`
}

type jsonScopeSpan struct {
	Spans []jsonSpan `json:"spans"`
}

type jsonSpan struct {
	TraceID           string    `json:"traceId"`
	SpanID            string    `json:"spanId"`
	ParentSpanID      string    `json:"parentSpanId"`
	Name              string    `json:"name"`
	Kind              int       `json:"kind"`
	StartTimeUnixNano jsonUint64 `json:"startTimeUnixNano"`
	EndTimeUnixNano   jsonUint64 `json:"endTimeUnixNano"`
	Attributes        []jsonKV  `json:"attributes"`
	Links             []jsonLink `json:"links"`
}

// jsonUint64 accepts both a bare number and a quoted decimal string
// (the OTLP JSON format uses quoted strings to avoid float64 precision loss).
type jsonUint64 uint64

func (u *jsonUint64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*u = jsonUint64(v)
	return nil
}

type jsonKV struct {
	Key   string      `json:"key"`
	Value jsonAnyValue `json:"value"`
}

type jsonAnyValue struct {
	StringValue string `json:"stringValue"`
}

type jsonLink struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

// parseJSON handles both single ExportTraceServiceRequest JSON documents and
// JSONL (multiple top-level JSON objects, one per line). json.Decoder.More()
// correctly handles both because it stops at EOF.
func parseJSON(data []byte) ([]Span, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var spans []Span
	for dec.More() {
		var req otlpJSONRequest
		if err := dec.Decode(&req); err != nil {
			return nil, fmt.Errorf("otlp: parse JSON: %w", err)
		}
		for _, rs := range req.ResourceSpans {
			svc := resourceServiceName(rs.Resource.Attributes)
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					spans = append(spans, convertJSONSpan(s, svc))
				}
			}
		}
	}
	return spans, nil
}

func resourceServiceName(attrs []jsonKV) string {
	for _, kv := range attrs {
		if kv.Key == "service.name" {
			return kv.Value.StringValue
		}
	}
	return ""
}

func convertJSONSpan(s jsonSpan, service string) Span {
	sp := Span{
		TraceID:       s.TraceID,
		SpanID:        s.SpanID,
		ParentSpanID:  s.ParentSpanID,
		Kind:          spanKindString(s.Kind),
		Service:       service,
		Name:          s.Name,
		StartUnixNano: uint64(s.StartTimeUnixNano),
		EndUnixNano:   uint64(s.EndTimeUnixNano),
	}
	// Collect allowlisted attributes only.
	allowed := make(map[string]string)
	for _, kv := range s.Attributes {
		if attrAllowlist[kv.Key] && kv.Value.StringValue != "" {
			allowed[kv.Key] = kv.Value.StringValue
		}
	}
	if len(allowed) > 0 {
		sp.Attrs = allowed
	}
	for _, l := range s.Links {
		sp.Links = append(sp.Links, SpanLink{TraceID: l.TraceID, SpanID: l.SpanID})
	}
	return sp
}

// ─── Protobuf parsing ─────────────────────────────────────────────────────────

// parseProto decodes a binary OTLP ExportTraceServiceRequest. We parse the
// outer envelope with protowire (field 1 = repeated ResourceSpans) to avoid
// importing the collector/trace/v1 package which carries gRPC+gateway deps;
// each ResourceSpans byte slice is then fully unmarshaled with proto.Unmarshal
// using the official go.opentelemetry.io/proto/otlp/trace/v1 types.
func parseProto(data []byte) ([]Span, error) {
	b := data
	var spans []Span
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("otlp: proto: malformed tag (err=%d)", n)
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			// resource_spans field (field number 1 in ExportTraceServiceRequest)
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return nil, fmt.Errorf("otlp: proto: malformed resource_spans bytes (err=%d)", n)
			}
			b = b[n:]
			var rs tracev1.ResourceSpans
			if err := proto.Unmarshal(v, &rs); err != nil {
				return nil, fmt.Errorf("otlp: proto: unmarshal ResourceSpans: %w", err)
			}
			svc := protoServiceName(&rs)
			for _, ss := range rs.GetScopeSpans() {
				for _, s := range ss.GetSpans() {
					spans = append(spans, convertProtoSpan(s, svc))
				}
			}
		} else {
			// Skip unknown/unneeded fields.
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, fmt.Errorf("otlp: proto: malformed field value for field %d (err=%d)", num, n)
			}
			b = b[n:]
		}
	}
	return spans, nil
}

func protoServiceName(rs *tracev1.ResourceSpans) string {
	if rs.GetResource() == nil {
		return ""
	}
	for _, kv := range rs.GetResource().GetAttributes() {
		if kv.GetKey() == "service.name" {
			return kv.GetValue().GetStringValue()
		}
	}
	return ""
}

func convertProtoSpan(s *tracev1.Span, service string) Span {
	sp := Span{
		TraceID:       hex.EncodeToString(s.GetTraceId()),
		SpanID:        hex.EncodeToString(s.GetSpanId()),
		ParentSpanID:  hex.EncodeToString(s.GetParentSpanId()),
		Kind:          spanKindString(int(s.GetKind())),
		Service:       service,
		Name:          s.GetName(),
		StartUnixNano: s.GetStartTimeUnixNano(),
		EndUnixNano:   s.GetEndTimeUnixNano(),
	}
	// Empty byte slices become empty hex strings; normalize to "".
	if sp.TraceID == strings.Repeat("0", len(sp.TraceID)) {
		sp.TraceID = hex.EncodeToString(s.GetTraceId())
	}
	// Allowlisted attributes only.
	allowed := make(map[string]string)
	for _, kv := range s.GetAttributes() {
		if attrAllowlist[kv.GetKey()] {
			if sv := kv.GetValue().GetStringValue(); sv != "" {
				allowed[kv.GetKey()] = sv
			}
		}
	}
	if len(allowed) > 0 {
		sp.Attrs = allowed
	}
	for _, l := range s.GetLinks() {
		sp.Links = append(sp.Links, SpanLink{
			TraceID: hex.EncodeToString(l.GetTraceId()),
			SpanID:  hex.EncodeToString(l.GetSpanId()),
		})
	}
	return sp
}

// ─── Deterministic sort ───────────────────────────────────────────────────────

// sortSpans sorts in-place by (trace_id, start_unix_nano, span_id).
// This is the canonical display and test order — never map iteration order
// (bug-class rule 2).
func sortSpans(spans []Span) {
	sort.Slice(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a.TraceID != b.TraceID {
			return a.TraceID < b.TraceID
		}
		if a.StartUnixNano != b.StartUnixNano {
			return a.StartUnixNano < b.StartUnixNano
		}
		return a.SpanID < b.SpanID
	})
}

// ─── Session storage helpers ───────────────────────────────────────────────────

// SessionDir returns the session directory path for the given base dir and
// session name. Base is typically ".polyflow/captures".
func SessionDir(base, name string) string {
	return base + "/" + name
}

// WriteSessionSpans appends a single ExportTraceServiceRequest JSON document
// as one line to the session's spans.otlp.json file. The caller is
// responsible for serialising concurrent appends (R.2 mutex).
func WriteSessionSpans(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := bytes.TrimRight(data, "\n")
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// ReadSessionSpans reads all spans from a session's spans.otlp.json JSONL file.
func ReadSessionSpans(path string) ([]Span, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseOTLPBytes(data)
}

// ─── io.Reader variant (for the embedded receiver) ────────────────────────────

// ParseOTLPReader parses OTLP from an io.Reader (auto-detect format).
func ParseOTLPReader(r io.Reader) ([]Span, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ParseOTLPBytes(data)
}
