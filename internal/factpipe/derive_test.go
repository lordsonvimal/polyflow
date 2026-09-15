package factpipe

import "testing"

func controllerKeyDerive() CompiledDerive {
	sep := "/"
	return CompiledDerive{spec: DeriveSpec{
		Relation:  "controller_key",
		From:      "node_meta",
		Columns:   []string{"controller_module", "resource"},
		Separator: &sep,
	}}
}

func TestCompileDeriveSpecs_Valid(t *testing.T) {
	sep := "/"
	ds, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "controller_key", From: "node_meta",
		Columns: []string{"controller_module", "resource"}, Separator: &sep,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds) != 1 || ds[0].Relation() != "controller_key" {
		t.Fatalf("got %#v", ds)
	}
}

func TestCompileDeriveSpecs_MissingRelation(t *testing.T) {
	sep := "/"
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		From: "node_meta", Columns: []string{"a", "b"}, Separator: &sep,
	}})
	if err == nil {
		t.Fatal("want error for missing relation")
	}
}

func TestCompileDeriveSpecs_FromMustBeNodeMeta(t *testing.T) {
	sep := "/"
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "x", From: "sass_import", Columns: []string{"a", "b"}, Separator: &sep,
	}})
	if err == nil {
		t.Fatal("want error when from != node_meta")
	}
}

func TestCompileDeriveSpecs_NeedsAtLeastTwoColumns(t *testing.T) {
	sep := "/"
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "x", From: "node_meta", Columns: []string{"a"}, Separator: &sep,
	}})
	if err == nil {
		t.Fatal("want error for < 2 columns")
	}
}

func TestCompileDeriveSpecs_EmptyColumnNameRejected(t *testing.T) {
	sep := "/"
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "x", From: "node_meta", Columns: []string{"a", ""}, Separator: &sep,
	}})
	if err == nil {
		t.Fatal("want error for empty column name")
	}
}

func TestCompileDeriveSpecs_MissingSeparatorRejected(t *testing.T) {
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "x", From: "node_meta", Columns: []string{"a", "b"},
	}})
	if err == nil {
		t.Fatal("want error for absent separator key")
	}
}

func TestCompileDeriveSpecs_EmptySeparatorAccepted(t *testing.T) {
	empty := ""
	_, err := CompileDeriveSpecs([]DeriveSpec{{
		Relation: "x", From: "node_meta", Columns: []string{"a", "b"}, Separator: &empty,
	}})
	if err != nil {
		t.Fatalf("empty separator must be valid: %v", err)
	}
}

func TestApplyDerive_JoinsBothColumnsPresent(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("controller_module"), Str("client_api/v1")}})
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("resource"), Str("lros")}})

	ApplyDerive([]CompiledDerive{controllerKeyDerive()}, fs)

	got := findFact(fs, "controller_key")
	if got == nil {
		t.Fatal("expected a controller_key fact")
	}
	if got.Args[0].Node != "n1" || got.Args[1].Str != "client_api/v1/lros" {
		t.Errorf("got %#v", got.Args)
	}
}

func TestApplyDerive_EmptyColumnStillJoins(t *testing.T) {
	// controller_module == "" is a legal, present value (a top-level
	// controller) — the join still fires, it just has an empty first part.
	fs := NewFactSet()
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("controller_module"), Str("")}})
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("resource"), Str("lros")}})

	ApplyDerive([]CompiledDerive{controllerKeyDerive()}, fs)

	got := findFact(fs, "controller_key")
	if got == nil {
		t.Fatal("expected a controller_key fact")
	}
	if got.Args[1].Str != "/lros" {
		t.Errorf("got %q, want \"/lros\" (empty first column still joins with the separator)", got.Args[1].Str)
	}
}

func TestApplyDerive_MissingColumnEmitsNothing(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("controller_module"), Str("client_api/v1")}})
	// no "resource" key at all on n1

	ApplyDerive([]CompiledDerive{controllerKeyDerive()}, fs)

	if got := findFact(fs, "controller_key"); got != nil {
		t.Errorf("expected no controller_key fact, got %#v", got)
	}
}

func TestApplyDerive_NoDerivesIsNoop(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("controller_module"), Str("a")}})
	before := fs.Len()
	ApplyDerive(nil, fs)
	if fs.Len() != before {
		t.Errorf("ApplyDerive(nil, ...) must be a no-op")
	}
}

func TestApplyDerive_MultiNodeIndependentJoins(t *testing.T) {
	fs := NewFactSet()
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("controller_module"), Str("admin")}})
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n1"), Str("resource"), Str("reports")}})
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n2"), Str("controller_module"), Str("client_api")}})
	fs.Add(Fact{Pred: "node_meta", Args: []Atom{Node("n2"), Str("resource"), Str("reports")}})

	ApplyDerive([]CompiledDerive{controllerKeyDerive()}, fs)

	vals := map[string]string{}
	for _, f := range fs.All() {
		if f.Pred == "controller_key" {
			vals[f.Args[0].Node] = f.Args[1].Str
		}
	}
	if vals["n1"] != "admin/reports" || vals["n2"] != "client_api/reports" {
		t.Errorf("got %#v", vals)
	}
}

func findFact(fs FactSet, pred string) *Fact {
	for _, f := range fs.All() {
		if f.Pred == pred {
			f := f
			return &f
		}
	}
	return nil
}
