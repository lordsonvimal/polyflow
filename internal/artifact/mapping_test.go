package artifact

import "testing"

func TestMappings_LoadAndValidate(t *testing.T) {
	all, err := Mappings()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("want at least endpoint_table + queue_table, got %d", len(all))
	}
	for _, m := range all {
		if resolveChain(m.Gate.Normalizers).bad != "" {
			t.Errorf("%s: unknown normalizer", m.Kind)
		}
		for _, f := range m.Formats {
			// toml is allowed in a mapping as a forward declaration; every
			// other named format must have a reader.
			if f == "toml" {
				continue
			}
			if !HasReader(f) {
				t.Errorf("%s: format %q has no reader", m.Kind, f)
			}
		}
	}
}

func TestMapping_EndpointChainMatchesNormalizeSchemaPath(t *testing.T) {
	m, _ := MappingFor("endpoint_table")
	want := []string{"query_strip", "param_wildcard", "trim_slash", "require_abs_path"}
	if len(m.Gate.Normalizers) != len(want) {
		t.Fatalf("endpoint_table normalizers = %v, want %v", m.Gate.Normalizers, want)
	}
	for i := range want {
		if m.Gate.Normalizers[i] != want[i] {
			t.Fatalf("endpoint_table normalizers = %v, want %v", m.Gate.Normalizers, want)
		}
	}
}

func TestMapping_Rule(t *testing.T) {
	m, _ := MappingFor("endpoint_table")
	if got := m.Rule("config/routes.json"); got != "artifacts/endpoint_table#config/routes.json" {
		t.Fatalf("Rule = %q", got)
	}
}

func TestMapping_RowsSplitsOnEntityDepth(t *testing.T) {
	m, _ := MappingFor("endpoint_table")
	src := []byte(`{"resources":{
		"widget":{"endpoint":"/api/widgets"},
		"gadget":{"endpoint":"/api/gadgets"},
		"sprocket":{"endpoint":"/api/sprockets"}}}`)
	a, _ := ReadFile("x.json", src)
	a.Service = "svc"
	k := known("svc", "/api/widgets", "/api/gadgets", "/api/sprockets")

	v := m.GateSpec().Evaluate(a, k, true) // declared: bypass the 5-match default
	if !v.Qualified {
		t.Fatalf("not qualified: %s", v.Reason)
	}
	rows := m.Rows(v)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	want := map[string]string{
		"resources.widget":   "endpoint",
		"resources.gadget":   "endpoint",
		"resources.sprocket": "endpoint",
	}
	for _, r := range rows {
		if wantKey, ok := want[r.Entity]; !ok {
			t.Errorf("unexpected entity %q", r.Entity)
		} else if r.Key != wantKey {
			t.Errorf("entity %q: key = %q, want %q", r.Entity, r.Key, wantKey)
		}
		delete(want, r.Entity)
	}
	if len(want) != 0 {
		t.Errorf("missing entities: %v", want)
	}
}

func TestMapping_RowsUnqualifiedIsNil(t *testing.T) {
	m, _ := MappingFor("endpoint_table")
	if rows := m.Rows(Verdict{Qualified: false}); rows != nil {
		t.Fatalf("want nil, got %v", rows)
	}
}
