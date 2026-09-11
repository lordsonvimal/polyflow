package factpipe

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTableFixture lays out one directory from a path->content map and
// returns it, for ApplyTable's svcPath argument.
func writeTableFixture(t *testing.T, files map[string]string) string {
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

func TestCompileTableSpecs(t *testing.T) {
	if _, err := CompileTableSpecs([]TableSpec{{Rows: [][]string{{"a"}}}}); err == nil {
		t.Error("missing relation should fail")
	}
	if _, err := CompileTableSpecs([]TableSpec{{Relation: "x"}}); err == nil {
		t.Error("neither rows nor artifact should fail")
	}
	if _, err := CompileTableSpecs([]TableSpec{{
		Relation: "x", Rows: [][]string{{"a"}}, Artifact: "endpoint_table", Against: "y",
	}}); err == nil {
		t.Error("both rows and artifact should fail")
	}
	if _, err := CompileTableSpecs([]TableSpec{{Relation: "x", Artifact: "endpoint_table"}}); err == nil {
		t.Error("artifact without against should fail")
	}
	if _, err := CompileTableSpecs([]TableSpec{{Relation: "x", Artifact: "no_such_kind", Against: "y"}}); err == nil {
		t.Error("unknown artifact mapping should fail")
	}
	if _, err := CompileTableSpecs([]TableSpec{{Relation: "x", Rows: [][]string{{"a"}}}}); err != nil {
		t.Errorf("valid declarative spec rejected: %v", err)
	}
	if _, err := CompileTableSpecs([]TableSpec{{Relation: "x", Artifact: "endpoint_table", Against: "handler_path"}}); err != nil {
		t.Errorf("valid artifact spec rejected: %v", err)
	}
}

func deviseTable() CompiledTable {
	ts, err := CompileTableSpecs([]TableSpec{{
		Relation: "devise_scope",
		Rows: [][]string{
			{"confirmable", "confirmations"},
			{"recoverable", "passwords"},
		},
	}})
	if err != nil {
		panic(err)
	}
	return ts[0]
}

func TestApplyTable_DeclarativeAssertsEveryRowUnconditionally(t *testing.T) {
	fs := NewFactSet()
	ApplyTable([]CompiledTable{deviseTable()}, "", fs) // no svcPath — declarative doesn't need one

	var got []Fact
	for _, f := range fs.All() {
		if f.Pred == "devise_scope" {
			got = append(got, f)
		}
	}
	if len(got) != 2 {
		t.Fatalf("devise_scope: %d facts, want 2", len(got))
	}
	if got[0].Args[0].Str != "confirmable" || got[0].Args[1].Str != "confirmations" {
		t.Errorf("row 0 = %#v", got[0].Args)
	}
	if got[0].Origin.Kind != OriginPrimitive {
		t.Errorf("origin kind = %d, want OriginPrimitive", got[0].Origin.Kind)
	}
}

func TestApplyTable_NoBlockIsNoop(t *testing.T) {
	fs := NewFactSet()
	ApplyTable(nil, "", fs)
	if fs.Len() != 0 {
		t.Errorf("Len = %d, want 0", fs.Len())
	}
}

func schemaURLTable() CompiledTable {
	ts, err := CompileTableSpecs([]TableSpec{{
		Relation: "schema_url_entity",
		Artifact: "endpoint_table",
		Against:  "handler_path",
	}})
	if err != nil {
		panic(err)
	}
	return ts[0]
}

func TestApplyTable_ArtifactAssertsRowsForQualifyingFile(t *testing.T) {
	dir := writeTableFixture(t, map[string]string{
		"config/routes.json": `{"resources":{
			"widget":{"endpoint":"/api/widgets"},
			"gadget":{"endpoint":"/api/gadgets"},
			"sprocket":{"endpoint":"/api/sprockets"},
			"thing":{"endpoint":"/api/things"},
			"more":{"endpoint":"/api/more"}}}`,
	})
	fs := NewFactSet()
	for _, p := range []string{"/api/widgets", "/api/gadgets", "/api/sprockets", "/api/things", "/api/more"} {
		fs.Add(Fact{Pred: "handler_path", Args: []Atom{Str(p)}})
	}

	ApplyTable([]CompiledTable{schemaURLTable()}, dir, fs)

	var got []Fact
	for _, f := range fs.All() {
		if f.Pred == "schema_url_entity" {
			got = append(got, f)
		}
	}
	if len(got) != 5 {
		t.Fatalf("schema_url_entity: %d facts, want 5: %#v", len(got), got)
	}
	for _, f := range got {
		if f.Origin.File != "config/routes.json" {
			t.Errorf("origin file = %q", f.Origin.File)
		}
	}
}

func TestApplyTable_ArtifactUngatedFileProducesNoRows(t *testing.T) {
	dir := writeTableFixture(t, map[string]string{
		"config/other.json": `{"a":{"note":"unrelated"},"b":{"note":"also unrelated"}}`,
	})
	fs := NewFactSet()
	fs.Add(Fact{Pred: "handler_path", Args: []Atom{Str("/api/widgets")}})

	ApplyTable([]CompiledTable{schemaURLTable()}, dir, fs)

	for _, f := range fs.All() {
		if f.Pred == "schema_url_entity" {
			t.Fatalf("expected no schema_url_entity fact, got %#v", f)
		}
	}
}

func TestApplyTable_ArtifactNoServicePathIsNoop(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "handler_path", Args: []Atom{Str("/api/widgets")}})
	ApplyTable([]CompiledTable{schemaURLTable()}, "", fs)
	if fs.Len() != 1 {
		t.Errorf("Len = %d, want 1 (empty svcPath ⇒ inert, no disk access)", fs.Len())
	}
}
