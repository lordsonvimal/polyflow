package pipeline_test

// FX.8.15 (2026-09-15): ruby_job_inherit — Tier FX migration of
// internal/linker/ruby_job_inherit.go's PromoteInheritedJobPerform, replaced
// by patterns/ruby/ruby_job_inherit.yaml + rules/ruby/ruby_job_inherit.dl,
// driven by the "ruby_job_inherit" hub provider
// (internal/factpipe/hub_ruby_job_inherit.go) plus the `replace:`/`delete:`
// emit primitives (internal/factpipe/emit.go) FX.8.15's rescoping added.
//
// This framework is hub-only: no source files are parsed, so these fixtures
// hand-build graph.Node/graph.Edge exactly like the retired pass's own tests
// did (ported near-verbatim) — mirroring rails_route_actions_test.go's
// convention for a hub-only framework with no tree-sitter extraction. The
// retired JobInheritResult.Promoted/Replaced/Dropped/Edges/Ledger map onto
// pipeline.Result.Nodes/Replaced/Deleted/Edges/Unresolved — Ledger becomes
// Unresolved here because job_ledger_row goes through an `unresolved:` block,
// not a fan_out ledger.

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func jiActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("ruby_job_inherit")
	if fw == nil {
		t.Fatal("ruby_job_inherit framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func jiRun(t *testing.T, nodes []graph.Node, edges []graph.Edge) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(jiActive(t), nil, graph.Snapshot{Nodes: nodes, Edges: edges})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

const cjService = "orion"

func cjFile(class string) string { return fmt.Sprintf("app/jobs/%s.rb", class) }

func cjClassID(class string) string {
	return fmt.Sprintf("%s:%s:class:%s:1", cjService, cjFile(class), class)
}

func cjCandidateID(class string) string {
	return fmt.Sprintf("%s:%s:variable:aj_perform_method_candidate:1", cjService, cjFile(class))
}

func cjSubscriberID(class string) string {
	return fmt.Sprintf("%s:%s:subscriber:aj_perform_method:1", cjService, cjFile(class))
}

func cjPerformID(class string) string {
	return fmt.Sprintf("%s:%s:function:perform:2", cjService, cjFile(class))
}

func cjClass(class string) []graph.Node {
	file := cjFile(class)
	return []graph.Node{
		{ID: cjClassID(class), Type: graph.NodeTypeClass, Label: class,
			Service: cjService, File: file, Line: 1, Language: "ruby"},
		{ID: cjPerformID(class), Type: graph.NodeTypeFunction, Label: "perform",
			Service: cjService, File: file, Line: 2, Language: "ruby",
			Meta: map[string]string{"class": class, "qualified_name": class + "#perform"}},
		{ID: cjCandidateID(class), Type: graph.NodeTypeVariable, Label: "aj_perform_method_candidate",
			Service: cjService, File: file, Line: 1, Language: "ruby",
			Meta: map[string]string{"pattern": "aj_perform_method_candidate", "job_class": class}},
	}
}

func cjBareClass(class string) graph.Node {
	return graph.Node{ID: cjClassID(class), Type: graph.NodeTypeClass, Label: class,
		Service: cjService, File: cjFile(class), Line: 1, Language: "ruby"}
}

func cjInherits(sub, super string) graph.Edge {
	return graph.Edge{
		ID:   "inherits:" + cjClassID(sub) + "->" + cjClassID(super),
		From: cjClassID(sub), To: cjClassID(super), Type: graph.EdgeTypeInherits,
	}
}

func cjChain(depth int) ([]graph.Node, []graph.Edge) {
	nodes := []graph.Node{cjBareClass("ApplicationJob")}
	var edges []graph.Edge
	for i := 0; i < depth; i++ {
		nodes = append(nodes, cjClass(fmt.Sprintf("job%d", i))...)
		super := "ApplicationJob"
		if i > 0 {
			super = fmt.Sprintf("job%d", i-1)
		}
		edges = append(edges, cjInherits(fmt.Sprintf("job%d", i), super))
	}
	return nodes, edges
}

func cjPromotedIDs(res pipeline.Result) map[string]bool {
	ids := map[string]bool{}
	for _, n := range res.Nodes {
		ids[n.ID] = true
	}
	return ids
}

// maxJobInheritHops mirrors the retired internal/linker/ruby_job_inherit.go
// constant (the rules/ruby/ruby_job_inherit.dl hop cap, le(D,4)).
const maxJobInheritHops = 4

// TestJobInherit_ChainDepths is the tier's core claim: a project base class
// between the job and ApplicationJob no longer hides the job. Depth 1 is the
// shape the predicated pattern already handles; 2 and 4 are what only an
// ancestor_dist walk can see.
func TestJobInherit_ChainDepths(t *testing.T) {
	for _, depth := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			nodes, edges := cjChain(depth)
			res := jiRun(t, nodes, edges)

			if len(res.Nodes) != depth {
				t.Fatalf("depth %d: promoted %d classes, want %d", depth, len(res.Nodes), depth)
			}
			if len(res.Unresolved) != 0 {
				t.Fatalf("depth %d: unexpected unresolved %+v", depth, res.Unresolved)
			}
			promoted := cjPromotedIDs(res)
			for i := 0; i < depth; i++ {
				class := fmt.Sprintf("job%d", i)
				if !promoted[cjSubscriberID(class)] {
					t.Errorf("depth %d: %s not promoted", depth, class)
				}
				if res.Replaced[cjCandidateID(class)] != cjSubscriberID(class) {
					t.Errorf("depth %d: %s candidate not mapped to its subscriber", depth, class)
				}
			}
			for _, n := range res.Nodes {
				var want string
				for i := 0; i < depth; i++ {
					if n.ID == cjSubscriberID(fmt.Sprintf("job%d", i)) {
						want = fmt.Sprintf("%d", i+1)
					}
				}
				if got := n.Meta["job_inherit_hops"]; got != want {
					t.Errorf("%s: hops %q, want %q", n.ID, got, want)
				}
				if n.Type != graph.NodeTypeSubscriber {
					t.Errorf("%s: type %s, want subscriber", n.ID, n.Type)
				}
				if n.Meta["pattern"] != "aj_perform_method" {
					t.Errorf("%s: pattern %q, want aj_perform_method — the jobs contract joins on it",
						n.ID, n.Meta["pattern"])
				}
			}
		})
	}
}

// TestJobInherit_BeyondHopBound proves the bound is a real stop, and that
// stopping is ledgered rather than silent.
func TestJobInherit_BeyondHopBound(t *testing.T) {
	nodes, edges := cjChain(maxJobInheritHops + 1)
	res := jiRun(t, nodes, edges)

	deepest := fmt.Sprintf("job%d", maxJobInheritHops)
	if cjPromotedIDs(res)[cjSubscriberID(deepest)] {
		t.Fatalf("%s is %d hops from the root and must not promote", deepest, maxJobInheritHops+1)
	}
	if len(res.Nodes) != maxJobInheritHops {
		t.Fatalf("promoted %d, want %d (everything inside the bound)", len(res.Nodes), maxJobInheritHops)
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("unresolved %+v, want exactly one job_base_unresolved", res.Unresolved)
	}
	got := res.Unresolved[0]
	if got.Kind != "job_base_unresolved" || got.Name != deepest || got.File != cjFile(deepest) {
		t.Errorf("unresolved entry %+v, want job_base_unresolved for %s", got, deepest)
	}
}

// TestJobInherit_NoPathToRoot is the over-promotion guard: the tier must not
// turn every PORO with a `perform` method into a job.
func TestJobInherit_NoPathToRoot(t *testing.T) {
	nodes := append(cjClass("Presenter"), cjBareClass("BasePresenter"))
	nodes = append(nodes, cjBareClass("ApplicationJob"))
	edges := []graph.Edge{cjInherits("Presenter", "BasePresenter")}

	res := jiRun(t, nodes, edges)

	if len(res.Nodes) != 0 {
		t.Fatalf("promoted %+v; a class that reaches no ActiveJob root is not a job", res.Nodes)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unresolved %+v; a PORO is a decided no, not an unresolved", res.Unresolved)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != cjCandidateID("Presenter") {
		t.Errorf("deleted %v, want just the Presenter candidate", res.Deleted)
	}
	for _, e := range res.Edges {
		t.Errorf("unexpected edge %s -> %s", e.From, e.To)
	}
}

// TestJobInherit_Diamond covers a class that reaches the root two ways at
// once (a superclass *and* an included module that inherits). The walk must
// settle on one node, not one per path.
func TestJobInherit_Diamond(t *testing.T) {
	nodes, edges := cjChain(1) // job0 < ApplicationJob
	nodes = append(nodes, cjClass("DiamondJob")...)
	nodes = append(nodes, cjBareClass("Retryable"))
	edges = append(edges,
		cjInherits("DiamondJob", "job0"),
		cjInherits("DiamondJob", "Retryable"),
		cjInherits("Retryable", "job0"),
	)

	res := jiRun(t, nodes, edges)

	seen := 0
	for _, n := range res.Nodes {
		if n.ID == cjSubscriberID("DiamondJob") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("DiamondJob promoted %d times, want exactly 1", seen)
	}
}

// TestJobInherit_ReplacesCandidateInPlace pins the node-count invariant the
// mutating-pass rule exists for.
func TestJobInherit_ReplacesCandidateInPlace(t *testing.T) {
	nodes, edges := cjChain(2)
	before := len(nodes)

	res := jiRun(t, nodes, edges)

	promotedByID := map[string]graph.Node{}
	for _, n := range res.Nodes {
		promotedByID[n.ID] = n
	}
	dropped := map[string]bool{}
	for _, id := range res.Deleted {
		dropped[id] = true
	}
	applied := make([]graph.Node, 0, len(nodes))
	for _, n := range nodes {
		if dropped[n.ID] {
			continue
		}
		if newID, ok := res.Replaced[n.ID]; ok {
			applied = append(applied, promotedByID[newID])
			continue
		}
		applied = append(applied, n)
	}

	if len(applied) != before {
		t.Fatalf("node count %d -> %d; promotion must replace, never append", before, len(applied))
	}
	seen := map[string]bool{}
	for _, n := range applied {
		if seen[n.ID] {
			t.Fatalf("duplicate node ID %s after promotion", n.ID)
		}
		seen[n.ID] = true
	}
	for _, n := range applied {
		if n.Meta["pattern"] == "aj_perform_method_candidate" {
			t.Errorf("candidate %s survived the pass", n.ID)
		}
	}
}

// TestJobInherit_SeedIsNotDuplicated covers the class the predicated pattern
// already matched: it has a seed subscriber *and* a candidate on the same
// line.
func TestJobInherit_SeedIsNotDuplicated(t *testing.T) {
	nodes, edges := cjChain(1)
	seed := graph.Node{
		ID: cjSubscriberID("job0"), Type: graph.NodeTypeSubscriber, Label: "perform",
		Service: cjService, File: cjFile("job0"), Line: 1, Language: "ruby",
		Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "job0"},
	}
	nodes = append(nodes, seed)

	res := jiRun(t, nodes, edges)

	if len(res.Nodes) != 0 {
		t.Fatalf("promoted %+v; the seed already covers job0", res.Nodes)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != cjCandidateID("job0") {
		t.Fatalf("deleted %v, want the redundant candidate", res.Deleted)
	}
	if len(res.Edges) != 1 || res.Edges[0].From != seed.ID || res.Edges[0].To != cjPerformID("job0") {
		t.Fatalf("edges %+v, want one job_perform seed -> perform method", res.Edges)
	}
}

// TestJobInherit_Edges is the second half of the tier's edge gain: a
// subscriber with no edge to the method it stands for is a leaf, and a
// subscriber with no `contains` edge from its class hangs off nothing.
func TestJobInherit_Edges(t *testing.T) {
	nodes, edges := cjChain(2)

	res := jiRun(t, nodes, edges)

	perform := map[string]string{}
	contains := map[string]string{}
	for _, e := range res.Edges {
		switch e.Type {
		case graph.EdgeTypeJobPerform:
			perform[e.From] = e.To
		case graph.EdgeTypeContains:
			contains[e.To] = e.From
		default:
			t.Errorf("unexpected edge type %s on %s", e.Type, e.ID)
		}
	}

	for _, class := range []string{"job0", "job1"} {
		sub := cjSubscriberID(class)
		if got := perform[sub]; got != cjPerformID(class) {
			t.Errorf("%s: job_perform -> %q, want %q", class, got, cjPerformID(class))
		}
		if got := contains[sub]; got != cjClassID(class) {
			t.Errorf("%s: contains from %q, want %q", class, got, cjClassID(class))
		}
	}
	if len(perform) != 2 || len(contains) != 2 {
		t.Errorf("edges %+v: want exactly one job_perform and one contains per promoted class", res.Edges)
	}
}
