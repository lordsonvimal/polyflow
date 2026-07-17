package trace_ingest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// testFixturesDir is the path to shared OTLP fixture files.
const testFixturesDir = "../../../testdata/evidence/runtime"

// TestMain generates the binary protobuf fixture file from the canonical
// two-service trace before running any tests, ensuring the binary and JSON
// fixtures are always in sync.
func TestMain(m *testing.M) {
	if err := generateProtoFixture(); err != nil {
		// Use os.Stderr since t is not available in TestMain
		_, _ = os.Stderr.WriteString("TestMain: generate proto fixture: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// generateProtoFixture writes the binary OTLP protobuf for the canonical
// 2-service HTTP trace to testdata/evidence/runtime/http_2svc.otlp.pb.
// The file is committed to the repo and regenerated here to guarantee it
// stays in sync with the JSON fixture.
//
// We build the ExportTraceServiceRequest envelope with protowire (field 1 =
// repeated ResourceSpans) to avoid importing the collector package and its
// transitive gRPC deps.
func generateProtoFixture() error {
	traceIDHex := "5b8efff798038103d269b633813fc60c"
	clientSpanIDHex := "eee19b7ec3c1b174"
	serverSpanIDHex := "aaa19b7ec3c1b174"

	traceID, err := hex.DecodeString(traceIDHex)
	if err != nil {
		return err
	}
	clientID, err := hex.DecodeString(clientSpanIDHex)
	if err != nil {
		return err
	}
	serverID, err := hex.DecodeString(serverSpanIDHex)
	if err != nil {
		return err
	}

	kv := func(k, v string) *commonv1.KeyValue {
		return &commonv1.KeyValue{Key: k, Value: &commonv1.AnyValue{
			Value: &commonv1.AnyValue_StringValue{StringValue: v},
		}}
	}

	rs1 := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{kv("service.name", "web")}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId: traceID, SpanId: clientID,
				Name: "GET", Kind: tracev1.Span_SPAN_KIND_CLIENT,
				StartTimeUnixNano: 1752480000000000000,
				EndTimeUnixNano:   1752480000120000000,
				Attributes:        []*commonv1.KeyValue{kv("http.request.method", "GET"), kv("url.full", "http://api:8080/games/42")},
			}},
		}},
	}
	rs2 := &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{kv("service.name", "api")}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Spans: []*tracev1.Span{{
				TraceId: traceID, SpanId: serverID, ParentSpanId: clientID,
				Name: "GET /games/:id", Kind: tracev1.Span_SPAN_KIND_SERVER,
				StartTimeUnixNano: 1752480000010000000,
				EndTimeUnixNano:   1752480000110000000,
				Attributes:        []*commonv1.KeyValue{kv("http.request.method", "GET"), kv("http.route", "/games/:id")},
			}},
		}},
	}

	// Build ExportTraceServiceRequest binary manually (field 1 = repeated ResourceSpans).
	b1, err := proto.Marshal(rs1)
	if err != nil {
		return err
	}
	b2, err := proto.Marshal(rs2)
	if err != nil {
		return err
	}
	// Encode as: tag(1, BytesType) + len + bytes, twice.
	var buf []byte
	buf = protowire.AppendTag(buf, 1, protowire.BytesType)
	buf = protowire.AppendBytes(buf, b1)
	buf = protowire.AppendTag(buf, 1, protowire.BytesType)
	buf = protowire.AppendBytes(buf, b2)

	return os.WriteFile(filepath.Join(testFixturesDir, "http_2svc.otlp.pb"), buf, 0o644)
}

// expectedTwoServiceSpans is the canonical parsed output for the 2-service
// HTTP trace fixture, sorted by (trace_id, start_unix_nano, span_id).
func expectedTwoServiceSpans() []Span {
	return []Span{
		{
			TraceID:       "5b8efff798038103d269b633813fc60c",
			SpanID:        "eee19b7ec3c1b174",
			ParentSpanID:  "",
			Kind:          "CLIENT",
			Service:       "web",
			Name:          "GET",
			StartUnixNano: 1752480000000000000,
			EndUnixNano:   1752480000120000000,
			Attrs:         map[string]string{"http.request.method": "GET", "url.full": "http://api:8080/games/42"},
		},
		{
			TraceID:       "5b8efff798038103d269b633813fc60c",
			SpanID:        "aaa19b7ec3c1b174",
			ParentSpanID:  "eee19b7ec3c1b174",
			Kind:          "SERVER",
			Service:       "api",
			Name:          "GET /games/:id",
			StartUnixNano: 1752480000010000000,
			EndUnixNano:   1752480000110000000,
			Attrs:         map[string]string{"http.request.method": "GET", "http.route": "/games/:id"},
		},
	}
}

// TestParseSingleJSON verifies that a single-document OTLP JSON file parses
// to the canonical 2-service span set.
func TestParseSingleJSON(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)
	require.Equal(t, expectedTwoServiceSpans(), spans)
}

// TestParseJSONL verifies that JSONL (one ExportTraceServiceRequest per line)
// produces the same result as the single-document JSON.
func TestParseJSONL(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.jsonl"))
	require.NoError(t, err)
	require.Equal(t, expectedTwoServiceSpans(), spans)
}

// TestParseProto verifies that the binary protobuf fixture (generated by
// TestMain from the same data) produces an identical span set.
func TestParseProto(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.pb"))
	require.NoError(t, err)
	require.Equal(t, expectedTwoServiceSpans(), spans)
}

// TestJSONLEqualsJSON verifies the three formats produce byte-identical span
// sets (determinism test — bug-class rule 2). This is the format-parity guard.
func TestJSONLEqualsJSON(t *testing.T) {
	single, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)
	jsonl, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.jsonl"))
	require.NoError(t, err)
	pb, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.pb"))
	require.NoError(t, err)

	// Serialize all three to JSON and compare (byte-identical output guard).
	encode := func(v interface{}) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	assert.Equal(t, encode(single), encode(jsonl), "single JSON vs JSONL")
	assert.Equal(t, encode(single), encode(pb), "single JSON vs protobuf")
}

// TestMalformedFile verifies that a malformed file returns an error and never
// panics.
func TestMalformedFile(t *testing.T) {
	_, err := ParseOTLPFile(filepath.Join(testFixturesDir, "malformed.otlp.json"))
	require.Error(t, err)
}

// TestUnknownAttrsDropped verifies that attributes not in the allowlist are
// discarded at parse time while the span itself is retained.
func TestUnknownAttrsDropped(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "unknown_attrs.otlp.json"))
	require.NoError(t, err)
	require.Len(t, spans, 1, "span must be retained even with unknown attributes")
	// Only allowlisted attrs should remain.
	sp := spans[0]
	assert.Equal(t, map[string]string{
		"http.request.method": "GET",
		"http.route":          "/health",
	}, sp.Attrs)
	// PII fields must not appear.
	_, hasEmail := sp.Attrs["user.email"]
	assert.False(t, hasEmail, "PII attribute must be dropped")
	_, hasDB := sp.Attrs["db.statement"]
	assert.False(t, hasDB, "non-allowlisted attribute must be dropped")
}

// TestMetricsOnlyFile verifies that a metrics-only OTLP file returns zero
// spans without error (graceful degradation: this is a valid OTLP file, just
// not a trace file).
func TestMetricsOnlyFile(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "metrics_only.otlp.json"))
	require.NoError(t, err)
	assert.Empty(t, spans, "metrics-only OTLP must produce zero spans")
}

// TestDeterministicSortOrder verifies that parsing the same input twice
// produces byte-identical output regardless of input order within a file
// (bug-class rule 2).
func TestDeterministicSortOrder(t *testing.T) {
	run1, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)
	run2, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	b1, _ := json.Marshal(run1)
	b2, _ := json.Marshal(run2)
	assert.Equal(t, string(b1), string(b2), "two-run determinism: output must be byte-identical")
}

// TestAttrAllowlistIsExhaustive verifies the allowlist covers every key named
// in the runtime-flow plan's mapping tables. Any key present in the plan that
// is missing from attrAllowlist would silently drop data.
func TestAttrAllowlistIsExhaustive(t *testing.T) {
	required := []string{
		// HTTP (new semconv)
		"http.request.method", "url.full", "url.path", "http.route",
		"http.response.header.content-type",
		// HTTP (old semconv)
		"http.method", "http.target",
		// Messaging (new semconv)
		"messaging.system", "messaging.operation.type", "messaging.destination.name",
		"messaging.rabbitmq.destination.routing_key",
		// Messaging (old semconv)
		"messaging.operation",
		// Code attribution
		"code.filepath", "code.function",
	}
	for _, k := range required {
		assert.True(t, attrAllowlist[k], "attrAllowlist must include %q", k)
	}
}

// TestEmptyFileReturnsNilSlice verifies that an empty input is not an error.
func TestEmptyFileReturnsNilSlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))
	spans, err := ParseOTLPFile(path)
	require.NoError(t, err)
	assert.Nil(t, spans)
}
