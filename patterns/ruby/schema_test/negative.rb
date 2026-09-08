# No-false-positives gate. None of these is a schema table declaration.
#
# Note what this file cannot cover: an identical `create_table "…" do |t|` in
# db/migrate/*.rb DOES match this pattern, by design — the query has no view
# of the file path. That gate is enforced in internal/parser/ruby.go and is
# tested in internal/parser/ruby_schema_test.go, not here.

# A method that merely *contains* the name.
create_tables "widgets" do |t|
  t.string "name"
end

# A receiver'd call — connection.create_table is the migration API, not the
# schema dumper's top-level DSL.
connection.create_table "widgets", force: :cascade do |t|
  t.string "name"
end

# No block: a bare reference, not a declaration.
create_table "widgets", force: :cascade

# Block, but the table name is not a literal string.
create_table table_name do |t|
  t.string "name"
end
