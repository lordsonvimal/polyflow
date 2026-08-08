package configsrc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeSvc lays out a service directory from a path→content map.
func writeSvc(t *testing.T, files map[string]string) string {
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

func TestLoad_AllThreeSources(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		".env":                  "API_URL=https://a.internal/api/v1\n# comment\nEMPTY=\n",
		".env.production":       "API_URL=https://a.internal/api/v2\n",
		"k8s/deploy.yaml":       "spec:\n  template:\n    spec:\n      containers:\n        - name: api\n          env:\n            - name: K8S_URL\n              value: \"https://k.internal\"\n",
		"terraform/prod.tfvars": "TF_URL = \"https://t.internal\"\nCOMPLEX = [1,2]\n",
	})

	got := Load(dir)

	want := map[string][]Value{
		"API_URL": {
			{Value: "https://a.internal/api/v1", Ref: ".env:1"},
			{Value: "https://a.internal/api/v2", Ref: ".env.production:1"},
		},
		"K8S_URL": {{Value: "https://k.internal", Ref: "deploy.yaml:1"}},
		"TF_URL":  {{Value: "https://t.internal", Ref: "prod.tfvars:1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestLoad_MissingDirIsEmptyNotError(t *testing.T) {
	got := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Fatalf("expected no values for a missing service dir, got %v", got)
	}
}

func TestLoad_DeduplicatesIdenticalValuesKeepingFirstRef(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		".env":      "API_URL=https://a.internal\n",
		".env.test": "API_URL=https://a.internal\n",
	})
	got := Load(dir)["API_URL"]
	want := []Value{{Value: "https://a.internal", Ref: ".env:1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestLoad_Deterministic(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		".env":         "A=1\nB=2\n",
		".env.staging": "A=3\n",
		".env.prod":    "A=4\n",
	})
	first := Load(dir)
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(Load(dir), first) {
			t.Fatalf("Load is not deterministic on run %d", i)
		}
	}
	// Sorted file order, not directory order: .env, .env.prod, .env.staging.
	want := []Value{
		{Value: "1", Ref: ".env:1"},
		{Value: "4", Ref: ".env.prod:1"},
		{Value: "3", Ref: ".env.staging:1"},
	}
	if !reflect.DeepEqual(first["A"], want) {
		t.Fatalf("got %#v, want %#v", first["A"], want)
	}
}

func TestLoad_StripsQuotesAndInlineComments(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		".env": "A=\"https://a.internal\"\nB='https://b.internal'\nC=https://c.internal # trailing\n",
	})
	got := Load(dir)
	for k, want := range map[string]string{
		"A": "https://a.internal",
		"B": "https://b.internal",
		"C": "https://c.internal",
	} {
		if len(got[k]) != 1 || got[k][0].Value != want {
			t.Errorf("%s = %#v, want value %q", k, got[k], want)
		}
	}
}
