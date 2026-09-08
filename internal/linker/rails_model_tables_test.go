package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier AT.2 (docs/cedar-end-to-end-flow-holes-plan.md).
//
// The fixtures below are real files on disk because LinkRailsModelTables
// re-reads model source for `self.table_name` / `self.abstract_class` — the
// same thing every other ruby_* linker pass does. Class and table *nodes* are
// derived from those same files rather than hand-written, so a fixture can
// never drift from the line numbers the pass keys on.

type atFixture struct {
	dir   string
	nodes []graph.Node
	edges []graph.Edge
	files map[string][]string
}

func newATFixture(t *testing.T) *atFixture {
	t.Helper()
	return &atFixture{dir: t.TempDir(), files: map[string][]string{}}
}

func (f *atFixture) write(t *testing.T, svc, rel, src string) string {
	t.Helper()
	abs := filepath.Join(f.dir, svc, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f.files[svc] = append(f.files[svc], abs)
	return abs
}

// schema writes a db/schema.rb and mints the table nodes AT.1's parser would,
// reading the names and lines straight out of the file it just wrote.
func (f *atFixture) schema(t *testing.T, svc, src string) {
	t.Helper()
	abs := f.write(t, svc, "db/schema.rb", src)
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, `create_table "`) {
			continue
		}
		name := strings.SplitN(trimmed[len(`create_table "`):], `"`, 2)[0]
		f.nodes = append(f.nodes, graph.Node{
			ID:      fmt.Sprintf("%s:%s:table:%s:%d", svc, abs, name, i+1),
			Type:    graph.NodeTypeTable,
			Label:   name,
			Service: svc,
			File:    abs,
			Line:    i + 1,
			Meta:    map[string]string{"source": "rails_schema"},
		})
	}
}

// model writes an app/models file and mints one class node per `class`
// declaration in it, plus the `inherits` edge to its superclass —
// LinkRubyTypeRelations' output, reproduced from the same source.
func (f *atFixture) model(t *testing.T, svc, rel, src string) {
	t.Helper()
	abs := f.write(t, svc, rel, src)
	srcBytes, root, release, ok := rubyParse(abs)
	if !ok {
		t.Fatalf("parse %s", abs)
	}
	defer release()

	classID := func(name string) string { return svc + ":class:" + name }
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "class" {
			nameNode := n.ChildByFieldName("name")
			if nameNode != nil {
				name := nameNode.Content(srcBytes)
				f.nodes = append(f.nodes, graph.Node{
					ID:      classID(name),
					Type:    graph.NodeTypeClass,
					Label:   name,
					Service: svc,
					File:    abs,
					Line:    int(n.StartPoint().Row) + 1,
				})
				// Only the edge, never a node for the superclass: an
				// out-of-repo base (ActiveRecord::Base, Struct) has no class
				// node in the real graph either, and LinkRubyTypeRelations
				// only ever points `inherits` at declarations it found.
				if sup := n.ChildByFieldName("superclass"); sup != nil && sup.NamedChildCount() > 0 {
					superName := sup.NamedChild(0).Content(srcBytes)
					f.edges = append(f.edges, graph.Edge{
						ID:   "inherits:" + classID(name) + "->" + classID(superName),
						From: classID(name), To: classID(superName), Type: graph.EdgeTypeInherits,
					})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

func (f *atFixture) run() ([]graph.Edge, []graph.UnresolvedRef) {
	return LinkRailsModelTables(f.nodes, f.edges, f.files)
}

// backedBy renders the emitted edges as "Class -> table" strings, sorted, so
// a test asserts the whole set at once rather than probing for members.
func backedBy(t *testing.T, f *atFixture, edges []graph.Edge) []string {
	t.Helper()
	label := map[string]string{}
	for _, n := range f.nodes {
		label[n.ID] = n.Label
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		if e.Type != graph.EdgeTypeBackedBy {
			t.Errorf("unexpected edge type %s", e.Type)
		}
		out = append(out, label[e.From]+" -> "+label[e.To])
	}
	sort.Strings(out)
	return out
}

func ledgerLines(ledger []graph.UnresolvedRef) []string {
	out := make([]string, 0, len(ledger))
	for _, l := range ledger {
		out = append(out, fmt.Sprintf("%s %s:%d %s", l.Kind, filepath.Base(l.File), l.Line, l.Name))
	}
	sort.Strings(out)
	return out
}

const atSchema = `ActiveRecord::Schema[7.1].define(version: 2026_01_01_000000) do
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
`

// atBase writes the application_record.rb every fixture needs — the abstract
// root that terminates the inherits walk and owns no table of its own.
func atBase(t *testing.T, f *atFixture, svc string) {
	t.Helper()
	f.model(t, svc, "app/models/application_record.rb", `class ApplicationRecord < ActiveRecord::Base
  self.abstract_class = true
end
`)
}

// TestLinkRailsModelTables_WorkedExample is the plan's worked example,
// asserted edge-for-edge and ledger-row-for-row.
func TestLinkRailsModelTables_WorkedExample(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/widget.rb", "class Widget < ApplicationRecord\nend\n")
	f.model(t, "orion", "app/models/thing.rb", `class Thing < ApplicationRecord
  self.table_name = "legacy_things"
end
`)
	f.model(t, "orion", "app/models/person.rb", "class Person < ApplicationRecord\nend\n")

	edges, ledger := f.run()
	want := []string{"Thing -> legacy_things", "Widget -> widgets"}
	if got := backedBy(t, f, edges); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("edges:\n got: %v\nwant: %v", got, want)
	}
	wantLedger := []string{
		"rails_model_table_unresolved person.rb:1 Person",
		"rails_table_unowned schema.rb:6 gadgets",
	}
	if got := ledgerLines(ledger); strings.Join(got, "|") != strings.Join(wantLedger, "|") {
		t.Errorf("ledger:\n got: %v\nwant: %v", got, wantLedger)
	}

	// ApplicationRecord is abstract: no edge AND no ledger row. Being
	// correctly table-less is not a resolution failure.
	for _, l := range ledger {
		if l.Name == "ApplicationRecord" {
			t.Errorf("abstract class must not ledger: %+v", l)
		}
	}
	// The convention edge records how far the class sat from the root.
	for _, e := range edges {
		if strings.HasSuffix(e.From, ":Widget") {
			if e.Meta["via"] != "convention" || e.Meta["inherit_hops"] != "1" {
				t.Errorf("Widget edge meta = %v", e.Meta)
			}
		}
		if strings.HasSuffix(e.From, ":Thing") && e.Meta["via"] != "table_name" {
			t.Errorf("Thing edge meta = %v", e.Meta)
		}
	}
}

// TestLinkRailsModelTables_SymbolTableName — `self.table_name = :legacy_things`
// is the same declaration as the string form.
func TestLinkRailsModelTables_SymbolTableName(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/thing.rb", `class Thing < ApplicationRecord
  self.table_name = :legacy_things
end
`)
	edges, _ := f.run()
	if got := backedBy(t, f, edges); len(got) != 1 || got[0] != "Thing -> legacy_things" {
		t.Errorf("edges = %v", got)
	}
}

// TestLinkRailsModelTables_ChainDepths exercises the closure walk: a model two
// intermediate classes below ApplicationRecord still resolves, and one beyond
// the hop bound ledgers rather than silently dropping.
func TestLinkRailsModelTables_ChainDepths(t *testing.T) {
	for _, depth := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			f := newATFixture(t)
			f.schema(t, "orion", atSchema)
			atBase(t, f, "orion")
			super := "ApplicationRecord"
			for i := 0; i < depth-1; i++ {
				mid := fmt.Sprintf("Mid%d", i)
				f.model(t, "orion", fmt.Sprintf("app/models/mid%d.rb", i),
					fmt.Sprintf("class %s < %s\n  self.abstract_class = true\nend\n", mid, super))
				super = mid
			}
			f.model(t, "orion", "app/models/widget.rb",
				fmt.Sprintf("class Widget < %s\nend\n", super))

			edges, ledger := f.run()
			got := backedBy(t, f, edges)
			if depth <= maxModelInheritHops {
				if len(got) != 1 || got[0] != "Widget -> widgets" {
					t.Fatalf("depth %d: edges = %v", depth, got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("depth %d: past the hop bound, want no edge, got %v", depth, got)
			}
			found := false
			for _, l := range ledger {
				if l.Kind == ledgerModelTableUnresolved && l.Name == "Widget" {
					found = true
				}
			}
			if !found {
				t.Errorf("depth %d: a chain past the bound must ledger, not vanish: %v", depth, ledgerLines(ledger))
			}
		})
	}
}

// TestLinkRailsModelTables_PlainObjectIsSilent — cedar has many PORO service
// objects under app/models/. They are not models, so they mint no edge; they
// are also not misses, so they mint no ledger row either.
func TestLinkRailsModelTables_PlainObjectIsSilent(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/widget_presenter.rb", "class WidgetPresenter\nend\n")
	f.model(t, "orion", "app/models/widget_policy.rb", "class WidgetPolicy < Struct\nend\n")

	edges, ledger := f.run()
	if got := backedBy(t, f, edges); len(got) != 0 {
		t.Errorf("edges = %v", got)
	}
	for _, l := range ledger {
		if l.Kind == ledgerModelTableUnresolved {
			t.Errorf("a PORO must not ledger: %+v", l)
		}
	}
}

// TestLinkRailsModelTables_NeverCrossesServices — the same model name in two
// services resolves against its own service's schema, never the other's.
func TestLinkRailsModelTables_NeverCrossesServices(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	f.schema(t, "willow", `ActiveRecord::Schema[7.1].define(version: 1) do
  create_table "widgets", force: :cascade do |t|
    t.string "name"
  end
end
`)
	atBase(t, f, "orion")
	atBase(t, f, "willow")
	f.model(t, "orion", "app/models/widget.rb", "class Widget < ApplicationRecord\nend\n")
	f.model(t, "willow", "app/models/widget.rb", "class Widget < ApplicationRecord\nend\n")

	edges, _ := f.run()
	if len(edges) != 2 {
		t.Fatalf("want one edge per service, got %d", len(edges))
	}
	byID := map[string]graph.Node{}
	for _, n := range f.nodes {
		byID[n.ID] = n
	}
	for _, e := range edges {
		from, to := byID[e.From], byID[e.To]
		if from.Service != to.Service {
			t.Errorf("cross-service edge: %s(%s) -> %s(%s)", from.Label, from.Service, to.Label, to.Service)
		}
	}
}

func TestRailsUnderscoreAndCandidates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		class string
		want  []string
	}{
		{"Widget", []string{"widgets"}},
		{"UserCategory", []string{"user_categories"}},
		{"Status", []string{"statuses"}},
		{"APIKey", []string{"api_keys"}},
		{"Admin::Report", []string{"reports"}},
		{"Person", []string{"persons"}}, // the irregular plural that ledgers
		{"Shelf", []string{"shelves", "shelfs"}},
	} {
		got := railsTableCandidates(tc.class)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.class, got, tc.want)
		}
	}
}

// TestLinkRailsModelTables_NoRailsSchemaIsANoOp — a workspace whose only
// tables come from .sql files (or from LinkTables' synthetic mints) must be
// left exactly as it was. AT may not reinterpret another tier's nodes.
func TestLinkRailsModelTables_NoRailsSchemaIsANoOp(t *testing.T) {
	f := newATFixture(t)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/widget.rb", "class Widget < ApplicationRecord\nend\n")
	f.nodes = append(f.nodes, graph.Node{
		ID: "orion:table:widgets", Type: graph.NodeTypeTable, Label: "widgets",
		Service: "orion", Meta: map[string]string{"synthetic": "true"},
	})
	edges, ledger := f.run()
	if len(edges) != 0 || len(ledger) != 0 {
		t.Errorf("edges=%v ledger=%v", edges, ledgerLines(ledger))
	}
}

// TestLinkRailsModelTables_SingleTableInheritance — Rails STI: `class Issue <
// Comment` has no `issues` table and is not meant to; its rows live in
// `comments`. Resolving it needs the answer for another class first, which is
// why linkService runs in two rounds.
func TestLinkRailsModelTables_SingleTableInheritance(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/gadget.rb", "class Gadget < ApplicationRecord\nend\n")
	f.model(t, "orion", "app/models/premium_gadget.rb", "class PremiumGadget < Gadget\nend\n")
	// Two levels of STI: the walk must not stop at the first ancestor that
	// happens to have no table of its own.
	f.model(t, "orion", "app/models/gold_gadget.rb", "class GoldGadget < PremiumGadget\nend\n")

	edges, ledger := f.run()
	want := []string{"GoldGadget -> gadgets", "Gadget -> gadgets", "PremiumGadget -> gadgets"}
	sort.Strings(want)
	if got := backedBy(t, f, edges); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("edges:\n got: %v\nwant: %v", got, want)
	}
	for _, e := range edges {
		if strings.HasSuffix(e.From, ":PremiumGadget") && e.Meta["via"] != "sti" {
			t.Errorf("PremiumGadget edge meta = %v, want via=sti", e.Meta)
		}
	}
	for _, l := range ledger {
		if l.Kind == ledgerModelTableUnresolved {
			t.Errorf("an STI subclass is not a miss: %+v", l)
		}
	}
}

// TestLinkRailsModelTables_ModuleIsSilent — the Ruby parser types `module X`
// as a class node, and app/models/concerns is full of them. A module owns no
// table, and saying so is not a resolution failure.
func TestLinkRailsModelTables_ModuleIsSilent(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/concerns/widgetable.rb", `module Widgetable
  extend ActiveSupport::Concern
end
`)
	edges, ledger := f.run()
	if got := backedBy(t, f, edges); len(got) != 0 {
		t.Errorf("edges = %v", got)
	}
	for _, l := range ledger {
		if l.Kind == ledgerModelTableUnresolved {
			t.Errorf("a module must not ledger: %+v", l)
		}
	}
}

// TestLinkRailsModelTables_MixinIsNotInheritance — `include Loggable` gives a
// class an `inherits` edge with meta via=mixin. Cedar has 1595 of those
// against 670 real superclass edges, and treating one as inheritance makes
// every PORO that includes a concern look like an unresolved model.
func TestLinkRailsModelTables_MixinIsNotInheritance(t *testing.T) {
	f := newATFixture(t)
	f.schema(t, "orion", atSchema)
	atBase(t, f, "orion")
	f.model(t, "orion", "app/models/widget_copier.rb", "class WidgetCopier\nend\n")
	// A mixin edge from the PORO straight to ApplicationRecord: the shape the
	// walk must refuse to follow. (Nothing real includes ApplicationRecord;
	// this is the strongest possible version of the wrong edge.)
	f.edges = append(f.edges, graph.Edge{
		ID:   "inherits:orion:class:WidgetCopier->orion:class:ApplicationRecord",
		From: "orion:class:WidgetCopier", To: "orion:class:ApplicationRecord",
		Type: graph.EdgeTypeInherits, Meta: map[string]string{"via": "mixin"},
	})
	edges, ledger := f.run()
	if got := backedBy(t, f, edges); len(got) != 0 {
		t.Errorf("edges = %v", got)
	}
	for _, l := range ledger {
		if l.Kind == ledgerModelTableUnresolved {
			t.Errorf("a mixin does not make a PORO a model: %+v", l)
		}
	}
}
