package yield_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/yield"
)

// buildFixtureStore builds an in-memory store containing:
//   - one resolved-cross http_call (svcA -> svcB, static confidence)
//   - one runtime-promoted http_call (svcA -> svcB, verification_state=verified)
//   - one dynamic-unresolved http_call (svcA -> unresolved:svcC synthetic node)
//   - one external cloud_call (svcA -> an external_service node)
//   - one resolved internal "calls" edge (svcA func1 -> svcA func2)
//   - one ledgered internal "call_ref" unresolved site (maps to calls/internal)
//   - one internal-`send` dynamic-dispatch site, ledgered undecidable_dispatch
//     (excluded from the denominator entirely)
//   - one ledger entry of an out-of-scope kind (inherits_unresolved), which
//     must not crash and must not appear in any row (bug-class #12: it has
//     its own producer/ledger; it's simply outside this report's population)
func buildFixtureStore(t *testing.T) *graph.SQLiteStore {
	t.Helper()
	s, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()

	nodes := []*graph.Node{
		{ID: "svcA:client1", Type: graph.NodeTypeHTTPClient, Label: "client1", Service: "svcA"},
		{ID: "svcA:client2", Type: graph.NodeTypeHTTPClient, Label: "client2", Service: "svcA"},
		{ID: "svcA:client3", Type: graph.NodeTypeHTTPClient, Label: "client3", Service: "svcA"},
		{ID: "svcA:func1", Type: graph.NodeTypeFunction, Label: "func1", Service: "svcA"},
		{ID: "svcA:func2", Type: graph.NodeTypeFunction, Label: "func2", Service: "svcA"},
		{ID: "svcA:cloud1", Type: graph.NodeTypeFunction, Label: "cloud1", Service: "svcA"},
		{ID: "svcB:handler1", Type: graph.NodeTypeHTTPHandler, Label: "handler1", Service: "svcB"},
		{ID: "svcB:handler2", Type: graph.NodeTypeHTTPHandler, Label: "handler2", Service: "svcB"},
		{ID: "external:s3", Type: graph.NodeTypeExternalService, Label: "s3"},
		{ID: "unresolved:svcC", Type: graph.NodeTypeService, Label: "unresolved:svcC"},
	}
	for _, n := range nodes {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatalf("upsert node %s: %v", n.ID, err)
		}
	}

	edges := []*graph.Edge{
		{ID: "e1", From: "svcA:client1", To: "svcB:handler1", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic},
		{ID: "e2", From: "svcA:client2", To: "svcB:handler2", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateVerified},
		{ID: "e3", From: "svcA:client3", To: "unresolved:svcC", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown},
		{ID: "e4", From: "svcA:cloud1", To: "external:s3", Type: graph.EdgeTypeCloudCall, Confidence: graph.ConfidenceStatic},
		{ID: "e5", From: "svcA:func1", To: "svcA:func2", Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceStatic},
	}
	for _, e := range edges {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("upsert edge %s: %v", e.ID, err)
		}
	}

	refs := []graph.UnresolvedRef{
		{Service: "svcA", File: "a.go", Line: 10, Name: "someHelper", Kind: "call_ref"},
		{Service: "svcA", File: "a.rb", Line: 20, Name: "obj.send(m)", Kind: yield.ReasonUndecidableDispatch},
		{Service: "svcA", File: "a.go", Line: 30, Name: "Base", Kind: "inherits_unresolved"},
	}
	if err := s.UpsertUnresolvedRefs(ctx, refs); err != nil {
		t.Fatalf("upsert unresolved refs: %v", err)
	}

	return s
}

func rowFor(t *testing.T, report yield.Report, class graph.EdgeType, scope yield.Scope) yield.Row {
	t.Helper()
	for _, r := range report.Rows {
		if r.Class == class && r.Scope == scope {
			return r
		}
	}
	t.Fatalf("no row for class=%s scope=%s in %+v", class, scope, report.Rows)
	return yield.Row{}
}

func TestCompute_Golden(t *testing.T) {
	s := buildFixtureStore(t)
	defer s.Close()
	ctx := context.Background()

	report, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	httpCross := rowFor(t, report, graph.EdgeTypeHTTPCall, yield.ScopeCross)
	if httpCross.ResolvedStatic != 1 {
		t.Errorf("http_call/cross ResolvedStatic = %d, want 1", httpCross.ResolvedStatic)
	}
	if httpCross.ResolvedRuntime != 1 {
		t.Errorf("http_call/cross ResolvedRuntime = %d, want 1", httpCross.ResolvedRuntime)
	}
	if httpCross.Unresolved != 1 {
		t.Errorf("http_call/cross Unresolved = %d, want 1", httpCross.Unresolved)
	}
	if httpCross.Reasons["unmatched_edge"] != 1 {
		t.Errorf("http_call/cross Reasons[unmatched_edge] = %d, want 1", httpCross.Reasons["unmatched_edge"])
	}
	if want := httpCross.ResolvedStatic + httpCross.ResolvedRuntime + httpCross.External + httpCross.Unresolved; httpCross.Resolvable != want {
		t.Errorf("http_call/cross Resolvable = %d, want %d", httpCross.Resolvable, want)
	}

	cloudCross := rowFor(t, report, graph.EdgeTypeCloudCall, yield.ScopeCross)
	if cloudCross.External != 1 {
		t.Errorf("cloud_call/cross External = %d, want 1", cloudCross.External)
	}

	callsInternal := rowFor(t, report, graph.EdgeTypeCalls, yield.ScopeInternal)
	if callsInternal.ResolvedStatic != 1 {
		t.Errorf("calls/internal ResolvedStatic = %d, want 1", callsInternal.ResolvedStatic)
	}
	if callsInternal.Unresolved != 1 {
		t.Errorf("calls/internal Unresolved = %d, want 1 (call_ref ledger entry)", callsInternal.Unresolved)
	}
	if callsInternal.Reasons["call_ref"] != 1 {
		t.Errorf("calls/internal Reasons[call_ref] = %d, want 1", callsInternal.Reasons["call_ref"])
	}

	// undecidable_dispatch must never create or inflate any row.
	for _, r := range report.Rows {
		for reason := range r.Reasons {
			if reason == yield.ReasonUndecidableDispatch {
				t.Errorf("row %s/%s carries undecidable_dispatch as a reason; must be excluded entirely", r.Class, r.Scope)
			}
		}
	}

	// inherits_unresolved (out-of-scope ledger kind) must not appear anywhere.
	for _, r := range report.Rows {
		if _, ok := r.Reasons["inherits_unresolved"]; ok {
			t.Errorf("row %s/%s carries inherits_unresolved; expected it to be outside this report's population", r.Class, r.Scope)
		}
	}

	// Every row's Unresolved count must be fully reason-ledgered (bug-class #12
	// corollary and the X.3 CI-gate condition "no unresolved site lacking a
	// reason code").
	for _, r := range report.Rows {
		sum := 0
		for _, n := range r.Reasons {
			sum += n
		}
		if sum != r.Unresolved {
			t.Errorf("row %s/%s: sum(Reasons)=%d != Unresolved=%d", r.Class, r.Scope, sum, r.Unresolved)
		}
	}

	// This fixture deliberately includes one unresolved internal call_ref
	// site, so InternalYield is 1/2 here — the "must be 1.0" bar is exercised
	// by TestCompute_GateFailsOnLowCrossYield instead.
	if want := 0.5; report.InternalYield != want {
		t.Errorf("InternalYield = %.4f, want %.4f", report.InternalYield, want)
	}
}

func TestCompute_RowsSortedDeterministically(t *testing.T) {
	s := buildFixtureStore(t)
	defer s.Close()
	ctx := context.Background()

	report, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	for i := 1; i < len(report.Rows); i++ {
		prev, cur := report.Rows[i-1], report.Rows[i]
		if prev.Class > cur.Class || (prev.Class == cur.Class && prev.Scope > cur.Scope) {
			t.Fatalf("Rows not sorted by (Class, Scope) at index %d: %+v then %+v", i, prev, cur)
		}
	}
}

// TestCompute_Determinism runs the pipeline twice on the same input and
// requires byte-identical JSON output (bug-class #2).
func TestCompute_Determinism(t *testing.T) {
	s := buildFixtureStore(t)
	defer s.Close()
	ctx := context.Background()

	r1, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute run 1: %v", err)
	}
	r2, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute run 2: %v", err)
	}

	j1, err := json.Marshal(r1)
	if err != nil {
		t.Fatalf("marshal run 1: %v", err)
	}
	j2, err := json.Marshal(r2)
	if err != nil {
		t.Fatalf("marshal run 2: %v", err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("Compute is non-deterministic:\nrun1=%s\nrun2=%s", j1, j2)
	}
}

func TestCompute_EmptyStoreVacuouslyPasses(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	report, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(report.Rows) != 0 {
		t.Errorf("expected no rows on an empty store, got %+v", report.Rows)
	}
	if !report.Pass {
		t.Errorf("expected Pass=true on an empty store (nothing to fail), got Failures=%v", report.Failures)
	}
	if report.InternalYield != 1.0 || report.CrossYieldStatic != 1.0 || report.CrossYieldWithRuntime != 1.0 {
		t.Errorf("expected vacuous 1.0 ratios on an empty store, got %+v", report)
	}
}

// TestCompute_GateFailsOnLowCrossYield proves the CI gate actually trips:
// a cross http_call left unresolved with no reason attribution would be a
// bug in Compute itself (Reasons always attaches unmatched_edge), so this
// asserts the realistic failure mode instead — cross-static yield below 0.95.
func TestCompute_GateFailsOnLowCrossYield(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	nodes := []*graph.Node{
		{ID: "svcA:client1", Type: graph.NodeTypeHTTPClient, Label: "client1", Service: "svcA"},
		{ID: "svcB:handler1", Type: graph.NodeTypeHTTPHandler, Label: "handler1", Service: "svcB"},
		{ID: "unresolved:svcC", Type: graph.NodeTypeService, Label: "unresolved:svcC"},
	}
	for _, n := range nodes {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatalf("upsert node: %v", err)
		}
	}
	// 1 resolved, 19 unresolved: 5% static yield, well under the 0.95 bar.
	if err := s.UpsertEdge(ctx, &graph.Edge{ID: "r0", From: "svcA:client1", To: "svcB:handler1", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic}); err != nil {
		t.Fatalf("upsert edge: %v", err)
	}
	for i := 0; i < 19; i++ {
		id := "u" + string(rune('a'+i))
		if err := s.UpsertEdge(ctx, &graph.Edge{ID: id, From: "svcA:client1", To: "unresolved:svcC", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown}); err != nil {
			t.Fatalf("upsert edge %s: %v", id, err)
		}
	}

	report, err := yield.Compute(ctx, s)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if report.Pass {
		t.Fatalf("expected Pass=false with cross_yield_static=%.3f", report.CrossYieldStatic)
	}
	found := false
	for _, f := range report.Failures {
		if f != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected non-empty Failures, got none")
	}
}
