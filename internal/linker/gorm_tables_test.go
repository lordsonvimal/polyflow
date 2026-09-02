package linker

import (
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func gormCallNode(id, service, target, op string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeDatastore, Service: service,
		File: "svc/repo.go", Line: 10,
		Meta: map[string]string{"kind": "call", "op": op, "target": target},
	}
}

// TestLinkGormModelTables_TableNameMethod: a `func (ExecConfig) TableName()`
// returning "maple_exec_configs" wires a gorm_persist on &models.ExecConfig{}
// to the schema table node.
func TestLinkGormModelTables_TableNameMethod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "exec_config.go")
	mustWrite(t, modelFile, "package models\n\nfunc (ExecConfig) TableName() string {\n\treturn \"maple_exec_configs\"\n}\n")

	nodes := []graph.Node{
		makeSQLTableNode("t:exec", "maple-manager", "db/schema.sql", "maple_exec_configs"),
		{ID: "m:tablename", Type: graph.NodeTypeMethod, Label: "TableName",
			Service: "maple-manager", File: modelFile, Line: 3, EndLine: 5,
			Meta: map[string]string{"receiver": "ExecConfig"}},
		gormCallNode("d:create", "maple-manager", "&models.ExecConfig{\n\tConfigID: \"x\",\n}", "persist"),
	}

	edges, unresolved := LinkGormModelTables(nodes)
	if len(unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", unresolved)
	}
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.From != "d:create" || e.To != "t:exec" || e.Type != graph.EdgeTypePersists {
		t.Errorf("bad edge: %+v", e)
	}
	if e.Meta["model"] != "ExecConfig" || e.Meta["table"] != "maple_exec_configs" {
		t.Errorf("bad edge meta: %+v", e.Meta)
	}
}

// TestLinkGormModelTables_TableLiteral: db.Table("audits") names the table
// outright via meta["table_name"], no model resolution needed.
func TestLinkGormModelTables_TableLiteral(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeSQLTableNode("t:audits", "svc", "db/schema.sql", "audits"),
		{ID: "d:find", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 4,
			Meta: map[string]string{"kind": "call", "op": "query", "table_name": `"audits"`}},
	}
	edges, unresolved := LinkGormModelTables(nodes)
	if len(unresolved) != 0 || len(edges) != 1 {
		t.Fatalf("want 1 edge 0 unresolved, got %d/%d", len(edges), len(unresolved))
	}
	if edges[0].Type != graph.EdgeTypeQueries || edges[0].To != "t:audits" {
		t.Errorf("bad edge: %+v", edges[0])
	}
}

// TestLinkGormModelTables_Convention: no TableName() method, but the
// snake-plural convention lands on a real schema table -> edge. A model with
// no schema match -> gorm_model_unresolved ledger, no edge.
func TestLinkGormModelTables_Convention(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeSQLTableNode("t:base_images", "svc", "db/schema.sql", "base_images"),
		{ID: "s:baseimage", Type: graph.NodeTypeStruct, Label: "BaseImage", Service: "svc"},
		{ID: "s:widget", Type: graph.NodeTypeStruct, Label: "Widget", Service: "svc"},
		gormCallNode("d:1", "svc", "&BaseImage{}", "persist"),
		gormCallNode("d:2", "svc", "&Widget{}", "persist"),
	}
	edges, unresolved := LinkGormModelTables(nodes)
	if len(edges) != 1 || edges[0].To != "t:base_images" {
		t.Fatalf("want 1 edge to base_images, got %+v", edges)
	}
	if len(unresolved) != 1 || unresolved[0].Kind != "gorm_model_unresolved" || unresolved[0].Name != "Widget" {
		t.Fatalf("want Widget gorm_model_unresolved, got %+v", unresolved)
	}
}

// TestLinkGormModelTables_LocalVarSilent: a lower-case target (a local var,
// not an exported model) yields neither an edge nor a ledger entry.
func TestLinkGormModelTables_LocalVarSilent(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeSQLTableNode("t:users", "svc", "db/schema.sql", "users"),
		gormCallNode("d:1", "svc", "&user", "query"),
	}
	edges, unresolved := LinkGormModelTables(nodes)
	if len(edges) != 0 || len(unresolved) != 0 {
		t.Fatalf("want nothing, got edges=%+v unresolved=%+v", edges, unresolved)
	}
}

// TestLinkGormModelTables_PreferSchemaSQL: a name declared in both schema.sql
// and a migration collapses to the schema.sql node, no collision ledger.
func TestLinkGormModelTables_PreferSchemaSQL(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeSQLTableNode("t:schema", "svc", "db/schema.sql", "orders"),
		makeSQLTableNode("t:migr", "svc", "db/migrations/001_orders.up.sql", "orders"),
		{ID: "d:1", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 1,
			Meta: map[string]string{"kind": "call", "op": "persist", "table_name": `"orders"`}},
	}
	edges, unresolved := LinkGormModelTables(nodes)
	if len(edges) != 1 || edges[0].To != "t:schema" {
		t.Fatalf("want 1 edge to schema node, got %+v", edges)
	}
	if len(unresolved) != 0 {
		t.Fatalf("want no collision ledger, got %+v", unresolved)
	}
}

func TestGormTableConvention(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ExecConfig": "exec_configs",
		"AppConfig":  "app_configs",
		"BaseImage":  "base_images",
		"APIKey":     "api_keys",
		"Category":   "categories",
		"Address":    "addresses",
		"Box":        "boxes",
	}
	for in, want := range cases {
		if got := gormTableConvention(in); got != want {
			t.Errorf("gormTableConvention(%q) = %q, want %q", in, got, want)
		}
	}
}
