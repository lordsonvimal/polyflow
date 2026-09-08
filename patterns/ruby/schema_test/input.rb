ActiveRecord::Schema[7.1].define(version: 2026_01_01_000000) do
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
