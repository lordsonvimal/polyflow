package factpipe

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func fixtureSnapshot() graph.Snapshot {
	return graph.Snapshot{
		Nodes: []graph.Node{
			{ID: "svc\x00ctrl.rb:1", Type: graph.NodeTypeClass, Label: "UsersController",
				Service: "web", File: "ctrl.rb", Line: 1, EndLine: 20,
				Meta: map[string]string{"pattern": "rails_controller", "abstract": "false"}},
			{ID: "svc\x00ctrl.rb:5", Type: graph.NodeTypeMethod, Label: "index",
				Service: "web", File: "ctrl.rb", Line: 5, EndLine: 8},
			{ID: "svc\x00base.rb:1", Type: graph.NodeTypeClass, Label: "ApplicationController",
				Service: "web", File: "base.rb", Line: 1},
			{ID: "svc\x00base.rb:3", Type: graph.NodeTypeMethod, Label: "authenticate",
				Service: "web", File: "base.rb", Line: 3, EndLine: 4},
		},
		Edges: []graph.Edge{
			{ID: "e1", From: "svc\x00ctrl.rb:1", To: "svc\x00ctrl.rb:5", Type: graph.EdgeTypeContains},
			{ID: "e2", From: "svc\x00base.rb:1", To: "svc\x00base.rb:3", Type: graph.EdgeTypeContains},
			{ID: "e3", From: "svc\x00ctrl.rb:1", To: "svc\x00base.rb:1", Type: graph.EdgeTypeInherits},
			{ID: "e4", From: "svc\x00ctrl.rb:5", To: "svc\x00base.rb:3", Type: graph.EdgeTypeCalls, Label: "authenticate"},
			{ID: "e5", From: "svc\x00ctrl.rb:5", To: "svc\x00base.rb:3", Type: graph.EdgeTypeCalls, Label: "authenticate"},
			// contains from a non-class parent must NOT produce defines
			{ID: "e6", From: "svc\x00ctrl.rb:5", To: "svc\x00base.rb:3", Type: graph.EdgeTypeContains},
		},
		Imports: []graph.SnapshotImport{{File: "ctrl.rb", Package: "devise"}},
		Resolved: []graph.ResolvedName{
			{Site: "svc\x00ctrl.rb:5", Name: "authenticate", Target: "svc\x00base.rb:3", Depth: 1},
			{Site: "svc\x00ctrl.rb:5", Name: "missing", Target: "", Depth: 0},
		},
	}
}

func byPred(fs FactSet, pred string) []Fact {
	var out []Fact
	for _, f := range fs.All() {
		if f.Pred == pred {
			out = append(out, f)
		}
	}
	return out
}

func TestGraphFactsCounts(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)

	want := map[string]int{
		"node":           4,
		"node_label":     4, // every node has a Label
		"node_line":      3, // only nodes with EndLine > 0
		"node_meta":      2, // both keys of the one node with Meta
		"edge":           6,
		"calls_edge":     2,
		"calls_edge_any": 1, // (from,label) deduped
		"calls_edge_seq": 2, // one per calls edge, with its ordinal
		"inherits":       1,
		"defines":        2, // e1, e2 — NOT e6 (method parent)
		"import":         1,
		"resolved":       2,
	}
	for pred, n := range want {
		if got := len(byPred(fs, pred)); got != n {
			t.Errorf("%s: %d facts, want %d", pred, got, n)
		}
	}
}

func TestGraphFactsNodeLineIntAtoms(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	nl := byPred(fs, "node_line")[0]
	if nl.Args[1].Kind != AtomInt || nl.Args[2].Kind != AtomInt {
		t.Fatalf("node_line line/endline kinds = %d/%d, want AtomInt", nl.Args[1].Kind, nl.Args[2].Kind)
	}
	if nl.Args[1].Int != 1 || nl.Args[2].Int != 20 {
		t.Errorf("node_line = (%d,%d), want (1,20)", nl.Args[1].Int, nl.Args[2].Int)
	}
	if nl.Args[0].Kind != AtomNode {
		t.Errorf("node_line id kind = %d, want AtomNode", nl.Args[0].Kind)
	}
}

func TestGraphFactsNodeMetaSorted(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	m := byPred(fs, "node_meta")
	if m[0].Args[1].Str != "abstract" || m[1].Args[1].Str != "pattern" {
		t.Errorf("node_meta keys = [%s %s], want sorted [abstract pattern]",
			m[0].Args[1].Str, m[1].Args[1].Str)
	}
}

func TestGraphFactsResolvedDepth(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	r := byPred(fs, "resolved")
	if r[0].Args[3].Kind != AtomInt || r[0].Args[3].Int != 1 {
		t.Errorf("resolved[0] depth = %v, want int 1", r[0].Args[3])
	}
	if r[1].Args[2].Kind != AtomNode || r[1].Args[2].Node != "" {
		t.Errorf("resolved[1] target = %v, want empty node", r[1].Args[2])
	}
}

func TestGraphFactsDefinesOnlyFromClass(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	for _, f := range byPred(fs, "defines") {
		if f.Args[0].Node == "svc\x00ctrl.rb:5" {
			t.Errorf("defines emitted for a method parent: %+v", f.Args)
		}
	}
	d := byPred(fs, "defines")[0]
	if d.Args[1].Str != "index" || d.Args[2].Node != "svc\x00ctrl.rb:5" {
		t.Errorf("defines[0] = (%s, %s), want (index, svc\x00ctrl.rb:5)", d.Args[1].Str, d.Args[2].Node)
	}
}

func TestGraphFactsDeterministic(t *testing.T) {
	a, b := NewFactSet(), NewFactSet()
	GraphFacts(fixtureSnapshot(), a)
	GraphFacts(fixtureSnapshot(), b)
	fa, fb := a.All(), b.All()
	if len(fa) != len(fb) {
		t.Fatalf("fact counts differ: %d vs %d", len(fa), len(fb))
	}
	for i := range fa {
		if fa[i].Pred != fb[i].Pred || len(fa[i].Args) != len(fb[i].Args) {
			t.Fatalf("fact %d differs: %+v vs %+v", i, fa[i], fb[i])
		}
		for j := range fa[i].Args {
			if fa[i].Args[j] != fb[i].Args[j] {
				t.Fatalf("fact %d arg %d differs: %+v vs %+v", i, j, fa[i].Args[j], fb[i].Args[j])
			}
		}
	}
}

func TestGraphFactsFileRank(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	fr := byPred(fs, "file_rank")
	// two distinct files: base.rb, ctrl.rb — lexical order.
	if len(fr) != 2 {
		t.Fatalf("file_rank: %d facts, want 2", len(fr))
	}
	if fr[0].Args[0].Str != "base.rb" || fr[0].Args[1].Kind != AtomInt || fr[0].Args[1].Int != 0 {
		t.Errorf("file_rank[0] = %+v, want (base.rb, 0)", fr[0].Args)
	}
	if fr[1].Args[0].Str != "ctrl.rb" || fr[1].Args[1].Int != 1 {
		t.Errorf("file_rank[1] = %+v, want (ctrl.rb, 1)", fr[1].Args)
	}
}

// ancestrySnapshot: Grandchild -> Child -> Parent superclass chain plus a mixin
// Mixed included straight into Grandchild, to exercise depth and level ties.
func ancestrySnapshot() graph.Snapshot {
	cls := func(id, label, file string) graph.Node {
		return graph.Node{ID: id, Type: graph.NodeTypeClass, Label: label, Service: "web", File: file, Line: 1}
	}
	return graph.Snapshot{
		Nodes: []graph.Node{
			cls("g", "Grandchild", "g.rb"),
			cls("c", "Child", "c.rb"),
			cls("p", "Parent", "p.rb"),
			cls("m", "Mixed", "m.rb"),
		},
		Edges: []graph.Edge{
			{ID: "e1", From: "g", To: "c", Type: graph.EdgeTypeInherits},
			{ID: "e2", From: "g", To: "m", Type: graph.EdgeTypeInherits}, // mixin, same level as c
			{ID: "e3", From: "c", To: "p", Type: graph.EdgeTypeInherits},
		},
	}
}

func TestGraphFactsAncestorDist(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(ancestrySnapshot(), fs)

	type ad struct {
		sub, anc string
		depth    int64
	}
	var got []ad
	for _, f := range byPred(fs, "ancestor_dist") {
		if f.Args[2].Kind != AtomInt {
			t.Fatalf("ancestor_dist depth not int: %+v", f.Args)
		}
		got = append(got, ad{f.Args[0].Node, f.Args[1].Node, f.Args[2].Int})
	}
	want := []ad{
		{"g", "c", 1}, {"g", "m", 1}, {"g", "p", 2}, // g's frontier order: c then m, then p
		{"c", "p", 1},
	}
	if len(got) != len(want) {
		t.Fatalf("ancestor_dist = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ancestor_dist[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGraphFactsOriginPattern(t *testing.T) {
	fs := NewFactSet()
	GraphFacts(fixtureSnapshot(), fs)
	for _, f := range fs.All() {
		if f.Origin.Kind != OriginGraph {
			t.Errorf("%s: Origin.Kind = %d, want OriginGraph", f.Pred, f.Origin.Kind)
		}
		if f.Origin.Pattern != f.Pred {
			t.Errorf("%s: Origin.Pattern = %q, want relation name", f.Pred, f.Origin.Pattern)
		}
	}
}
