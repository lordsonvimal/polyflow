package artifact

import "testing"

func known(svc string, paths ...string) map[string]map[string]bool {
	m := map[string]map[string]bool{svc: {}}
	for _, p := range paths {
		m[svc][p] = true
	}
	return m
}

func endpointGate() Gate {
	m, ok := MappingFor("endpoint_table")
	if !ok {
		panic("endpoint_table mapping missing")
	}
	return m.GateSpec()
}

// A JSON resource table qualifies and its entity level is the resource name.
func TestGate_EndpointTableQualifies(t *testing.T) {
	src := []byte(`{"resources":{
		"widget":{"endpoint":"/api/widgets","reorder":"/api/widgets/:id/reorder"},
		"gadget":{"endpoint":"/api/gadgets","reorder":"/api/gadgets/:id/reorder"},
		"sprocket":{"endpoint":"/api/sprockets","update":"/api/sprockets/{id}"}}}`)
	a, _ := ReadFile("x.json", src)
	a.Service = "svc"
	k := known("svc",
		"/api/widgets", "/api/widgets/*/reorder",
		"/api/gadgets", "/api/gadgets/*/reorder",
		"/api/sprockets", "/api/sprockets/*")

	v := endpointGate().Evaluate(a, k, false)
	if !v.Qualified {
		t.Fatalf("not qualified: %s", v.Reason)
	}
	if v.EntityDepth != 2 { // resources / <name>
		t.Fatalf("entity depth = %d, want 2", v.EntityDepth)
	}
	if v.MatchCount != 6 || v.Distinct != 6 {
		t.Fatalf("matched %d / distinct %d", v.MatchCount, v.Distinct)
	}
}

// A file with a few incidental path-like strings does not qualify.
func TestGate_NonAssetRejected(t *testing.T) {
	src := []byte(`{"a":{"url":"/api/widgets"},"b":{"url":"/api/gadgets"},"c":{"note":"nope"},
		"d":{"x":"1"},"e":{"y":"2"},"f":{"z":"3"},"g":{"w":"4"},"h":{"v":"5"}}`)
	a, _ := ReadFile("x.json", src)
	a.Service = "svc"
	k := known("svc", "/api/widgets", "/api/gadgets", "/api/sprockets", "/api/things", "/api/more")

	v := endpointGate().Evaluate(a, k, false)
	if v.Qualified {
		t.Fatalf("should not qualify; matched=%d ratio=%.2f", v.MatchCount, v.Ratio)
	}
	if v.GatePassed {
		t.Fatal("gate should not have passed (2 of 8)")
	}
}

// declared=true bypasses the match threshold but still needs an entity level.
func TestGate_DeclaredBypassesMatchGate(t *testing.T) {
	src := []byte(`{"resources":{
		"widget":{"endpoint":"/api/widgets"},
		"gadget":{"endpoint":"/api/gadgets"},
		"sprocket":{"endpoint":"/api/sprockets"}}}`)
	a, _ := ReadFile("x.json", src)
	a.Service = "svc"
	k := known("svc", "/api/widgets", "/api/gadgets") // 2 of 3, below MinMatches

	if v := endpointGate().Evaluate(a, k, false); v.Qualified {
		t.Fatal("undeclared file with 2 matches should not qualify")
	}
	v := endpointGate().Evaluate(a, k, true)
	if !v.Qualified {
		t.Fatalf("declared file should qualify: %s", v.Reason)
	}
	if v.EntityDepth != 2 {
		t.Fatalf("entity depth = %d", v.EntityDepth)
	}
}

// A loosened threshold is visible: same file, default gate fails, tuned passes.
func TestGate_TunedThresholdDetectable(t *testing.T) {
	src := []byte(`{"r":{
		"widget":{"u":"/api/widgets"},
		"gadget":{"u":"/api/gadgets"},
		"sprocket":{"u":"/api/sprockets"},
		"extra1":{"u":"/nope/1"},"extra2":{"u":"/nope/2"}}}`)
	a, _ := ReadFile("x.json", src)
	a.Service = "svc"
	k := known("svc", "/api/widgets", "/api/gadgets", "/api/sprockets")

	if v := endpointGate().Evaluate(a, k, false); v.Qualified {
		t.Fatal("default gate wants 5 matches; only 3 here")
	}
	loose := endpointGate()
	loose.MinMatches = 3
	v := loose.Evaluate(a, k, false)
	if !v.Qualified || !v.GatePassed {
		t.Fatalf("loosened gate should qualify: %s", v.Reason)
	}
	gatePassDef := v.MatchCount >= DefaultMinMatches && v.Ratio >= DefaultMinRatio
	if gatePassDef {
		t.Fatal("default thresholds should NOT have passed — tuned is undetectable")
	}
}

func TestGate_UnknownNormalizerRejectsEverything(t *testing.T) {
	a, _ := ReadFile("x.json", []byte(`{"a":{"u":"/x"}}`))
	a.Service = "svc"
	g := Gate{Against: "x", Normalizers: []string{"no_such_normalizer"}}
	v := g.Evaluate(a, known("svc", "/x"), true)
	if v.Qualified || v.Reason == "" {
		t.Fatalf("expected rejection, got %+v", v)
	}
}
