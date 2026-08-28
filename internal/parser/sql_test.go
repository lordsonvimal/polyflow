package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sqlPatternsDir = "../../patterns"

// parseSQLSrc runs the SQLParser on inline source written to a temp file —
// the real parser->matcher path (rule 6), not hand-built nodes.
func parseSQLSrc(t *testing.T, filename, src string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(sqlPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &SQLParser{}
	nodes, edges, unresolved, err := p.Parse(file, "svc", m, nil)
	require.NoError(t, err)
	return nodes, edges, unresolved
}

func sqlNode(nodes []graph.Node, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeTable && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

// TestSQL_CreateTableWithColumns verifies SQ0's acceptance criterion: a
// migration file with two CREATE TABLE statements indexes to two table
// nodes with correct meta["columns"].
func TestSQL_CreateTableWithColumns(t *testing.T) {
	t.Parallel()
	src := `CREATE TABLE users (
  id INT PRIMARY KEY,
  name VARCHAR(255) NOT NULL
);

CREATE TABLE orders (
  id INT PRIMARY KEY,
  user_id INT
);

ALTER TABLE orders ADD COLUMN status VARCHAR(50);
`
	nodes, _, _ := parseSQLSrc(t, "schema.sql", src)

	users := sqlNode(nodes, "users")
	orders := sqlNode(nodes, "orders")
	require.NotNil(t, users, "users table missing")
	require.NotNil(t, orders, "orders table missing")
	assert.Equal(t, "sql", users.Language)

	var userCols []sqlColumn
	require.NoError(t, json.Unmarshal([]byte(users.Meta["columns"]), &userCols))
	require.Len(t, userCols, 2)
	assert.Equal(t, "id", userCols[0].Name)
	assert.Equal(t, "name", userCols[1].Name)

	var orderCols []sqlColumn
	require.NoError(t, json.Unmarshal([]byte(orders.Meta["columns"]), &orderCols))
	require.Len(t, orderCols, 3, "orders should have id, user_id, and the ALTER-added status column")
	assert.Equal(t, "status", orderCols[2].Name)
}

// TestSQL_SchemaQualifiedNameMintsOneTable is the regression fixture for the
// object_reference dialect gap found during SQ3 corpus authoring: a
// schema-qualified name (`CREATE TABLE public.maple_agents (...)`, real
// pg_dump syntax) has TWO identifier children under object_reference
// ("public", "maple_agents"), not one. An unanchored `(identifier) @name`
// capture matches both separately, minting a spurious "public"-labeled
// table node per qualified CREATE TABLE. schema.yaml's trailing `.` anchor
// pins @name to the LAST identifier child, so exactly one table node —
// named after the real table, not the schema — comes out.
func TestSQL_SchemaQualifiedNameMintsOneTable(t *testing.T) {
	t.Parallel()
	src := `CREATE TABLE public.maple_agents (
  id SERIAL PRIMARY KEY,
  name TEXT
);
`
	nodes, _, _ := parseSQLSrc(t, "schema.sql", src)

	var tables []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeTable {
			tables = append(tables, n)
		}
	}
	require.Len(t, tables, 1, "a schema-qualified CREATE TABLE must mint exactly one table node, not one per qualifier segment")
	assert.Equal(t, "maple_agents", tables[0].Label, "the table node must be labeled after the real table name, not the schema qualifier")
}

// TestSQL_AlterTableEmitsSelfEdge verifies ALTER TABLE ADD/DROP COLUMN never
// mints a new node (it's a mutation of the same entity) and instead emits an
// edge onto the existing table node.
func TestSQL_AlterTableEmitsSelfEdge(t *testing.T) {
	t.Parallel()
	src := `CREATE TABLE widgets (id INT PRIMARY KEY);
ALTER TABLE widgets ADD COLUMN weight INT;
ALTER TABLE widgets DROP COLUMN weight;
`
	nodes, edges, _ := parseSQLSrc(t, "alter.sql", src)

	widgets := sqlNode(nodes, "widgets")
	require.NotNil(t, widgets)

	var tableNodeCount int
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeTable {
			tableNodeCount++
		}
	}
	assert.Equal(t, 1, tableNodeCount, "ALTER must not mint a second table node")

	var addEdge, dropEdge bool
	for _, e := range edges {
		if e.From != widgets.ID || e.To != widgets.ID || e.Type != graph.EdgeTypeCalls {
			continue
		}
		switch e.Meta["op"] {
		case "add_column":
			addEdge = true
		case "drop_column":
			dropEdge = true
		}
	}
	assert.True(t, addEdge, "expected an add_column alter edge onto the table node")
	assert.True(t, dropEdge, "expected a drop_column alter edge onto the table node")

	var cols []sqlColumn
	require.NoError(t, json.Unmarshal([]byte(widgets.Meta["columns"]), &cols))
	require.Len(t, cols, 2)
	assert.Equal(t, "weight", cols[1].Name)
	assert.True(t, cols[1].Dropped)
}

// TestSQL_CreateViewGetsTableTypeWithKindMeta verifies CREATE VIEW mints the
// same NodeTypeTable as CREATE TABLE, discriminated by meta["kind"]="view".
func TestSQL_CreateViewGetsTableTypeWithKindMeta(t *testing.T) {
	t.Parallel()
	src := `CREATE TABLE users (id INT PRIMARY KEY);
CREATE VIEW active_users AS SELECT * FROM users WHERE id > 0;
`
	nodes, _, _ := parseSQLSrc(t, "view.sql", src)

	view := sqlNode(nodes, "active_users")
	require.NotNil(t, view, "active_users view node missing")
	assert.Equal(t, "view", view.Meta["kind"])
}

// TestSQL_CreateIndexAndDropTableAreMetaOnly verifies CREATE INDEX and DROP
// TABLE never mint their own node — they merge onto the existing table.
func TestSQL_CreateIndexAndDropTableAreMetaOnly(t *testing.T) {
	t.Parallel()
	src := `CREATE TABLE users (id INT PRIMARY KEY, name VARCHAR(255));
CREATE INDEX idx_users_name ON users(name);
CREATE TABLE orders (id INT PRIMARY KEY);
DROP TABLE orders;
`
	nodes, _, _ := parseSQLSrc(t, "index_drop.sql", src)

	var tableNodeCount int
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeTable {
			tableNodeCount++
		}
	}
	assert.Equal(t, 2, tableNodeCount, "CREATE INDEX/DROP TABLE must not add new table nodes")

	users := sqlNode(nodes, "users")
	require.NotNil(t, users)
	assert.Contains(t, users.Meta["indexes"], "idx_users_name")

	orders := sqlNode(nodes, "orders")
	require.NotNil(t, orders)
	assert.Equal(t, "true", orders.Meta["dropped"])
}

// TestSQL_CommentedOutCreateTableIsNotCaptured proves rule 11's comment-
// blanking discipline: a comment whose TEXT contains "CREATE TABLE" is not
// parsed as a real declaration by the grammar.
func TestSQL_CommentedOutCreateTableIsNotCaptured(t *testing.T) {
	t.Parallel()
	src := "-- CREATE TABLE fake (id INT);\nCREATE TABLE real_one (id INT);\n"
	nodes, _, _ := parseSQLSrc(t, "comment.sql", src)

	assert.Nil(t, sqlNode(nodes, "fake"), "a CREATE TABLE inside a comment must not become a node")
	require.NotNil(t, sqlNode(nodes, "real_one"))
}

// TestSQL_QuotedIdentifiersNormalize proves rule 11's identifier-quote strip
// (backtick, double-quote, and the grammar's own bracket handling) all
// normalize to the bare table name.
func TestSQL_QuotedIdentifiersNormalize(t *testing.T) {
	t.Parallel()
	src := "CREATE TABLE `backtick_table` (id INT);\n" +
		`CREATE TABLE "doublequote_table" (id INT);` + "\n" +
		"CREATE TABLE [bracket_table] (id INT);\n"
	nodes, _, _ := parseSQLSrc(t, "quoted.sql", src)

	require.NotNil(t, sqlNode(nodes, "backtick_table"))
	require.NotNil(t, sqlNode(nodes, "doublequote_table"))
	require.NotNil(t, sqlNode(nodes, "bracket_table"))
}
