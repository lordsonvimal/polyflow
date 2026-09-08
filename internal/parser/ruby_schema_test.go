package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// schemaFixture is the AT.1 worked example from
// docs/cedar-end-to-end-flow-holes-plan.md: three tables, one of them
// carrying a non-column macro (t.index) that must not be mistaken for a
// column, and one carrying `t.timestamps`, which declares columns Rails
// invents at runtime and the dump does not name.
const schemaFixture = `ActiveRecord::Schema[7.1].define(version: 2026_01_01_000000) do
  create_table "widgets", force: :cascade do |t|
    t.string "name"
    t.bigint "gadget_id", null: false
    t.index ["gadget_id"], name: "index_widgets_on_gadget_id"
  end

  create_table "gadgets", id: false, force: :cascade do |t|
    t.string "label"
    t.timestamps
  end

  create_table "legacy_things", force: :cascade do |t|
    t.text "note"
  end
end
`

func tableNodes(nodes []graph.Node) map[string]graph.Node {
	out := map[string]graph.Node{}
	for _, n := range nodes {
		if n.Type == graph.NodeTypeTable {
			out[n.Label] = n
		}
	}
	return out
}

func TestRailsSchema_MintsOneTableNodePerCreateTable(t *testing.T) {
	t.Parallel()
	tables := tableNodes(parseRubyAt(t, "db/schema.rb", schemaFixture))
	require.Len(t, tables, 3)

	require.Equal(t, "name,gadget_id", tables["widgets"].Meta["columns"],
		"t.index is a non-column macro and must not appear in the column list")
	require.Equal(t, "label", tables["gadgets"].Meta["columns"],
		"t.timestamps names no column, so it contributes none")
	require.Equal(t, "note", tables["legacy_things"].Meta["columns"])

	for name, n := range tables {
		require.Equal(t, "rails_schema", n.Meta["source"], "table %s", name)
	}
}

// TestRailsSchema_MigrationMintsNothing is the gate that stops the
// duplicate-table regression: cedar's db/migrate holds 537+ create_table
// calls, creating and dropping the same tables across dozens of files.
func TestRailsSchema_MigrationMintsNothing(t *testing.T) {
	t.Parallel()
	nodes := parseRubyAt(t, "db/migrate/20260101000000_add_widgets.rb", `class AddWidgets < ActiveRecord::Migration[7.1]
  def change
    create_table "widgets", force: :cascade do |t|
      t.string "name"
    end
  end
end
`)
	require.Empty(t, tableNodes(nodes))
}

// TestRailsSchema_MultiDatabaseSchemaFile — a multi-database Rails app dumps
// db/<name>_schema.rb per connection; all of them are real schema dumps.
func TestRailsSchema_MultiDatabaseSchemaFile(t *testing.T) {
	t.Parallel()
	tables := tableNodes(parseRubyAt(t, "db/analytics_schema.rb", schemaFixture))
	require.Len(t, tables, 3)
}

// TestRailsSchema_CommentedOutCreateTableMintsNothing — a create_table inside
// a =begin/=end block is a comment, and tree-sitter parses it as one.
func TestRailsSchema_CommentedOutCreateTableMintsNothing(t *testing.T) {
	t.Parallel()
	nodes := parseRubyAt(t, "db/schema.rb", `ActiveRecord::Schema[7.1].define(version: 1) do
=begin
  create_table "widgets", force: :cascade do |t|
    t.string "name"
  end
=end
end
`)
	require.Empty(t, tableNodes(nodes))
}

func TestIsRailsSchemaFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file string
		want bool
	}{
		{"db/schema.rb", true},
		{"db/primary_schema.rb", true},
		{"/abs/path/orion/db/schema.rb", true},
		{"db/migrate/20260101000000_add_widgets.rb", false},
		{"app/models/schema.rb", false},
		{"db/seeds.rb", false},
		{"schema.rb", false},
	} {
		require.Equal(t, tc.want, isRailsSchemaFile(tc.file), tc.file)
	}
}
