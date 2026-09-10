package factpipe

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/datalog"
)

func mustCompile(t *testing.T, src string) CompiledEmit {
	t.Helper()
	ces, err := CompileEmits([]byte(src))
	if err != nil {
		t.Fatalf("CompileEmits: %v", err)
	}
	if len(ces) != 1 {
		t.Fatalf("got %d emits, want 1", len(ces))
	}
	return ces[0]
}

func TestEmitConfidenceTiers(t *testing.T) {
	ce := mustCompile(t, `
emit:
  - relation: gin_guard
    columns: [Handler, Target, Depth]
    edge: { from: {arg: Handler}, to: {arg: Target}, type: calls, label: middleware }
    confidence:
      - { when: "Depth == 0", value: certain }
      - { when: "Depth <= 2", value: inferred }
      - { else: heuristic }
`)
	res := ce.Apply([]datalog.Tuple{
		{"h1", "t0", "0"},
		{"h2", "t2", "2"},
		{"h3", "t5", "5"},
	}, nil)

	want := map[string]string{"h1->t0:middleware": "certain", "h2->t2:middleware": "inferred", "h3->t5:middleware": "heuristic"}
	if len(res.Edges) != 3 {
		t.Fatalf("got %d edges, want 3", len(res.Edges))
	}
	for _, e := range res.Edges {
		if want[e.ID] != e.Confidence {
			t.Errorf("edge %s: confidence %q, want %q", e.ID, e.Confidence, want[e.ID])
		}
		if len(e.Sources) != 1 || e.Sources[0].Layer != "L3" || e.Sources[0].Rule != "gin_guard" {
			t.Errorf("edge %s: bad provenance %+v", e.ID, e.Sources)
		}
	}
}

func TestEmitAbstainOnDisagreement(t *testing.T) {
	ce := mustCompile(t, `
emit:
  - relation: prop_url
    columns: [Component, Target, Prop]
    edge: { from: {arg: Component}, to: {arg: Target}, type: http_call }
    abstain:
      when: "count_distinct(Target by Component, Prop) > 1"
`)
	// c1/pA disagree (t1 vs t2) -> both abstained. c2/pB agrees -> one edge.
	res := ce.Apply([]datalog.Tuple{
		{"c1", "t1", "pA"},
		{"c1", "t2", "pA"},
		{"c2", "t3", "pB"},
	}, nil)

	if len(res.Edges) != 1 {
		t.Fatalf("got %d edges, want 1 (%+v)", len(res.Edges), res.Edges)
	}
	if res.Edges[0].ID != "c2->t3:http_call" {
		t.Errorf("surviving edge = %s", res.Edges[0].ID)
	}
}

func TestEmitFanOutToLedger(t *testing.T) {
	ce := mustCompile(t, `
emit:
  - relation: gin_guard
    columns: [Handler, Target]
    edge: { from: {arg: Handler}, to: {arg: Target}, type: calls, label: middleware }
    fan_out: { key: [Handler], max: 2, on_exceed: ledger_only }
`)
	res := ce.Apply([]datalog.Tuple{
		{"h1", "a"}, {"h1", "b"}, {"h1", "c"}, // over cap 2
		{"h2", "d"}, {"h2", "e"}, // at cap
	}, nil)

	if len(res.Edges) != 2 {
		t.Fatalf("got %d edges, want 2 (h2 only)", len(res.Edges))
	}
	if len(res.Ledger) != 1 {
		t.Fatalf("got %d ledger rows, want 1", len(res.Ledger))
	}
	l := res.Ledger[0]
	if l.Kind != "gin_guard_fanout" || l.Name != "h1" || l.Targets != "a\nb\nc" {
		t.Errorf("ledger row = %+v", l)
	}
}

func TestEmitUnresolvedOnNull(t *testing.T) {
	ce := mustCompile(t, `
emit:
  - relation: gin_guard
    columns: [Handler, Target, Svc, Name]
    edge: { from: {arg: Handler}, to: {arg: Target}, type: calls, label: middleware }
    unresolved:
      when: "Target == null"
      ref: { service: {arg: Svc}, name: {arg: Name}, kind: middleware }
`)
	res := ce.Apply([]datalog.Tuple{
		{"h1", "t1", "svc", "Authenticate"},
		{"h2", "", "svc", "RateLimit"},
	}, nil)

	if len(res.Edges) != 1 || res.Edges[0].ID != "h1->t1:middleware" {
		t.Fatalf("edges = %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("got %d unresolved, want 1", len(res.Unresolved))
	}
	u := res.Unresolved[0]
	if u.Kind != "middleware" || u.Service != "svc" || u.Name != "RateLimit" {
		t.Errorf("unresolved = %+v", u)
	}
}

func TestEmitDedup(t *testing.T) {
	ce := mustCompile(t, `
emit:
  - relation: gin_guard
    columns: [Handler, Target, Expr]
    edge: { from: {arg: Handler}, to: {arg: Target}, type: calls, label: middleware }
    meta: { via: gin_middleware_use, expr: {arg: Expr} }
    dedup: [from, to, label]
`)
	res := ce.Apply([]datalog.Tuple{
		{"h1", "t1", "authMw.Auth()"},
		{"h1", "t1", "authMw.Auth()"},
	}, nil)
	if len(res.Edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(res.Edges))
	}
	if res.Edges[0].Meta["via"] != "gin_middleware_use" || res.Edges[0].Meta["expr"] != "authMw.Auth()" {
		t.Errorf("meta = %+v", res.Edges[0].Meta)
	}
}

func TestCompileEmitRejectsUnknownEdgeType(t *testing.T) {
	_, err := CompileEmits([]byte(`
emit:
  - relation: r
    columns: [A, B]
    edge: { from: {arg: A}, to: {arg: B}, type: pusher_trigger }
`))
	if err == nil {
		t.Fatal("expected an error for a framework-named edge type")
	}
}

func TestCompileEmitRejectsBadPredicate(t *testing.T) {
	_, err := CompileEmits([]byte(`
emit:
  - relation: r
    columns: [A, B]
    edge: { from: {arg: A}, to: {arg: B}, type: calls }
    abstain: { when: "A === B" }
`))
	if err == nil {
		t.Fatal("expected a predicate parse error")
	}
}
