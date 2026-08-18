package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkRubyAssociations_HasMany covers the concrete gap this pass exists
// for: Category#has_many :deliverables, where Deliverable lives in a
// different file. Confirms the naive pluralize-then-classify path.
func TestLinkRubyAssociations_HasMany(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	category := writeRuby(t, dir, "category.rb", `
class Category < ApplicationRecord
  has_many :deliverables
end
`)
	deliverable := writeRuby(t, dir, "deliverable.rb", "class Deliverable\nend\n")

	categoryClass := rubyClassNodeAt("svc", category, "Category", 2)
	deliverableClass := rubyClassNode("svc", deliverable, "Deliverable", 1)
	nodes := []graph.Node{categoryClass, deliverableClass}
	serviceFiles := map[string][]string{"svc": {category, deliverable}}

	edges, unresolved := LinkRubyAssociations(nodes, serviceFiles)

	var got *graph.Edge
	for i := range edges {
		if edges[i].From == categoryClass.ID && edges[i].To == deliverableClass.ID {
			got = &edges[i]
		}
	}
	if got == nil {
		t.Fatalf("missing calls edge Category -> Deliverable; edges: %+v", edges)
	}
	if got.Type != graph.EdgeTypeCalls {
		t.Errorf("association edge must be EdgeTypeCalls, got %q", got.Type)
	}
	if got.Confidence != graph.ConfidenceInferred {
		t.Errorf("unambiguous resolution should be inferred, got %q", got.Confidence)
	}
	if len(unresolved) != 0 {
		t.Errorf("an unambiguous resolution must not be ledgered: %+v", unresolved)
	}
}

// TestLinkRubyAssociations_BelongsToAndHasOneWithClassNameOverride covers
// belongs_to (already-singular symbol) and has_one with an explicit
// class_name: override, which must win over the derived name.
func TestLinkRubyAssociations_BelongsToAndHasOneWithClassNameOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	deliverable := writeRuby(t, dir, "deliverable.rb", `
class Deliverable
  belongs_to :study
  has_one :owner, class_name: "User"
end
`)
	study := writeRuby(t, dir, "study.rb", "class Study\nend\n")
	user := writeRuby(t, dir, "user.rb", "class User\nend\n")

	deliverableClass := rubyClassNodeAt("svc", deliverable, "Deliverable", 2)
	studyClass := rubyClassNode("svc", study, "Study", 1)
	userClass := rubyClassNode("svc", user, "User", 1)
	nodes := []graph.Node{deliverableClass, studyClass, userClass}
	serviceFiles := map[string][]string{"svc": {deliverable, study, user}}

	edges, _ := LinkRubyAssociations(nodes, serviceFiles)

	wantTargets := map[string]bool{studyClass.ID: false, userClass.ID: false}
	for _, e := range edges {
		if e.From != deliverableClass.ID {
			continue
		}
		if _, ok := wantTargets[e.To]; ok {
			wantTargets[e.To] = true
		}
	}
	for id, found := range wantTargets {
		if !found {
			t.Errorf("missing expected edge Deliverable -> %q; edges: %+v", id, edges)
		}
	}

	// Owner must resolve to User (the class_name: override), never a
	// nonexistent Owner class.
	for _, e := range edges {
		if e.From == deliverableClass.ID && e.To != studyClass.ID && e.To != userClass.ID {
			t.Errorf("unexpected association target: %+v", e)
		}
	}
}

// TestLinkRubyAssociations_UnknownTargetNotLedgered covers a
// belongs_to/has_many whose target class this service does not declare
// (a Rails engine/gem model). No edge, no ledger noise.
func TestLinkRubyAssociations_UnknownTargetNotLedgered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	model := writeRuby(t, dir, "deliverable.rb", `
class Deliverable
  belongs_to :organization
end
`)

	nodes := []graph.Node{rubyClassNode("svc", model, "Deliverable", 2)}
	serviceFiles := map[string][]string{"svc": {model}}

	edges, unresolved := LinkRubyAssociations(nodes, serviceFiles)
	if len(edges) != 0 {
		t.Errorf("unresolvable target must not emit an edge: %+v", edges)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolvable target must not be ledgered by this pass: %+v", unresolved)
	}
}

// TestLinkRubyAssociations_DynamicAndPolymorphicShapes covers the documented
// behavior for shapes this tier deliberately does not special-case: a
// dynamic/`as:` polymorphic argument produces the naive-target edge (not a
// suppressed or crashing case), and a non-symbol first argument produces no
// edge at all rather than a panic.
func TestLinkRubyAssociations_DynamicAndPolymorphicShapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	model := writeRuby(t, dir, "comment.rb", `
class Comment
  has_many :things, as: :owner
  belongs_to :something, through: :x
end
`)
	thing := writeRuby(t, dir, "thing.rb", "class Thing\nend\n")
	something := writeRuby(t, dir, "something.rb", "class Something\nend\n")

	commentClass := rubyClassNodeAt("svc", model, "Comment", 2)
	thingClass := rubyClassNode("svc", thing, "Thing", 1)
	somethingClass := rubyClassNode("svc", something, "Something", 1)
	nodes := []graph.Node{commentClass, thingClass, somethingClass}
	serviceFiles := map[string][]string{"svc": {model, thing, something}}

	edges, _ := LinkRubyAssociations(nodes, serviceFiles)

	foundThing, foundSomething := false, false
	for _, e := range edges {
		if e.From != commentClass.ID {
			continue
		}
		if e.To == thingClass.ID {
			foundThing = true
		}
		if e.To == somethingClass.ID {
			foundSomething = true
		}
	}
	if !foundThing {
		t.Errorf("as: polymorphic option must not suppress the naive-target edge; edges: %+v", edges)
	}
	if !foundSomething {
		t.Errorf("through: option must not suppress the naive-target edge; edges: %+v", edges)
	}
}

// TestLinkRubyAssociations_NoHasAndBelongsToMany confirms
// has_and_belongs_to_many is out of scope (Non-goals) and produces no edge.
func TestLinkRubyAssociations_NoHasAndBelongsToMany(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	model := writeRuby(t, dir, "post.rb", `
class Post
  has_and_belongs_to_many :tags
end
`)
	tag := writeRuby(t, dir, "tag.rb", "class Tag\nend\n")

	postClass := rubyClassNodeAt("svc", model, "Post", 2)
	tagClass := rubyClassNode("svc", tag, "Tag", 1)
	nodes := []graph.Node{postClass, tagClass}
	serviceFiles := map[string][]string{"svc": {model, tag}}

	edges, _ := LinkRubyAssociations(nodes, serviceFiles)
	for _, e := range edges {
		if e.From == postClass.ID {
			t.Errorf("has_and_belongs_to_many is out of scope; unexpected edge: %+v", e)
		}
	}
}

// TestLinkRubyAssociations_CollisionLedgersOnce covers two same-named
// classes across files in one service: multiple associations referencing
// that name must produce exactly one deduped UnresolvedRef, not one per
// occurrence.
func TestLinkRubyAssociations_CollisionLedgersOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	owner1 := writeRuby(t, dir, "category.rb", `
class Category
  has_many :widgets
end
`)
	owner2 := writeRuby(t, dir, "study.rb", `
class Study
  has_many :widgets
end
`)
	widgetA := writeRuby(t, dir, "a_widget.rb", "class Widget\nend\n")
	widgetB := writeRuby(t, dir, "b_widget.rb", "class Widget\nend\n")

	nodes := []graph.Node{
		rubyClassNodeAt("svc", owner1, "Category", 2),
		rubyClassNodeAt("svc", owner2, "Study", 2),
		rubyClassNode("svc", widgetA, "Widget", 1),
		rubyClassNode("svc", widgetB, "Widget", 1),
	}
	serviceFiles := map[string][]string{"svc": {owner1, owner2, widgetA, widgetB}}

	edges, unresolved := LinkRubyAssociations(nodes, serviceFiles)

	partialCount := 0
	for _, e := range edges {
		if e.Confidence == graph.ConfidencePartial {
			partialCount++
		}
	}
	if partialCount != 4 {
		t.Errorf("expected 4 partial-confidence edges (2 owners x 2 Widget matches), got %d: %+v", partialCount, edges)
	}

	collisions := 0
	for _, u := range unresolved {
		if u.Kind == "association_collision" {
			collisions++
		}
	}
	if collisions != 2 {
		t.Errorf("expected one deduped collision ledger entry per owner->target pair, got %d: %+v", collisions, unresolved)
	}
}

// TestLinkRubyAssociations_NoCrossServiceBinding mirrors the Tier K.7a guard:
// Ruby constant lookup is process-local, so an association in service A must
// never resolve to a class in service B.
func TestLinkRubyAssociations_NoCrossServiceBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	category := writeRuby(t, dir, "category.rb", `
class Category
  has_many :deliverables
end
`)
	aDeliverable := writeRuby(t, dir, "a_deliverable.rb", "class Deliverable\nend\n")
	bDeliverable := writeRuby(t, dir, "b_deliverable.rb", "class Deliverable\nend\n")

	categoryClass := rubyClassNodeAt("svc-a", category, "Category", 2)
	aDeliverableClass := rubyClassNode("svc-a", aDeliverable, "Deliverable", 1)
	nodes := []graph.Node{
		categoryClass,
		aDeliverableClass,
		rubyClassNode("svc-b", bDeliverable, "Deliverable", 1),
	}
	serviceFiles := map[string][]string{
		"svc-a": {category, aDeliverable},
		"svc-b": {bDeliverable},
	}

	edges, _ := LinkRubyAssociations(nodes, serviceFiles)
	if len(edges) != 1 {
		t.Fatalf("expected exactly one edge (bound to svc-a's own Deliverable), got %d: %+v", len(edges), edges)
	}
	if edges[0].To != aDeliverableClass.ID {
		t.Errorf("edge must bind to svc-a's Deliverable, got To=%q", edges[0].To)
	}
}
