package artifact

import (
	"reflect"
	"testing"
)

func leafStrings(a *Artifact) [][]string {
	out := make([][]string, 0, len(a.Leaves))
	for _, l := range a.Leaves {
		row := append(append([]string{}, l.Path...), l.Value)
		out = append(out, row)
	}
	return out
}

func TestReadFile_JSONFlattensDeterministically(t *testing.T) {
	src := []byte(`{"b":{"y":"2","x":"1"},"a":["p","q"],"n":5}`)
	a1, ok := ReadFile("cfg/x.json", src)
	if !ok {
		t.Fatal("json not read")
	}
	want := [][]string{
		{"a", "0", "p"},
		{"a", "1", "q"},
		{"b", "x", "1"},
		{"b", "y", "2"},
	}
	if got := leafStrings(a1); !reflect.DeepEqual(got, want) {
		t.Fatalf("json leaves\n got %v\nwant %v", got, want)
	}
	a2, _ := ReadFile("cfg/x.json", src)
	if !reflect.DeepEqual(leafStrings(a1), leafStrings(a2)) {
		t.Fatal("json leaf order not deterministic across reads")
	}
	if a1.Format != "json" {
		t.Fatalf("format = %q", a1.Format)
	}
}

func TestReadFile_YAMLNestedAndArrays(t *testing.T) {
	src := []byte("resources:\n  widget:\n    list: /api/widgets\n  gadget:\n    list: /api/gadgets\ntags: [a, b]\n")
	a, ok := ReadFile("cfg/x.yaml", src)
	if !ok {
		t.Fatal("yaml not read")
	}
	want := [][]string{
		{"resources", "gadget", "list", "/api/gadgets"},
		{"resources", "widget", "list", "/api/widgets"},
		{"tags", "0", "a"},
		{"tags", "1", "b"},
	}
	if got := leafStrings(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("yaml leaves\n got %v\nwant %v", got, want)
	}
}

func TestReadFile_YMLExtension(t *testing.T) {
	if _, ok := ReadFile("x.yml", []byte("k: v\n")); !ok {
		t.Fatal(".yml not claimed")
	}
}

func TestReadFile_EnvKeyValueWithLines(t *testing.T) {
	src := []byte("# comment\nexport QUEUE_MAIN=orders.inbound\nQUEUE_DLQ=\"orders.dlq\"\n\nBROKEN\n")
	a, ok := ReadFile(".env", src)
	if !ok {
		t.Fatal("env not read")
	}
	want := [][]string{
		{"QUEUE_MAIN", "orders.inbound"},
		{"QUEUE_DLQ", "orders.dlq"},
	}
	if got := leafStrings(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("env leaves\n got %v\nwant %v", got, want)
	}
	if a.Leaves[0].Line != 2 || a.Leaves[1].Line != 3 {
		t.Fatalf("env line numbers = %d,%d", a.Leaves[0].Line, a.Leaves[1].Line)
	}
}

func TestReadFile_UnknownExtension(t *testing.T) {
	if _, ok := ReadFile("x.toml", []byte("k = 1")); ok {
		t.Fatal("toml should not be claimed (no reader)")
	}
}

func TestReadFile_ParseFailureIsNotAnArtifact(t *testing.T) {
	if _, ok := ReadFile("x.json", []byte("{not json")); ok {
		t.Fatal("malformed json should not read")
	}
}
