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
