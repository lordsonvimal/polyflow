package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_RailsModelTables is Tier AT end to end: db/schema.rb mints table
// nodes (AT.1) and app/models classes reach them over `backed_by` (AT.2),
// through the real indexer rather than a hand-built node slice.
//
// The dangling-endpoint assertion is the point of running this at the indexer
// level at all — `backed_by` is the first edge type whose target is a node
// minted by a *pattern* in one language and whose source comes from the
// structural pass of another, which is exactly the shape that trips the
// FOREIGN KEY bug recorded as checklist item 10 in docs/phases.md.
func TestRun_RailsModelTables(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "app", "models"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "db", "migrate"), 0o755))

	// rails_model_tables is Tier FX FX.8 — a package-gated framework pass, so
	// the fixture has to resolve railties the way a real Rails app does.
	writeFile(t, svc, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\n")
	writeFile(t, svc, "Gemfile.lock", `GEM
  remote: https://rubygems.org/
  specs:
    activerecord (7.1.3)
    railties (7.1.3)
    rails (7.1.3)

PLATFORMS
  ruby

DEPENDENCIES
  rails
`)

	writeFile(t, filepath.Join(svc, "db"), "schema.rb", `ActiveRecord::Schema[7.1].define(version: 2026_01_01_000000) do
  create_table "widgets", force: :cascade do |t|
    t.string "name"
    t.bigint "gadget_id"
  end
  create_table "gadgets", force: :cascade do |t|
    t.string "label"
  end
  create_table "legacy_things", force: :cascade do |t|
    t.string "note"
  end
end
`)
	// The same tables, declared again in a migration. Not one extra node.
	writeFile(t, filepath.Join(svc, "db", "migrate"), "20260101000000_create_widgets.rb",
		`class CreateWidgets < ActiveRecord::Migration[7.1]
  def change
    create_table "widgets", force: :cascade do |t|
      t.string "name"
    end
  end
end
`)

	models := filepath.Join(svc, "app", "models")
	writeFile(t, models, "application_record.rb", `class ApplicationRecord < ActiveRecord::Base
  self.abstract_class = true
end
`)
	writeFile(t, models, "widget.rb", `class Widget < ApplicationRecord
end
`)
	writeFile(t, models, "thing.rb", `class Thing < ApplicationRecord
  self.table_name = "legacy_things"
end
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "orion", Path: svc, Language: "ruby"}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	byID := idx.Nodes
	tables := map[string]*graph.Node{}
	for _, n := range byID {
		if n.Type == graph.NodeTypeTable {
			assert.NotContains(t, tables, n.Label,
				"two table nodes named %s — the migration gate leaked", n.Label)
			tables[n.Label] = n
		}
	}
	require.Len(t, tables, 3, "exactly the three tables db/schema.rb declares")
	assert.Equal(t, "name,gadget_id", tables["widgets"].Meta["columns"])

	backed := map[string]string{} // class label -> table label
	for _, e := range idx.AllEdges() {
		require.Contains(t, byID, e.From, "edge %s has a dangling From", e.ID)
		require.Contains(t, byID, e.To, "edge %s has a dangling To", e.ID)
		if e.Type == graph.EdgeTypeBackedBy {
			backed[byID[e.From].Label] = byID[e.To].Label
		}
	}
	assert.Equal(t, map[string]string{"Widget": "widgets", "Thing": "legacy_things"}, backed)

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	unowned := map[string]bool{}
	for _, u := range unresolved {
		if u.Kind == "rails_table_unowned" {
			unowned[u.Name] = true
		}
		assert.NotEqual(t, "rails_model_table_unresolved", u.Kind,
			"every model here resolves: %+v", u)
	}
	assert.Equal(t, map[string]bool{"gadgets": true}, unowned,
		"gadgets has no model in this fixture, and the ledger row is the graph saying so")
}
