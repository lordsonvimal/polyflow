package trace_ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// twoSvcWS is a minimal workspace config with "web" and "api" services used
// by most tests.
func twoSvcWS() *workspace.WorkspaceConfig {
	return &workspace.WorkspaceConfig{
		Services: []workspace.Service{
			{Name: "web"},
			{Name: "api"},
		},
	}
}

// ─── TestMapSpansHTTPBasic ────────────────────────────────────────────────────
//
// Verifies the canonical 2-service HTTP trace (CLIENT web → SERVER api):
//   - produces exactly one FlowRecord
//   - kind=http, key="get /games/*", causality=parent_child
//   - from_service="web", to_service="api"
//   - empty ledger
//
// Test drives through real OTLP fixture bytes (bug-class rule 6).
func TestMapSpansHTTPBasic(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	ws := twoSvcWS()
	flows, ledger := MapSpans(spans, "sess1", ws)

	require.Len(t, flows, 1, "expected exactly one flow record")
	f := flows[0]
	assert.Equal(t, "http", string(f.Kind))
	assert.Equal(t, "get /games/*", f.Key, "http.route /games/:id must be normalised to get /games/*")
	assert.Equal(t, "web", f.FromService)
	assert.Equal(t, "api", f.ToService)
	assert.Equal(t, "parent_child", f.Causality)
	require.Len(t, f.Refs, 1)
	assert.Equal(t, "sess1", f.Refs[0].Session)
	assert.Equal(t, "5b8efff798038103d269b633813fc60c", f.Refs[0].TraceID)
	assert.Equal(t, int64(1752480000), f.Refs[0].ObservedAt)
	assert.Empty(t, f.Refs[0].CodeFile, "no code.filepath present")
	assert.Empty(t, ledger)
}

// ─── TestMapSpansRoutePrefersHTTPRoute ────────────────────────────────────────
//
// SERVER span carries both http.route and url.path; http.route must win
// (already the normalised route pattern — url.path would lose param info).
// Uses http_2svc.otlp.json which has http.route="/games/:id".
func TestMapSpansRoutePrefersHTTPRoute(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	// Artificially inject url.path on the SERVER span to prove http.route wins.
	for i := range spans {
		if spans[i].Kind == "SERVER" {
			spans[i].Attrs["url.path"] = "/games/42"
		}
	}

	flows, _ := MapSpans(spans, "s", twoSvcWS())
	require.Len(t, flows, 1)
	// http.route "/games/:id" → "get /games/*", not "/games/42" from url.path.
	assert.Equal(t, "get /games/*", flows[0].Key)
}

// ─── TestMapSpansURLPathFallback ──────────────────────────────────────────────
//
// SERVER span has no http.route but has url.path (raw concrete path).
// param_wildcard is applied to the raw path.
func TestMapSpansURLPathFallback(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	for i := range spans {
		if spans[i].Kind == "SERVER" {
			delete(spans[i].Attrs, "http.route")
			spans[i].Attrs["url.path"] = "/games/42"
		}
	}

	flows, _ := MapSpans(spans, "s", twoSvcWS())
	require.Len(t, flows, 1)
	// url.path "/games/42" → param_wildcard → "/games/*" (numeric segment treated
	// as a wildcard candidate only via normalizer; here the segment is "42" which
	// is NOT a colon/brace param, so it stays "42").  Normalizers do NOT
	// wildcard numeric segments — only ":id", "{id}", and "[pattern]" forms.
	assert.Equal(t, "get /games/42", flows[0].Key,
		"numeric segment in url.path is not wildcarded by param_wildcard")
}

// ─── TestMapSpansOldSemconv ───────────────────────────────────────────────────
//
// Old OTel semconv: http.method + http.target instead of
// http.request.method + http.route. Both must produce a valid flow record.
// Drives through real fixture bytes (bug-class rule 6).
func TestMapSpansOldSemconv(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_old_semconv.otlp.json"))
	require.NoError(t, err)

	ws := twoSvcWS()
	flows, ledger := MapSpans(spans, "s", ws)

	require.Len(t, flows, 1, "old semconv span must produce a flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "http", string(f.Kind))
	// http.target="/users/7" is not a route pattern → param_wildcard is no-op on /users/7;
	// but the path /users/7 has a numeric segment which stays as-is.
	assert.Equal(t, "get /users/7", f.Key)
	assert.Equal(t, "parent_child", f.Causality)
}

// ─── TestMapSpansServerOnly ───────────────────────────────────────────────────
//
// SERVER span with no parent CLIENT in the session (uninstrumented caller /
// browser without JS instrumentation) → from_service="" causality=server_only.
// Drives through real fixture bytes (bug-class rule 6).
func TestMapSpansServerOnly(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_server_only.otlp.json"))
	require.NoError(t, err)

	ws := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "api"}},
	}
	flows, ledger := MapSpans(spans, "s", ws)

	require.Len(t, flows, 1, "server-only span must produce a flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "", f.FromService, "from_service must be empty for server_only")
	assert.Equal(t, "api", f.ToService)
	assert.Equal(t, "server_only", f.Causality)
	assert.Equal(t, "get /health", f.Key)
}

// ─── TestMapSpansUnknownService ───────────────────────────────────────────────
//
// SERVER span from a service not in the workspace → ledger entry, no flow record.
// Negative fixture: zero flow records produced (bug-class rule per phases.md).
func TestMapSpansUnknownService(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	// Workspace declares only "web", not "api" — api spans must be ledgered.
	ws := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "web"}},
	}
	flows, ledger := MapSpans(spans, "s", ws)

	assert.Empty(t, flows, "unknown-service span must not produce a flow record")
	require.NotEmpty(t, ledger, "unknown-service span must produce a ledger entry")
	hasUnknownSvc := false
	for _, e := range ledger {
		if e.Reason == "unknown_service" {
			hasUnknownSvc = true
		}
	}
	assert.True(t, hasUnknownSvc, "ledger must include an unknown_service entry")
}

// ─── TestMapSpansServiceNamesMapping ─────────────────────────────────────────
//
// evidence.runtime.service_names maps raw OTel names to polyflow names.
// Uses http_2svc_mapped.otlp.json where spans carry "chessleap-web" /
// "chessleap-api" which must resolve to "web" / "api" via the mapping.
// Drives through real fixture bytes (bug-class rule 6).
func TestMapSpansServiceNamesMapping(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc_mapped.otlp.json"))
	require.NoError(t, err)

	ws := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "web"}, {Name: "api"}},
		Evidence: workspace.EvidenceConfig{
			Runtime: workspace.RuntimeEvidenceConfig{
				ServiceNames: map[string]string{
					"chessleap-web": "web",
					"chessleap-api": "api",
				},
			},
		},
	}
	flows, ledger := MapSpans(spans, "mapped", ws)

	require.Len(t, flows, 1, "mapped service names must produce a flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "web", f.FromService, "chessleap-web must map to web")
	assert.Equal(t, "api", f.ToService, "chessleap-api must map to api")
	assert.Equal(t, "get /games/*", f.Key)
}

// ─── TestMapSpansCodeAttribution ─────────────────────────────────────────────
//
// A SERVER span carrying code.filepath / code.function must set CodeFile /
// CodeFunc on the FlowRef — which the provider propagates to SourceRef.CodeFile,
// causing the reconciler to stamp verified_granularity=site.
// Drives through real fixture bytes (bug-class rule 6).
func TestMapSpansCodeAttribution(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_code_attr.otlp.json"))
	require.NoError(t, err)

	flows, ledger := MapSpans(spans, "s", twoSvcWS())
	require.Len(t, flows, 1)
	assert.Empty(t, ledger)
	ref := flows[0].Refs[0]
	assert.Equal(t, "internal/handler/games.go", ref.CodeFile, "code.filepath must be preserved in FlowRef")
	assert.Equal(t, "GetGame", ref.CodeFunc, "code.function must be preserved in FlowRef")
}

// ─── TestMapSpansGranularityGuard ─────────────────────────────────────────────
//
// Two static call sites on the same channel + one span WITHOUT code.filepath
// → both static edges get verified_granularity=channel (never site).
// One span WITH code.filepath → both edges get verified_granularity=site.
// Required by the R.1 spec's "granularity guard" test.
func TestMapSpansGranularityGuard(t *testing.T) {
	// Build two static edges on the same channel (different call sites).
	nodes := []graph.Node{
		{ID: "site-a", Service: "web", File: "a.go", Line: 1},
		{ID: "site-b", Service: "web", File: "b.go", Line: 2},
		{ID: "handler", Service: "api", File: "h.go", Line: 3},
	}
	sharedLabel := "get /games/*"
	staticEdges := []graph.Edge{
		{ID: "call-a", From: "site-a", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: sharedLabel, Confidence: graph.ConfidenceInferred},
		{ID: "call-b", From: "site-b", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: sharedLabel, Confidence: graph.ConfidenceInferred},
	}

	// Case 1: span WITHOUT code.filepath → channel granularity.
	spans1, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	flows1, _ := MapSpans(spans1, "s1", twoSvcWS())
	require.Len(t, flows1, 1)
	runtimeEv1 := evidence.Evidence{
		Edges: []graph.Edge{flowRecordToEdge(&flows1[0])},
	}
	sp1 := evidence.NewStaticProvider(nodes, staticEdges, nil)
	rec1, err := evidence.NewReconciler(sp1, &fakeRuntimeProvider{ev: runtimeEv1})
	require.NoError(t, err)
	result1, err := rec1.Reconcile(context.Background(), nil)
	require.NoError(t, err)

	for _, e := range result1.Edges {
		if e.ID == "call-a" || e.ID == "call-b" {
			assert.Equal(t, graph.StateVerified, e.VerificationState,
				"edge %s must be verified", e.ID)
			assert.Equal(t, graph.GranularityChannel, e.VerifiedGranularity,
				"span without code.filepath → channel granularity on %s", e.ID)
		}
	}

	// Case 2: span WITH code.filepath → site granularity on all matching edges.
	spans2, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_code_attr.otlp.json"))
	require.NoError(t, err)

	flows2, _ := MapSpans(spans2, "s2", twoSvcWS())
	require.Len(t, flows2, 1)
	assert.NotEmpty(t, flows2[0].Refs[0].CodeFile, "fixture must carry code.filepath")
	runtimeEv2 := evidence.Evidence{
		Edges: []graph.Edge{flowRecordToEdge(&flows2[0])},
	}
	sp2 := evidence.NewStaticProvider(nodes, staticEdges, nil)
	rec2, err := evidence.NewReconciler(sp2, &fakeRuntimeProvider{ev: runtimeEv2})
	require.NoError(t, err)
	result2, err := rec2.Reconcile(context.Background(), nil)
	require.NoError(t, err)

	for _, e := range result2.Edges {
		if e.ID == "call-a" || e.ID == "call-b" {
			assert.Equal(t, graph.StateVerified, e.VerificationState,
				"edge %s must be verified", e.ID)
			assert.Equal(t, graph.GranularitySite, e.VerifiedGranularity,
				"span with code.filepath → site granularity on %s", e.ID)
		}
	}
}

// ─── TestMapSpansFanOut ───────────────────────────────────────────────────────
//
// Two static edges sharing one channel + one runtime span → BOTH edges get
// the runtime source (fan-out, never first-match — bug-class rule 1).
func TestMapSpansFanOut(t *testing.T) {
	nodes := []graph.Node{
		{ID: "site-a", Service: "web", File: "a.go", Line: 1},
		{ID: "site-b", Service: "web", File: "b.go", Line: 2},
		{ID: "handler", Service: "api", File: "h.go", Line: 3},
	}
	label := "get /games/*"
	staticEdges := []graph.Edge{
		{ID: "e-a", From: "site-a", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: label, Confidence: graph.ConfidenceInferred},
		{ID: "e-b", From: "site-b", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: label, Confidence: graph.ConfidenceInferred},
	}

	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	flows, _ := MapSpans(spans, "sess", twoSvcWS())
	require.Len(t, flows, 1)

	runtimeEv := evidence.Evidence{
		Edges: []graph.Edge{flowRecordToEdge(&flows[0])},
	}

	sp := evidence.NewStaticProvider(nodes, staticEdges, nil)
	rec, err := evidence.NewReconciler(sp, &fakeRuntimeProvider{ev: runtimeEv})
	require.NoError(t, err)
	result, err := rec.Reconcile(context.Background(), nil)
	require.NoError(t, err)

	edgeByID := make(map[string]graph.Edge)
	for _, e := range result.Edges {
		edgeByID[e.ID] = e
	}

	// Both edges must receive the runtime source (multi-valued join).
	for _, id := range []string{"e-a", "e-b"} {
		e, ok := edgeByID[id]
		require.True(t, ok, "edge %s must be in result", id)
		hasRuntime := false
		for _, s := range e.Sources {
			if s.Provider == "runtime" {
				hasRuntime = true
			}
		}
		assert.True(t, hasRuntime, "edge %s must receive the runtime source (fan-out)", id)
		assert.Equal(t, graph.StateVerified, e.VerificationState,
			"edge %s must be verified after runtime confirmation", id)
	}
}

// ─── TestMapSpansObservedOnlyGap ──────────────────────────────────────────────
//
// A runtime flow record that has no matching static edge surfaces as an
// observed_only_gap (synthetic edge + synthetic endpoint nodes).
func TestMapSpansObservedOnlyGap(t *testing.T) {
	// Static graph has no edges at all; runtime trace sees a real call.
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	flows, _ := MapSpans(spans, "sess", twoSvcWS())
	require.Len(t, flows, 1)

	runtimeEv := evidence.Evidence{
		Edges: []graph.Edge{flowRecordToEdge(&flows[0])},
	}

	sp := evidence.NewStaticProvider(nil, nil, nil) // empty static graph
	rec, err := evidence.NewReconciler(sp, &fakeRuntimeProvider{ev: runtimeEv})
	require.NoError(t, err)
	result, err := rec.Reconcile(context.Background(), nil)
	require.NoError(t, err)

	var gapEdges []graph.Edge
	for _, e := range result.Edges {
		if e.VerificationState == graph.StateObservedOnlyGap {
			gapEdges = append(gapEdges, e)
		}
	}
	require.Len(t, gapEdges, 1, "gap edge must surface when static graph misses the channel")
	assert.Contains(t, gapEdges[0].ID, "gap:", "gap edge ID must be prefixed with gap:")
	assert.Equal(t, "get /games/*", gapEdges[0].Label)
}

// ─── TestMapSpansDeterminism ──────────────────────────────────────────────────
//
// Two-run determinism test (bug-class rule 2): feeding the same session twice
// through MapSpans must produce byte-identical JSON output both times.
func TestMapSpansDeterminism(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)

	run := func() []byte {
		flows, ledger := MapSpans(spans, "s", twoSvcWS())
		b, err := json.Marshal(struct {
			Flows  []FlowRecord        `json:"flows"`
			Ledger []IngestLedgerEntry `json:"ledger"`
		}{flows, ledger})
		require.NoError(t, err)
		return b
	}

	first := run()
	second := run()
	assert.Equal(t, string(first), string(second),
		"MapSpans must produce byte-identical JSON output across runs")
}

// ─── TestProviderDeterminism ──────────────────────────────────────────────────
//
// RuntimeProvider.Collect on the same session twice must produce byte-identical
// JSON-serialised Evidence (bug-class rule 2).
func TestProviderDeterminism(t *testing.T) {
	capturesDir := t.TempDir()
	sessDir := filepath.Join(capturesDir, "sess1")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	raw, err := os.ReadFile(filepath.Join(testFixturesDir, "http_2svc.otlp.json"))
	require.NoError(t, err)
	require.NoError(t, WriteSessionSpans(filepath.Join(sessDir, "spans.otlp.json"), raw))

	p := NewRuntimeProvider(capturesDir, nil)
	ws := twoSvcWS()

	collect := func() []byte {
		ev, err := p.Collect(context.Background(), ws)
		require.NoError(t, err)
		b, err := json.Marshal(ev.Edges)
		require.NoError(t, err)
		return b
	}

	first := collect()
	second := collect()
	assert.Equal(t, string(first), string(second),
		"RuntimeProvider.Collect must produce byte-identical edges across runs")
}

// ─── TestProviderGracefulDegradation ─────────────────────────────────────────
//
// With no sessions directory present, the provider must return empty Evidence
// (degradation — no runtime sessions = static-only, never an error).
func TestProviderGracefulDegradation(t *testing.T) {
	p := NewRuntimeProvider("/nonexistent/captures", nil)
	ev, err := p.Collect(context.Background(), twoSvcWS())
	require.NoError(t, err)
	assert.Empty(t, ev.Edges)
	assert.Empty(t, ev.Unresolved)
}

// ─── TestProviderNameValid ────────────────────────────────────────────────────

func TestProviderNameValid(t *testing.T) {
	p := NewRuntimeProvider("", nil)
	assert.Equal(t, "runtime", p.Name())
	assert.NoError(t, evidence.ValidateProviderName(p.Name()))
}

// ─── TestMapSpansSSEConnection ────────────────────────────────────────────────
//
// Positive: SERVER span with http.response.header.content-type=text/event-stream
// → exactly one SSE connection flow record (not N event edges), kind="sse",
// key is path-only (no method prefix), causality=parent_child.
// Drives through real OTLP fixture bytes (bug-class rule 6).
func TestMapSpansSSEConnection(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "sse_connection.otlp.json"))
	require.NoError(t, err)

	ws := twoSvcWS()
	flows, ledger := MapSpans(spans, "sess-sse", ws)

	require.Len(t, flows, 1, "SSE span must produce exactly one connection flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "sse", string(f.Kind), "kind must be sse, not http")
	assert.Equal(t, "/events", f.Key, "SSE key must be path-only (no method prefix)")
	assert.Equal(t, "web", f.FromService)
	assert.Equal(t, "api", f.ToService)
	assert.Equal(t, "parent_child", f.Causality)
	require.Len(t, f.Refs, 1)
	assert.Equal(t, "sess-sse", f.Refs[0].Session)
	assert.Equal(t, "1a2b3c4d5e6f7890abcdef1234567890", f.Refs[0].TraceID)
}

// ─── TestMapSpansSSEWorkspaceListedRoute ──────────────────────────────────────
//
// Positive: SERVER span with NO content-type header but path listed in
// evidence.runtime.sse_routes → still detected as SSE.
// Drives through real OTLP fixture bytes (bug-class rule 6).
func TestMapSpansSSEWorkspaceListedRoute(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "sse_ws_listed_route.otlp.json"))
	require.NoError(t, err)

	ws := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "api"}},
		Evidence: workspace.EvidenceConfig{
			Runtime: workspace.RuntimeEvidenceConfig{
				SSERoutes: []string{"/stream"},
			},
		},
	}
	flows, ledger := MapSpans(spans, "sess-ws", ws)

	require.Len(t, flows, 1, "workspace-listed SSE route must produce one SSE flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "sse", string(f.Kind), "workspace-listed route must produce sse kind")
	assert.Equal(t, "/stream", f.Key)
}

// ─── TestMapSpansSSENotSSE ────────────────────────────────────────────────────
//
// Negative: a long-lived SERVER span without text/event-stream content-type and
// not in sse_routes must produce an HTTP flow record, never an SSE record.
// Long duration is NOT the SSE detection signal (phases.md mapping table rule).
// Drives through real OTLP fixture bytes (bug-class rule 6).
func TestMapSpansSSENotSSE(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "sse_not_sse.otlp.json"))
	require.NoError(t, err)

	ws := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "api"}},
	}
	flows, ledger := MapSpans(spans, "s", ws)

	require.Len(t, flows, 1, "long-lived non-SSE span must produce exactly one flow record")
	assert.Empty(t, ledger)
	f := flows[0]
	assert.Equal(t, "http", string(f.Kind), "long-lived span without content-type must be http, not sse")
	assert.Equal(t, "get /slow-export", f.Key)
}

// ─── TestMetricsOnlyNoFlows ───────────────────────────────────────────────────
//
// Negative: a metrics-only OTLP file produces zero spans, so MapSpans must
// return no flow records and no ledger entries.
func TestMetricsOnlyNoFlows(t *testing.T) {
	spans, err := ParseOTLPFile(filepath.Join(testFixturesDir, "metrics_only.otlp.json"))
	require.NoError(t, err)
	require.Empty(t, spans, "pre-condition: metrics-only file must parse to zero spans")

	flows, ledger := MapSpans(spans, "s", twoSvcWS())
	assert.Empty(t, flows, "zero spans must produce zero flow records (negative fixture)")
	assert.Empty(t, ledger)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeRuntimeProvider implements evidence.Provider returning a fixed Evidence.
type fakeRuntimeProvider struct{ ev evidence.Evidence }

func (f *fakeRuntimeProvider) Name() string { return "runtime" }
func (f *fakeRuntimeProvider) Collect(_ context.Context, _ *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	return f.ev, nil
}
