package factpipe

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfigFixture lays out one directory from a path->content map and
// returns it, for ApplyConfig's svcPath argument.
func writeConfigFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func envRefConfig() CompiledConfig {
	cs, err := CompileConfigSpecs([]ConfigSpec{{Relation: "env_var_value", From: "env_var_ref"}})
	if err != nil {
		panic(err)
	}
	return cs[0]
}

func TestCompileConfigSpecs(t *testing.T) {
	if _, err := CompileConfigSpecs([]ConfigSpec{{From: "x"}}); err == nil {
		t.Error("missing relation should fail")
	}
	if _, err := CompileConfigSpecs([]ConfigSpec{{Relation: "x"}}); err == nil {
		t.Error("missing from should fail")
	}
	if _, err := CompileConfigSpecs([]ConfigSpec{{Relation: "x", From: "x"}}); err == nil {
		t.Error("from == relation should fail")
	}
	if _, err := CompileConfigSpecs([]ConfigSpec{{Relation: "y", From: "x"}}); err != nil {
		t.Errorf("valid spec rejected: %v", err)
	}
}

func TestApplyConfigAddsFacts(t *testing.T) {
	dir := writeConfigFixture(t, map[string]string{
		".env": "API_URL=https://api.example.com/v2\n",
	})

	fs := NewFactSet()
	fs.Add(Fact{Pred: "env_var_ref", Args: []Atom{
		Str("client.go"), Int(9), Str("API_URL"),
	}})

	ApplyConfig([]CompiledConfig{envRefConfig()}, dir, fs)

	var got []Fact
	for _, f := range fs.All() {
		if f.Pred == "env_var_value" {
			got = append(got, f)
		}
	}
	if len(got) != 1 {
		t.Fatalf("env_var_value: %d facts, want 1", len(got))
	}
	a := got[0].Args
	if a[0].Str != "client.go" || a[1].Int != 9 || a[2].Str != "API_URL" ||
		a[3].Str != "https://api.example.com/v2" || a[4].Kind != AtomStr {
		t.Errorf("env_var_value args = %#v", a)
	}
	if got[0].Origin.Kind != OriginPrimitive {
		t.Errorf("origin kind = %d, want OriginPrimitive", got[0].Origin.Kind)
	}
}

func TestApplyConfigMiss(t *testing.T) {
	dir := writeConfigFixture(t, map[string]string{".env": "OTHER_VAR=x\n"})
	fs := NewFactSet()
	fs.Add(Fact{Pred: "env_var_ref", Args: []Atom{Str("client.go"), Int(1), Str("API_URL")}})

	ApplyConfig([]CompiledConfig{envRefConfig()}, dir, fs)

	for _, f := range fs.All() {
		if f.Pred == "env_var_value" {
			t.Fatalf("expected no env_var_value fact, got %#v", f)
		}
	}
}

func TestApplyConfigFansOutDisagreeingValues(t *testing.T) {
	dir := writeConfigFixture(t, map[string]string{
		".env": "API_URL=https://api.example.com/v2\n",
		"k8s/deploy.yaml": "spec:\n  template:\n    spec:\n      containers:\n        - env:\n" +
			"            - name: API_URL\n              value: https://staging.example.com/v3\n",
	})
	fs := NewFactSet()
	fs.Add(Fact{Pred: "env_var_ref", Args: []Atom{Str("client.go"), Int(1), Str("API_URL")}})

	ApplyConfig([]CompiledConfig{envRefConfig()}, dir, fs)

	var got []Fact
	for _, f := range fs.All() {
		if f.Pred == "env_var_value" {
			got = append(got, f)
		}
	}
	// Both checked-in values are asserted — picking a winner (or abstaining on
	// disagreement, Tier CB's rule) is derivation policy, not extraction.
	if len(got) != 2 {
		t.Fatalf("env_var_value: %d facts, want 2 (one per distinct checked-in value)", len(got))
	}
}

func TestApplyConfigNoBlockIsNoop(t *testing.T) {
	dir := writeConfigFixture(t, map[string]string{".env": "API_URL=https://api.example.com\n"})
	fs := NewFactSet()
	fs.Add(Fact{Pred: "env_var_ref", Args: []Atom{Str("client.go"), Int(1), Str("API_URL")}})
	ApplyConfig(nil, dir, fs)
	if fs.Len() != 1 {
		t.Errorf("Len = %d, want 1 (no config block ⇒ inert)", fs.Len())
	}
}

func TestApplyConfigNoServicePathIsNoop(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "env_var_ref", Args: []Atom{Str("client.go"), Int(1), Str("API_URL")}})
	ApplyConfig([]CompiledConfig{envRefConfig()}, "", fs)
	if fs.Len() != 1 {
		t.Errorf("Len = %d, want 1 (empty svcPath ⇒ inert, no disk access)", fs.Len())
	}
}
