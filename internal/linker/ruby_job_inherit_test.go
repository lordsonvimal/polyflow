package linker

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// The CJ fixtures below model what the parser + LinkRubyTypeRelations have
// already put in the graph by the time this pass runs: one class node and one
// `perform` method node per job file, `inherits` edges between the class
// nodes, and one `aj_perform_method_candidate` function node per
// `perform`-defining class (the unpredicated pattern), sitting on the class's
// own line.

const cjService = "orion"

func cjFile(class string) string {
	return fmt.Sprintf("app/jobs/%s.rb", class)
}

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

// cjClass emits the three nodes a `perform`-defining class contributes: the
// class node, its `perform` method node, and the candidate nomination.
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

// cjBareClass emits a class node with no `perform` and no candidate — a base
// class that only exists to be inherited from (ApplicationJob itself).
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

// cjChain builds ApplicationJob plus `depth` project classes above it, each
// defining `perform`: job0 < ApplicationJob, job1 < job0, … The class at
// index i is `depth-i` hops from the root… counted the other way round:
// job0 is 1 hop, job1 is 2, and so on.
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

func cjPromotedIDs(res JobInheritResult) map[string]bool {
	ids := map[string]bool{}
	for _, n := range res.Promoted {
		ids[n.ID] = true
	}
	return ids
}

// TestPromoteInheritedJobPerform_ChainDepths is the tier's core claim: a
// project base class between the job and ApplicationJob no longer hides the
// job. Depth 1 is the shape the predicated pattern already handles; 2 and 4
// are what only an inherits walk can see.
func TestPromoteInheritedJobPerform_ChainDepths(t *testing.T) {
	t.Parallel()
	for _, depth := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			t.Parallel()
			nodes, edges := cjChain(depth)
			res := PromoteInheritedJobPerform(nodes, edges)

			if len(res.Promoted) != depth {
				t.Fatalf("depth %d: promoted %d classes, want %d", depth, len(res.Promoted), depth)
			}
			if len(res.Ledger) != 0 {
				t.Fatalf("depth %d: unexpected ledger %+v", depth, res.Ledger)
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
			// hops must be the real distance to ApplicationJob, not a constant.
			for _, n := range res.Promoted {
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

// TestPromoteInheritedJobPerform_BeyondHopBound proves the bound is a real
// stop, and that stopping is ledgered rather than silent: a chain this deep is
// probably a job, but saying so would be a guess.
func TestPromoteInheritedJobPerform_BeyondHopBound(t *testing.T) {
	t.Parallel()
	nodes, edges := cjChain(maxJobInheritHops + 1)
	res := PromoteInheritedJobPerform(nodes, edges)

	deepest := fmt.Sprintf("job%d", maxJobInheritHops)
	if cjPromotedIDs(res)[cjSubscriberID(deepest)] {
		t.Fatalf("%s is %d hops from the root and must not promote", deepest, maxJobInheritHops+1)
	}
	if len(res.Promoted) != maxJobInheritHops {
		t.Fatalf("promoted %d, want %d (everything inside the bound)", len(res.Promoted), maxJobInheritHops)
	}
	if len(res.Ledger) != 1 {
		t.Fatalf("ledger %+v, want exactly one job_base_unresolved", res.Ledger)
	}
	got := res.Ledger[0]
	if got.Kind != "job_base_unresolved" || got.Name != deepest || got.File != cjFile(deepest) {
		t.Errorf("ledger entry %+v, want job_base_unresolved for %s", got, deepest)
	}
}

// TestPromoteInheritedJobPerform_NoPathToRoot is the over-promotion guard: the
// tier must not turn every PORO with a `perform` method into a job. Widening
// the pattern's regex to `.*Job$` would have done exactly that.
func TestPromoteInheritedJobPerform_NoPathToRoot(t *testing.T) {
	t.Parallel()
	nodes := append(cjClass("Presenter"), cjBareClass("BasePresenter"))
	nodes = append(nodes, cjBareClass("ApplicationJob"))
	edges := []graph.Edge{cjInherits("Presenter", "BasePresenter")}

	res := PromoteInheritedJobPerform(nodes, edges)

	if len(res.Promoted) != 0 {
		t.Fatalf("promoted %+v; a class that reaches no ActiveJob root is not a job", res.Promoted)
	}
	if len(res.Ledger) != 0 {
		t.Errorf("ledger %+v; a PORO is a decided no, not an unresolved", res.Ledger)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != cjCandidateID("Presenter") {
		t.Errorf("dropped %v, want just the Presenter candidate", res.Dropped)
	}
	for _, e := range res.Edges {
		t.Errorf("unexpected edge %s -> %s", e.From, e.To)
	}
}

// TestPromoteInheritedJobPerform_Diamond covers a class that reaches the root
// two ways at once (a superclass *and* an included module that inherits). The
// walk must settle on one node, not one per path — two subscriber nodes for
// one class is exactly the fan-out this tier may not introduce.
func TestPromoteInheritedJobPerform_Diamond(t *testing.T) {
	t.Parallel()
	nodes, edges := cjChain(1) // job0 < ApplicationJob
	nodes = append(nodes, cjClass("DiamondJob")...)
	nodes = append(nodes, cjBareClass("Retryable"))
	edges = append(edges,
		cjInherits("DiamondJob", "job0"),
		// `include Retryable` is emitted as an inherits edge too.
		cjInherits("DiamondJob", "Retryable"),
		cjInherits("Retryable", "job0"),
	)

	res := PromoteInheritedJobPerform(nodes, edges)

	seen := 0
	for _, n := range res.Promoted {
		if n.ID == cjSubscriberID("DiamondJob") {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("DiamondJob promoted %d times, want exactly 1", seen)
	}
}

// TestPromoteInheritedJobPerform_ReplacesCandidateInPlace pins the node-count
// invariant the mutating-pass rule exists for: promotion is a replacement, so
// applying the result must leave the graph with the same number of nodes and
// no duplicate ID.
func TestPromoteInheritedJobPerform_ReplacesCandidateInPlace(t *testing.T) {
	t.Parallel()
	nodes, edges := cjChain(2)
	before := len(nodes)

	res := PromoteInheritedJobPerform(nodes, edges)

	// Apply the result the way link_passes.go does.
	promotedByID := map[string]graph.Node{}
	for _, n := range res.Promoted {
		promotedByID[n.ID] = n
	}
	applied := make([]graph.Node, 0, len(nodes))
	dropped := map[string]bool{}
	for _, id := range res.Dropped {
		dropped[id] = true
	}
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

// TestPromoteInheritedJobPerform_SeedIsNotDuplicated covers the class the
// predicated pattern already matched: it has a seed subscriber *and* a
// candidate on the same line. Promoting the candidate would hand the enqueue
// matcher two identical targets.
func TestPromoteInheritedJobPerform_SeedIsNotDuplicated(t *testing.T) {
	t.Parallel()
	nodes, edges := cjChain(1)
	seed := graph.Node{
		ID: cjSubscriberID("job0"), Type: graph.NodeTypeSubscriber, Label: "perform",
		Service: cjService, File: cjFile("job0"), Line: 1, Language: "ruby",
		Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "job0"},
	}
	nodes = append(nodes, seed)

	res := PromoteInheritedJobPerform(nodes, edges)

	if len(res.Promoted) != 0 {
		t.Fatalf("promoted %+v; the seed already covers job0", res.Promoted)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != cjCandidateID("job0") {
		t.Fatalf("dropped %v, want the redundant candidate", res.Dropped)
	}
	// The seed still gets its job_perform edge — that half is not conditional
	// on promotion.
	if len(res.Edges) != 1 || res.Edges[0].From != seed.ID || res.Edges[0].To != cjPerformID("job0") {
		t.Fatalf("edges %+v, want one job_perform seed -> perform method", res.Edges)
	}
}

// TestPromoteInheritedJobPerform_Edges is the second half of the tier's edge
// gain. A subscriber with no edge to the method it stands for is a leaf and the
// trace stops one node short of the code that runs; a subscriber with no
// `contains` edge from its class hangs off nothing at all, which is what a
// directly-matched subscriber gets from the parser and a promoted one must be
// given here.
func TestPromoteInheritedJobPerform_Edges(t *testing.T) {
	t.Parallel()
	nodes, edges := cjChain(2)

	res := PromoteInheritedJobPerform(nodes, edges)

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
