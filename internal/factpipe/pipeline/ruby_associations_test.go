package pipeline_test

// FX.8.25: ActiveRecord associations, migrated off
// internal/linker/ruby_associations.go onto patterns/ruby/ruby_associations.yaml
// + rules/ruby/ruby_associations.dl. Real parses (not hand-built nodes),
// mirroring internal/contract's FX.8.14 test convention.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// assocFixtureNodes parses each (relFile, source) pair as one Ruby service
// file and returns every node the real parser produced (class nodes with
// their real ID/Label/Line/EndLine — the ruby_associations.dl span-
// containment join depends on the real EndLine the parser computes, not a
// hand-built one).
func assocFixtureNodes(t *testing.T, dir string, files map[string]string) ([]graph.Node, []pipeline.ParsedFile) {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	if err != nil {
		t.Fatalf("EmbeddedRegistry: %v", err)
	}
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	var parsed []pipeline.ParsedFile
	for rel, src := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		p := parser.ForFile(abs)
		if p == nil {
			t.Fatalf("no parser for %s", abs)
		}
		ns, _, _, err := p.Parse(abs, "svc", m, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", abs, err)
		}
		nodes = append(nodes, ns...)
		parsed = append(parsed, pipeline.ParsedFile{Path: abs, Language: "ruby", Src: []byte(src)})
	}
	return nodes, parsed
}

func assocActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg.Active([]deps.Dependency{{Name: "activerecord", Version: "7.1.0"}})
}

func assocEdgesFrom(t *testing.T, dir string, files map[string]string) (nodes []graph.Node, res pipeline.Result) {
	t.Helper()
	nodes, parsed := assocFixtureNodes(t, dir, files)
	snap := graph.Snapshot{Nodes: nodes}
	res, err := pipeline.Run(assocActive(t), parsed, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return nodes, res
}

func classNode(t *testing.T, nodes []graph.Node, label string) *graph.Node {
	t.Helper()
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeClass && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	t.Fatalf("no class node %q among %d nodes", label, len(nodes))
	return nil
}

// TestRubyAssociationsRule_HasManySingularizeClassify covers the concrete gap
// this pass exists for: Category#has_many :deliverables, Deliverable in a
// different file, resolved by singularize+classify.
func TestRubyAssociationsRule_HasManySingularizeClassify(t *testing.T) {
	dir := t.TempDir()
	nodes, res := assocEdgesFrom(t, dir, map[string]string{
		"category.rb":    "class Category < ApplicationRecord\n  has_many :deliverables\nend\n",
		"deliverable.rb": "class Deliverable\nend\n",
	})
	category := classNode(t, nodes, "Category")
	deliverable := classNode(t, nodes, "Deliverable")

	var got *graph.Edge
	for i := range res.Edges {
		if res.Edges[i].From == category.ID && res.Edges[i].To == deliverable.ID {
			got = &res.Edges[i]
		}
	}
	if got == nil {
		t.Fatalf("missing calls edge Category -> Deliverable; edges: %+v", res.Edges)
	}
	if got.Type != graph.EdgeTypeCalls {
		t.Errorf("want EdgeTypeCalls, got %q", got.Type)
	}
	if got.Confidence != graph.ConfidenceInferred {
		t.Errorf("unambiguous resolution should be inferred, got %q", got.Confidence)
	}
	if got.Meta["via"] != "association" || got.Meta["granularity"] != "class" {
		t.Errorf("unexpected meta: %+v", got.Meta)
	}
}

// TestRubyAssociationsRule_ClassNameOverrideWins covers has_one with an
// explicit class_name: override, which must win over the derived name.
func TestRubyAssociationsRule_ClassNameOverrideWins(t *testing.T) {
	dir := t.TempDir()
	nodes, res := assocEdgesFrom(t, dir, map[string]string{
		"deliverable.rb": "class Deliverable\n  belongs_to :study\n  has_one :owner, class_name: \"User\"\nend\n",
		"study.rb":       "class Study\nend\n",
		"user.rb":        "class User\nend\n",
	})
	deliverable := classNode(t, nodes, "Deliverable")
	study := classNode(t, nodes, "Study")
	user := classNode(t, nodes, "User")

	want := map[string]bool{study.ID: false, user.ID: false}
	for _, e := range res.Edges {
		if e.From != deliverable.ID {
			continue
		}
		if _, ok := want[e.To]; ok {
			want[e.To] = true
		}
		if e.To != study.ID && e.To != user.ID {
			t.Errorf("unexpected association target: %+v", e)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing expected edge Deliverable -> %q; edges: %+v", id, res.Edges)
		}
	}
}

// TestRubyAssociationsRule_UnknownTargetProducesNoEdgeOrLedger covers a
// belongs_to whose target this service does not declare (an engine/gem
// model) — no edge, no ledger noise.
func TestRubyAssociationsRule_UnknownTargetProducesNoEdgeOrLedger(t *testing.T) {
	dir := t.TempDir()
	nodes, res := assocEdgesFrom(t, dir, map[string]string{
		"deliverable.rb": "class Deliverable\n  belongs_to :organization\nend\n",
	})
	deliverable := classNode(t, nodes, "Deliverable")
	for _, e := range res.Edges {
		if e.From == deliverable.ID {
			t.Errorf("unresolvable target must not emit an edge: %+v", e)
		}
	}
	for _, u := range res.Unresolved {
		if u.Kind == "association_collision" {
			t.Errorf("unresolvable target must not be ledgered by this pass: %+v", u)
		}
	}
}

// TestRubyAssociationsRule_CollisionDemotesAndLedgersOnce covers two
// same-named classes across files in one service: every candidate edge
// demotes to partial, and each owner ledgers exactly one association_collision
// row.
func TestRubyAssociationsRule_CollisionDemotesAndLedgersOnce(t *testing.T) {
	dir := t.TempDir()
	nodes, res := assocEdgesFrom(t, dir, map[string]string{
		"category.rb": "class Category\n  has_many :widgets\nend\n",
		"study.rb":    "class Study\n  has_many :widgets\nend\n",
		"a_widget.rb": "class Widget\nend\n",
		"b_widget.rb": "class Widget\nend\n",
	})
	_ = nodes

	partial := 0
	for _, e := range res.Edges {
		if e.Confidence == graph.ConfidencePartial {
			partial++
		}
	}
	if partial != 4 {
		t.Errorf("expected 4 partial-confidence edges (2 owners x 2 Widget matches), got %d: %+v", partial, res.Edges)
	}

	collisions := 0
	for _, u := range res.Unresolved {
		if u.Kind == "association_collision" {
			collisions++
		}
	}
	if collisions != 2 {
		t.Errorf("expected one deduped collision ledger row per owner, got %d: %+v", collisions, res.Unresolved)
	}
}
