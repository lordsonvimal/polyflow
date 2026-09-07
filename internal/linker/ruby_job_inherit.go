package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// maxJobInheritHops bounds the inherits walk from a `perform`-defining class
// up to a known ActiveJob root. Cedar's deepest real chain is 2
// (Job -> ProjectBase -> ApplicationJob); 4 leaves headroom without letting
// an unrelated deep hierarchy drift into the job graph.
const maxJobInheritHops = 4

// activeJobRootNames are the framework class names that terminate the walk.
// A class reaching one of these over `inherits` edges *is* an ActiveJob,
// however many project base classes sit in between. ApplicationJob is the
// Rails generator's own base and is in-repo (so it has a class node and an
// inbound inherits edge); ActiveJob::Base is not in-repo, but a class naming
// it directly already matches the predicated `aj_perform_method` pattern and
// arrives here as a seed rather than a candidate.
var activeJobRootNames = map[string]bool{
	"ApplicationJob":  true,
	"ActiveJob::Base": true,
}

// JobInheritResult is what PromoteInheritedJobPerform decided about every
// `aj_perform_method_candidate` node in the graph. Every candidate lands in
// exactly one of Promoted/Replaced (it reaches an ActiveJob root) or Dropped
// (it does not, or a seed already covers the same class) — a candidate node
// is bookkeeping for this pass and must never survive it.
type JobInheritResult struct {
	// Promoted are real subscriber nodes, one per promoted candidate, carrying
	// the pattern/job_class meta contracts/jobs.yaml's ActiveJob rule joins on.
	Promoted []graph.Node
	// Replaced maps candidate node ID -> promoted node ID. The caller swaps
	// each candidate in place (the node ID embeds the node type, so promotion
	// cannot keep the old ID) rather than appending, so the node count is
	// unchanged and no class ends up with two subscriber nodes — two would
	// make the enqueue matcher mint fan-out where there is exactly one target.
	Replaced map[string]string
	// Dropped are candidate node IDs to delete outright.
	Dropped []string
	// Edges are job_perform edges from each ActiveJob subscriber (promoted and
	// seed alike) to the `perform` method node it stands for.
	Edges []graph.Edge
	// Ledger holds one job_base_unresolved entry per candidate whose chain was
	// still climbing when it ran out of hops — a real inheritance chain deeper
	// than maxJobInheritHops, surfaced rather than silently dropped.
	Ledger []graph.UnresolvedRef
}

// PromoteInheritedJobPerform finds `perform`-defining classes that reach a
// known ActiveJob root through `inherits` edges and promotes their candidate
// nodes to `subscriber`, so the jobs contract can match them as enqueue
// targets.
//
// The predicated `aj_perform_method` pattern tests the *direct* superclass
// name, so a project that introduces its own base class
// (ReportJob < ProjectBaseJob < ApplicationJob) mints no subscriber node for
// any of its jobs — a missing target node, not a matching failure. A
// tree-sitter predicate cannot walk an inheritance chain; the chain is
// already in the graph as `inherits` edges, so this pass walks it.
//
// Seeds are nodes already typed `subscriber` with Meta["pattern"] ==
// "aj_perform_method" (the direct-superclass shape the pattern file matches);
// they are never re-promoted, only used to root the closure and to receive a
// job_perform edge.
//
// Must run AFTER the parser and LinkRubyTypeRelations have emitted class and
// `inherits` nodes/edges, and BEFORE any pass that attaches edges to a
// candidate node (containment) or consumes subscriber nodes (the contract
// engine).
func PromoteInheritedJobPerform(nodes []graph.Node, edges []graph.Edge) JobInheritResult {
	res := JobInheritResult{Replaced: map[string]string{}}

	ix := newJobInheritIndex(nodes, edges)
	if len(ix.candidates) == 0 && len(ix.seeds) == 0 {
		return res
	}

	// Seeds first: they need no promotion, only their job_perform edge.
	for _, i := range ix.seeds {
		n := &nodes[i]
		if e := ix.performEdge(n, n.Meta["job_class"]); e != nil {
			res.Edges = append(res.Edges, *e)
		}
	}

	for _, i := range ix.candidates {
		cand := &nodes[i]
		jobClass := cand.Meta["job_class"]

		// A class whose direct superclass already satisfied the predicated
		// pattern has both a seed and a candidate node at the same class line.
		// Promoting the candidate would mint a second subscriber for one class
		// and hand the enqueue matcher two identical targets, so the candidate
		// is redundant bookkeeping: drop it and keep the seed.
		if ix.seedAt(cand.Service, cand.File, cand.Line) {
			res.Dropped = append(res.Dropped, cand.ID)
			continue
		}

		classID, ok := ix.classNodeFor(cand, jobClass)
		if !ok {
			res.Dropped = append(res.Dropped, cand.ID)
			continue
		}

		hops, reached, exhausted := ix.hopsToRoot(classID)
		switch {
		case reached:
			promoted := ix.promote(cand, jobClass, hops)
			res.Promoted = append(res.Promoted, promoted)
			res.Replaced[cand.ID] = promoted.ID
			if e := ix.performEdge(&promoted, jobClass); e != nil {
				res.Edges = append(res.Edges, *e)
			}
			// The structural edge ruby.go's linkRubyEnclosingCalls gives a
			// directly-matched subscriber (its class body declares it, it is
			// not called). That pass ran during parsing, long before this one,
			// so a promoted subscriber has to be given the same edge here or it
			// hangs off nothing and `trace --root ReportJob` never reaches it.
			res.Edges = append(res.Edges, graph.Edge{
				ID:   fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeContains), classID, promoted.ID),
				From: classID,
				To:   promoted.ID,
				Type: graph.EdgeTypeContains,
			})
		case exhausted:
			// The chain was still climbing at the hop bound: this may well be a
			// job, but proving it would mean guessing past the bound. Ledger it.
			res.Ledger = append(res.Ledger, graph.UnresolvedRef{
				Service: cand.Service, File: cand.File, Line: cand.Line,
				Name: jobClass, Kind: "job_base_unresolved",
			})
			res.Dropped = append(res.Dropped, cand.ID)
		default:
			// Reaches a root of its own that is not an ActiveJob — an ordinary
			// PORO that happens to define `perform`. Never a job.
			res.Dropped = append(res.Dropped, cand.ID)
		}
	}

	sort.Strings(res.Dropped)
	return res
}

// jobInheritIndex is the read-only view of the graph this pass walks: the
// candidate/seed node positions, the class-node topology, and the `perform`
// method nodes a job_perform edge lands on.
type jobInheritIndex struct {
	nodes []graph.Node

	// candidates/seeds are indices into nodes, in file/line order so the
	// result is deterministic across runs.
	candidates []int
	seeds      []int

	// seedPos marks "service\x00file\x00line" positions already covered by a
	// seed subscriber.
	seedPos map[string]bool

	// classByID / classNameOf describe the class-node layer: superIDs is the
	// `inherits` adjacency (subclass ID -> superclass IDs, covering both a
	// real superclass and an include/extend/prepend of a module).
	classNameOf map[string]string
	superIDs    map[string][]string
	// classesInFile lists class node IDs per service+file, ascending by line,
	// for the name lookup a candidate needs.
	classesInFile map[string][]classAtLine

	// performByClass maps service+file -> "Class#perform" -> method node ID.
	// Keyed by file, not by service: two unrelated classes of the same name in
	// one service would otherwise collide and hand a job a `perform` from
	// another file entirely.
	performByClass map[string]map[string]string
	// performInFile maps service+file -> the `perform` method nodes in it,
	// ascending by line — the fallback when the parser recorded no usable
	// class name on the method.
	performInFile map[string][]classAtLine
}

// classAtLine is a node ID paired with its declaration line, for the
// innermost-enclosing lookups below.
type classAtLine struct {
	id   string
	name string
	line int
}

func jobPosKey(service, file string, line int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", service, file, line)
}

func jobFileKey(service, file string) string {
	return service + "\x00" + file
}

func newJobInheritIndex(nodes []graph.Node, edges []graph.Edge) *jobInheritIndex {
	ix := &jobInheritIndex{
		nodes:          nodes,
		seedPos:        map[string]bool{},
		classNameOf:    map[string]string{},
		superIDs:       map[string][]string{},
		classesInFile:  map[string][]classAtLine{},
		performByClass: map[string]map[string]string{},
		performInFile:  map[string][]classAtLine{},
	}

	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeSubscriber:
			if n.Meta["pattern"] == "aj_perform_method" {
				ix.seeds = append(ix.seeds, i)
				ix.seedPos[jobPosKey(n.Service, n.File, n.Line)] = true
			}
		case graph.NodeTypeVariable:
			if n.Meta["pattern"] == "aj_perform_method_candidate" {
				ix.candidates = append(ix.candidates, i)
			}
		case graph.NodeTypeFunction:
			switch {
			case n.Label == "perform" && isRubyFile(n.File):
				// The Ruby parser's own method node (type function,
				// qualified_name "Class#perform").
				k := jobFileKey(n.Service, n.File)
				if qn := n.Meta["qualified_name"]; qn != "" {
					if ix.performByClass[k] == nil {
						ix.performByClass[k] = map[string]string{}
					}
					ix.performByClass[k][qn] = n.ID
				}
				ix.performInFile[k] = append(ix.performInFile[k], classAtLine{id: n.ID, name: n.Label, line: n.Line})
			}
		case graph.NodeTypeClass:
			ix.classNameOf[n.ID] = n.Label
			k := jobFileKey(n.Service, n.File)
			ix.classesInFile[k] = append(ix.classesInFile[k], classAtLine{id: n.ID, name: n.Label, line: n.Line})
		}
	}

	for _, e := range edges {
		if e.Type != graph.EdgeTypeInherits {
			continue
		}
		ix.superIDs[e.From] = append(ix.superIDs[e.From], e.To)
	}

	for k := range ix.classesInFile {
		sortByLine(ix.classesInFile[k])
	}
	for k := range ix.performInFile {
		sortByLine(ix.performInFile[k])
	}
	sortNodeIdx(nodes, ix.candidates)
	sortNodeIdx(nodes, ix.seeds)
	return ix
}

func sortByLine(s []classAtLine) {
	sort.Slice(s, func(i, j int) bool { return s[i].line < s[j].line })
}

func sortNodeIdx(nodes []graph.Node, idx []int) {
	sort.Slice(idx, func(i, j int) bool {
		a, b := &nodes[idx[i]], &nodes[idx[j]]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
}

func (ix *jobInheritIndex) seedAt(service, file string, line int) bool {
	return ix.seedPos[jobPosKey(service, file, line)]
}

// classNodeFor resolves the class node a candidate was matched inside. The
// candidate's line *is* the class declaration's line (the matcher takes the
// earliest named capture, which is the class name), so the exact-line lookup
// below is the normal path; the name scan only covers a service whose class
// node landed on a different line than the pattern match.
func (ix *jobInheritIndex) classNodeFor(cand *graph.Node, jobClass string) (string, bool) {
	inFile := ix.classesInFile[jobFileKey(cand.Service, cand.File)]
	for _, c := range inFile {
		if c.line == cand.Line && c.name == jobClass {
			return c.id, true
		}
	}
	// Nearest declaration of that name at or above the candidate's line.
	best := ""
	for _, c := range inFile {
		if c.name == jobClass && c.line <= cand.Line {
			best = c.id
		}
	}
	return best, best != ""
}

// hopsToRoot walks `inherits` edges upward from classID. reached reports that
// an ActiveJob root was found within maxJobInheritHops (hops is the distance
// to it); exhausted reports that the walk still had unexplored superclasses
// when it hit the bound, which is a "cannot tell", not a "no".
func (ix *jobInheritIndex) hopsToRoot(classID string) (hops int, reached, exhausted bool) {
	if activeJobRootNames[ix.classNameOf[classID]] {
		return 0, true, false
	}
	seen := map[string]bool{classID: true}
	frontier := []string{classID}
	for hop := 1; hop <= maxJobInheritHops; hop++ {
		var next []string
		for _, id := range frontier {
			for _, super := range ix.superIDs[id] {
				if seen[super] {
					continue
				}
				seen[super] = true
				// A class node whose *name* is a root ends the walk. The
				// candidate's own superclass may also be the framework class
				// with no in-repo node at all, but that shape matches the
				// predicated pattern and never reaches this pass.
				if activeJobRootNames[ix.classNameOf[super]] {
					return hop, true, false
				}
				next = append(next, super)
			}
		}
		if len(next) == 0 {
			return 0, false, false
		}
		frontier = next
	}
	return 0, false, true
}

// promote builds the subscriber node that replaces cand. The ID is the
// canonical `aj_perform_method` shape — identical to what the predicated
// pattern would have minted had the superclass been spelled ApplicationJob —
// so nothing downstream can tell a promoted job from a directly-matched one.
func (ix *jobInheritIndex) promote(cand *graph.Node, jobClass string, hops int) graph.Node {
	meta := make(map[string]string, len(cand.Meta)+3)
	for k, v := range cand.Meta {
		meta[k] = v
	}
	meta["pattern"] = "aj_perform_method"
	meta["job_class"] = jobClass
	meta["job_inherit_hops"] = fmt.Sprintf("%d", hops)
	delete(meta, "perform_name")

	n := *cand
	n.ID = fmt.Sprintf("%s:%s:%s:aj_perform_method:%d",
		cand.Service, cand.File, string(graph.NodeTypeSubscriber), cand.Line)
	n.Type = graph.NodeTypeSubscriber
	n.Label = "perform"
	n.Meta = meta
	return n
}

// performEdge links a job subscriber to the `perform` method node it stands
// for. Without it a subscriber is a leaf: the enqueue side lands on it and the
// trace stops one node short of the code that actually runs.
func (ix *jobInheritIndex) performEdge(sub *graph.Node, jobClass string) *graph.Edge {
	to := ""
	if jobClass != "" {
		// jobClass may be namespaced as written (`Admin::ReportJob`) while the
		// parser records the innermost class name; try both spellings.
		short := jobClass
		if i := strings.LastIndex(jobClass, "::"); i >= 0 {
			short = jobClass[i+2:]
		}
		byClass := ix.performByClass[jobFileKey(sub.Service, sub.File)]
		for _, key := range []string{jobClass + "#perform", short + "#perform"} {
			if id, ok := byClass[key]; ok {
				to = id
				break
			}
		}
	}
	if to == "" {
		// Fall back to the first `perform` method declared at or below the
		// class line in the same file. Same-file and below-the-class-line
		// together are what make this safe: a file's second job class would
		// have its own candidate node and pick its own nearer method.
		for _, p := range ix.performInFile[jobFileKey(sub.Service, sub.File)] {
			if p.line >= sub.Line {
				to = p.id
				break
			}
		}
	}
	if to == "" || to == sub.ID {
		return nil
	}
	return &graph.Edge{
		ID:         fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeJobPerform), sub.ID, to),
		From:       sub.ID,
		To:         to,
		Type:       graph.EdgeTypeJobPerform,
		Confidence: graph.ConfidenceInferred,
		Meta:       map[string]string{"via": "aj_perform_method"},
	}
}
