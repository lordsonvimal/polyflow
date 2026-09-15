package pipeline_test

// FX.8.23 (2026-09-15): gorm_tables — Tier FX migration of
// internal/linker/gorm_tables.go's LinkGormModelTables (Tier GT), replaced
// by patterns/go/gorm_tables.yaml + rules/go/gorm_tables.dl, driven by the
// "gorm_tables" hub provider (internal/factpipe/hub_gorm_tables.go).
//
// This framework is hub-only (no `patterns:`/`facts:` block): resolving a
// model's TableName() body, a local var's declared type, or a .Model()/
// .Table() chain needs real Go over sibling files, so every input is either
// a graph.Node the fixture hands the hub directly or a temp-dir Go source
// file the hub reads via os.ReadFile — mirroring the retired pass's own
// tests, ported near-verbatim, plus a real *_test.go file for the
// TableName()/local-var-type cases that need actual source to parse.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func gtActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("gorm_tables")
	if fw == nil {
		t.Fatal("gorm_tables framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func gtRun(t *testing.T, nodes []graph.Node) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(gtActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func gtSQLTable(id, service, file, name string) graph.Node {
	return graph.Node{ID: id, Type: graph.NodeTypeTable, Label: name, Service: service, File: file}
}

func gtCallNode(id, service, target, op string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeDatastore, Service: service,
		File: "svc/repo.go", Line: 10,
		Meta: map[string]string{"kind": "call", "op": op, "target": target},
	}
}

func gtWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGormTablesRule_TableNameMethod: a `func (ExecConfig) TableName()`
// returning "maple_exec_configs" wires a gorm_persist on &models.ExecConfig{}
// to the schema table node.
func TestGormTablesRule_TableNameMethod(t *testing.T) {
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "exec_config.go")
	gtWrite(t, modelFile, "package models\n\nfunc (ExecConfig) TableName() string {\n\treturn \"maple_exec_configs\"\n}\n")

	nodes := []graph.Node{
		gtSQLTable("t:exec", "maple-manager", "db/schema.sql", "maple_exec_configs"),
		{ID: "m:tablename", Type: graph.NodeTypeMethod, Label: "TableName",
			Service: "maple-manager", File: modelFile, Line: 3, EndLine: 5,
			Meta: map[string]string{"receiver": "ExecConfig"}},
		gtCallNode("d:create", "maple-manager", "&models.ExecConfig{\n\tConfigID: \"x\",\n}", "persist"),
	}

	res := gtRun(t, nodes)
	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Edges) != 1 {
		t.Fatalf("want 1 edge, got %d: %+v", len(res.Edges), res.Edges)
	}
	e := res.Edges[0]
	if e.From != "d:create" || e.To != "t:exec" || e.Type != graph.EdgeTypePersists {
		t.Errorf("bad edge: %+v", e)
	}
	if e.Meta["model"] != "ExecConfig" || e.Meta["table"] != "maple_exec_configs" {
		t.Errorf("bad edge meta: %+v", e.Meta)
	}
}

// TestGormTablesRule_TableLiteral: db.Table("audits") names the table
// outright via meta["table_name"], no model resolution needed.
func TestGormTablesRule_TableLiteral(t *testing.T) {
	nodes := []graph.Node{
		gtSQLTable("t:audits", "svc", "db/schema.sql", "audits"),
		{ID: "d:find", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 4,
			Meta: map[string]string{"kind": "call", "op": "query", "table_name": `"audits"`}},
	}
	res := gtRun(t, nodes)
	if len(res.Unresolved) != 0 || len(res.Edges) != 1 {
		t.Fatalf("want 1 edge 0 unresolved, got %d/%d", len(res.Edges), len(res.Unresolved))
	}
	if res.Edges[0].Type != graph.EdgeTypeQueries || res.Edges[0].To != "t:audits" {
		t.Errorf("bad edge: %+v", res.Edges[0])
	}
}

// TestGormTablesRule_Convention: no TableName() method, but the
// snake-plural convention lands on a real schema table -> edge. A model with
// no schema match -> gorm_model_unresolved ledger, no edge.
func TestGormTablesRule_Convention(t *testing.T) {
	nodes := []graph.Node{
		gtSQLTable("t:base_images", "svc", "db/schema.sql", "base_images"),
		{ID: "s:baseimage", Type: graph.NodeTypeStruct, Label: "BaseImage", Service: "svc"},
		{ID: "s:widget", Type: graph.NodeTypeStruct, Label: "Widget", Service: "svc"},
		gtCallNode("d:1", "svc", "&BaseImage{}", "persist"),
		gtCallNode("d:2", "svc", "&Widget{}", "persist"),
	}
	res := gtRun(t, nodes)
	if len(res.Edges) != 1 || res.Edges[0].To != "t:base_images" {
		t.Fatalf("want 1 edge to base_images, got %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "gorm_model_unresolved" || res.Unresolved[0].Name != "Widget" {
		t.Fatalf("want Widget gorm_model_unresolved, got %+v", res.Unresolved)
	}
}

// TestGormTablesRule_LocalVarSilent: a lower-case target (a local var, not
// an exported model) yields neither an edge nor a ledger entry.
func TestGormTablesRule_LocalVarSilent(t *testing.T) {
	nodes := []graph.Node{
		gtSQLTable("t:users", "svc", "db/schema.sql", "users"),
		gtCallNode("d:1", "svc", "&user", "query"),
	}
	res := gtRun(t, nodes)
	if len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Fatalf("want nothing, got edges=%+v unresolved=%+v", res.Edges, res.Unresolved)
	}
}

// TestGormTablesRule_PreferSchemaSQL: a name declared in both schema.sql and
// a migration collapses to the schema.sql node, no collision ledger.
func TestGormTablesRule_PreferSchemaSQL(t *testing.T) {
	nodes := []graph.Node{
		gtSQLTable("t:schema", "svc", "db/schema.sql", "orders"),
		gtSQLTable("t:migr", "svc", "db/migrations/001_orders.up.sql", "orders"),
		{ID: "d:1", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 1,
			Meta: map[string]string{"kind": "call", "op": "persist", "table_name": `"orders"`}},
	}
	res := gtRun(t, nodes)
	if len(res.Edges) != 1 || res.Edges[0].To != "t:schema" {
		t.Fatalf("want 1 edge to schema node, got %+v", res.Edges)
	}
	if len(res.Unresolved) != 0 {
		t.Fatalf("want no collision ledger, got %+v", res.Unresolved)
	}
}

// TestGormTablesRule_EnclosingReceiver: a chain-form write whose finisher
// arg is an unresolvable column map resolves via the enclosing
// *ExecConfigRepository method receiver.
func TestGormTablesRule_EnclosingReceiver(t *testing.T) {
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "exec_config.go")
	gtWrite(t, modelFile, "package models\n\nfunc (ExecConfig) TableName() string {\n\treturn \"maple_exec_configs\"\n}\n")

	nodes := []graph.Node{
		gtSQLTable("t:exec", "maple-manager", "db/schema.sql", "maple_exec_configs"),
		{ID: "m:tn", Type: graph.NodeTypeMethod, Label: "TableName", Service: "maple-manager",
			File: modelFile, Line: 3, EndLine: 5, Meta: map[string]string{"receiver": "ExecConfig"}},
		{ID: "m:update", Type: graph.NodeTypeMethod, Label: "UpdateColumns", Service: "maple-manager",
			File: "repo/exec_config_repository.go", Line: 52, EndLine: 60,
			Meta: map[string]string{"receiver": "ExecConfigRepository"}},
		{ID: "d:upd", Type: graph.NodeTypeDatastore, Service: "maple-manager",
			File: "repo/exec_config_repository.go", Line: 53,
			Meta: map[string]string{"kind": "call", "op": "persist", "pattern": "gorm_persist_chain"}},
	}
	res := gtRun(t, nodes)
	if len(res.Unresolved) != 0 || len(res.Edges) != 1 {
		t.Fatalf("want 1 edge 0 unresolved, got %d/%d %+v", len(res.Edges), len(res.Unresolved), res.Unresolved)
	}
	if res.Edges[0].From != "d:upd" || res.Edges[0].To != "t:exec" ||
		res.Edges[0].Type != graph.EdgeTypePersists || res.Edges[0].Meta["model"] != "ExecConfig" {
		t.Errorf("bad edge: %+v", res.Edges[0])
	}
}

// TestGormTablesRule_LocalVarType: r.db.Create(&setting) where the
// enclosing method declares `var setting models.Setting` resolves through
// the Setting model's TableName().
func TestGormTablesRule_LocalVarType(t *testing.T) {
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "setting.go")
	gtWrite(t, modelFile, "package models\n\nfunc (Setting) TableName() string {\n\treturn \"maple_app_settings\"\n}\n")
	repoFile := filepath.Join(dir, "settings_repository.go")
	gtWrite(t, repoFile, "package repository\n\nfunc (r *SettingsRepository) GetOrCreate() error {\n\tvar setting models.Setting\n\tif err := r.db.Create(&setting).Error; err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n")

	nodes := []graph.Node{
		gtSQLTable("t:s", "maple-manager", "db/schema.sql", "maple_app_settings"),
		{ID: "m:tn", Type: graph.NodeTypeMethod, Label: "TableName", Service: "maple-manager",
			File: modelFile, Line: 3, EndLine: 5, Meta: map[string]string{"receiver": "Setting"}},
		{ID: "m:goc", Type: graph.NodeTypeMethod, Label: "GetOrCreate", Service: "maple-manager",
			File: repoFile, Line: 3, EndLine: 9, Meta: map[string]string{"receiver": "SettingsRepository"}},
		{ID: "d:c", Type: graph.NodeTypeDatastore, Service: "maple-manager", File: repoFile, Line: 5,
			Meta: map[string]string{"kind": "call", "op": "persist", "target": "&setting"}},
	}
	res := gtRun(t, nodes)
	if len(res.Unresolved) != 0 || len(res.Edges) != 1 {
		t.Fatalf("want 1 edge 0 unresolved, got %d/%d %+v", len(res.Edges), len(res.Unresolved), res.Unresolved)
	}
	if res.Edges[0].To != "t:s" || res.Edges[0].Type != graph.EdgeTypePersists || res.Edges[0].Meta["model"] != "Setting" {
		t.Errorf("bad edge: %+v", res.Edges[0])
	}
}

// TestGormTablesRule_ChainModelScan: a `db.Model(&models.ExecConfig{}).
// Where(…).UpdateColumn(…)` inside a plain func (no receiver) resolves via
// the .Model() argument scanned out of the chain statement.
func TestGormTablesRule_ChainModelScan(t *testing.T) {
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "exec_config.go")
	gtWrite(t, modelFile, "package models\n\nfunc (ExecConfig) TableName() string {\n\treturn \"maple_exec_configs\"\n}\n")
	repoFile := filepath.Join(dir, "exec_config_repository.go")
	gtWrite(t, repoFile, "package repository\n\nfunc makeCurrentTx(db *gorm.DB, id string) error {\n\treturn db.Model(&models.ExecConfig{}).\n\t\tWhere(\"config_id = ?\", id).\n\t\tUpdateColumn(\"is_current\", false).Error\n}\n")

	nodes := []graph.Node{
		gtSQLTable("t:exec", "maple-manager", "db/schema.sql", "maple_exec_configs"),
		{ID: "m:tn", Type: graph.NodeTypeMethod, Label: "TableName", Service: "maple-manager",
			File: modelFile, Line: 3, EndLine: 5, Meta: map[string]string{"receiver": "ExecConfig"}},
		{ID: "d:uc", Type: graph.NodeTypeDatastore, Service: "maple-manager", File: repoFile, Line: 6,
			Meta: map[string]string{"kind": "call", "op": "persist", "pattern": "gorm_persist_chain"}},
	}
	res := gtRun(t, nodes)
	if len(res.Unresolved) != 0 || len(res.Edges) != 1 || res.Edges[0].To != "t:exec" {
		t.Fatalf("want 1 edge to t:exec, got %d/%d %+v", len(res.Edges), len(res.Unresolved), res.Edges)
	}
}

// TestGormTablesRule_TableCollision: a table name matching more than one
// non-schema.sql node ledgers a collision alongside the fanned-out edges.
func TestGormTablesRule_TableCollision(t *testing.T) {
	nodes := []graph.Node{
		gtSQLTable("t:a", "svc", "db/migrations/001.up.sql", "widgets"),
		gtSQLTable("t:b", "svc", "db/migrations/002.up.sql", "widgets"),
		{ID: "d:1", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 1,
			Meta: map[string]string{"kind": "call", "op": "query", "table_name": `"widgets"`}},
	}
	res := gtRun(t, nodes)
	if len(res.Edges) != 2 {
		t.Fatalf("want 2 fanned-out edges, got %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "gorm_table_collision" || res.Unresolved[0].Name != "widgets" {
		t.Fatalf("want 1 gorm_table_collision, got %+v", res.Unresolved)
	}
}

// TestGormTablesRule_TableUnresolved: a literal table name with no matching
// schema node ledgers gorm_table_unresolved, no edge.
func TestGormTablesRule_TableUnresolved(t *testing.T) {
	nodes := []graph.Node{
		{ID: "d:1", Type: graph.NodeTypeDatastore, Service: "svc", File: "svc/a.go", Line: 7,
			Meta: map[string]string{"kind": "call", "op": "query", "table_name": `"ghosts"`}},
	}
	res := gtRun(t, nodes)
	if len(res.Edges) != 0 {
		t.Fatalf("want no edges, got %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "gorm_table_unresolved" || res.Unresolved[0].Name != "ghosts" {
		t.Fatalf("want 1 gorm_table_unresolved, got %+v", res.Unresolved)
	}
}
