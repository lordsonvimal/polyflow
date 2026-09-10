package factpipe

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ancestorDistCap bounds the ancestor_dist BFS. Ruby inheritance cannot cycle,
// but a malformed graph can; the walk-based consumers (resolveCallback) never
// look past 16 levels anyway.
const ancestorDistCap = 32

// GraphFacts is the FX.1 bridge: it asserts the graph-so-far (graph.Snapshot)
// into a FactSet as base relations, against a frozen schema a framework `.dl`
// rule joins against. See docs/declarative-framework-pipeline-plan.md § FX.1.
//
//	relation                          arity  source
//	node(Id, Type, File, Service)        4    every node
//	node_label(Id, Label)               2    every node with a non-empty Label
//	node_start(Id, Line)                 2    every node with Line > 0 (int atom)
//	node_line(Id, Line, EndLine)         3    nodes with EndLine > 0 (int atoms)
//	node_meta(Id, Key, Value)            3    every Meta entry (keys sorted)
//	calls_edge(From, Label, To)          3    "calls" edges, label denormalized
//	calls_edge_any(From, Label)          2    projection of the above, for `not`
//	calls_edge_seq(From, Label, To, Seq) 4    "calls" edges + their slice ordinal
//	edge(From, Type, To)                 3    every edge
//	inherits(Sub, Super)                 2    "inherits" edges
//	class_super(Sub, Super)              2    "inherits" edges with meta.via != "mixin"
//	includes_module(Klass, Module)       2    "inherits" edges with meta.via == "mixin"
//	ancestor_dist(Sub, Anc, Depth)       3    BFS hop-count over "inherits" (int atom)
//	defines(Klass, Name, Id)             3    class node --contains--> declaration
//	file_rank(File, N)                   2    distinct node files, lexical order (int atom)
//	import(File, Package)                2    Snapshot.Imports
//	resolved(Site, Name, Target, Depth)  4    Snapshot.Resolved (Depth is an int atom)
//
// ancestor_dist is the transitive closure of "inherits" carrying the minimum
// hop count from Sub to Anc — a class is at its own distance 0 is NOT emitted;
// the first hop is Depth 1. It exists so a rule can reproduce a level-order
// ancestry walk (rails_filters' resolveCallback: "the first ancestor level that
// defines the callback, and how far up it was") with a min(Depth) aggregate.
// Ties at a level are all emitted, matching the walk taking every hit at the
// first non-empty level. Bounded at ancestorDistCap hops (cycle guard); a
// consumer that wants the walk's tighter bound filters with le(Depth, N).
//
// file_rank assigns each distinct file a dense 0-based lexical rank, so a rule
// can order derivations by source-scan position (rails_filters' dedup tie-break
// keeps the earliest-declared registration's meta) without a string comparison,
// which the engine's integer-only min/max cannot do.
//
// Output is deterministic: relations are emitted in Snapshot slice order, with
// per-node Meta keys sorted and calls_edge_any in first-seen order. Every fact
// carries an OriginGraph Origin whose Pattern is the relation name, so a wrong
// edge derived from a base fact can still name its source.
//
// calls_edge_seq carries the "calls" edge's 0-based ordinal in Snapshot.Edges
// (calls edges only, in slice order). It exists so a rule can reproduce a Go
// link pass's last-write-wins target pick — where several already-resolved
// calls edges from one site reach different nodes sharing a label and the pass
// kept whichever landed last in the edge slice — with a max(Seq) aggregate.
// Set-semantics rules cannot otherwise express "the last one inserted".
func GraphFacts(s graph.Snapshot, fs FactSet) {
	fileOf := make(map[string]string, len(s.Nodes))
	typeOf := make(map[string]graph.NodeType, len(s.Nodes))
	labelOf := make(map[string]string, len(s.Nodes))
	for i := range s.Nodes {
		n := &s.Nodes[i]
		fileOf[n.ID] = n.File
		typeOf[n.ID] = n.Type
		labelOf[n.ID] = n.Label
	}

	origin := func(rel, file string, line int) Origin {
		return Origin{Kind: OriginGraph, File: file, Line: line, Pattern: rel}
	}

	for i := range s.Nodes {
		n := &s.Nodes[i]
		fs.Add(Fact{
			Pred:   "node",
			Args:   []Atom{Node(n.ID), Str(string(n.Type)), Str(n.File), Str(n.Service)},
			Origin: origin("node", n.File, n.Line),
		})
		if n.Label != "" {
			fs.Add(Fact{
				Pred:   "node_label",
				Args:   []Atom{Node(n.ID), Str(n.Label)},
				Origin: origin("node_label", n.File, n.Line),
			})
		}
		if n.Line > 0 {
			fs.Add(Fact{
				Pred:   "node_start",
				Args:   []Atom{Node(n.ID), Int(int64(n.Line))},
				Origin: origin("node_start", n.File, n.Line),
			})
		}
		if n.EndLine > 0 {
			fs.Add(Fact{
				Pred:   "node_line",
				Args:   []Atom{Node(n.ID), Int(int64(n.Line)), Int(int64(n.EndLine))},
				Origin: origin("node_line", n.File, n.Line),
			})
		}
		if len(n.Meta) > 0 {
			keys := make([]string, 0, len(n.Meta))
			for k := range n.Meta {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fs.Add(Fact{
					Pred:   "node_meta",
					Args:   []Atom{Node(n.ID), Str(k), Str(n.Meta[k])},
					Origin: origin("node_meta", n.File, n.Line),
				})
			}
		}
	}

	// file_rank: every distinct node file, dense 0-based lexical rank.
	fileSet := make(map[string]bool, len(s.Nodes))
	for i := range s.Nodes {
		fileSet[s.Nodes[i].File] = true
	}
	rankedFiles := make([]string, 0, len(fileSet))
	for f := range fileSet {
		rankedFiles = append(rankedFiles, f)
	}
	sort.Strings(rankedFiles)
	for n, f := range rankedFiles {
		fs.Add(Fact{
			Pred:   "file_rank",
			Args:   []Atom{Str(f), Int(int64(n))},
			Origin: origin("file_rank", f, 0),
		})
	}

	inheritsAdj := make(map[string][]string, len(s.Nodes))

	callsAnySeen := make(map[[2]string]bool)
	callsSeq := int64(0)
	for i := range s.Edges {
		e := &s.Edges[i]
		efile := fileOf[e.From]

		fs.Add(Fact{
			Pred:   "edge",
			Args:   []Atom{Node(e.From), Str(string(e.Type)), Node(e.To)},
			Origin: origin("edge", efile, 0),
		})

		switch e.Type {
		case graph.EdgeTypeCalls:
			fs.Add(Fact{
				Pred:   "calls_edge",
				Args:   []Atom{Node(e.From), Str(e.Label), Node(e.To)},
				Origin: origin("calls_edge", efile, 0),
			})
			fs.Add(Fact{
				Pred:   "calls_edge_seq",
				Args:   []Atom{Node(e.From), Str(e.Label), Node(e.To), Int(callsSeq)},
				Origin: origin("calls_edge_seq", efile, 0),
			})
			callsSeq++
			key := [2]string{e.From, e.Label}
			if !callsAnySeen[key] {
				callsAnySeen[key] = true
				fs.Add(Fact{
					Pred:   "calls_edge_any",
					Args:   []Atom{Node(e.From), Str(e.Label)},
					Origin: origin("calls_edge_any", efile, 0),
				})
			}
		case graph.EdgeTypeInherits:
			fs.Add(Fact{
				Pred:   "inherits",
				Args:   []Atom{Node(e.From), Node(e.To)},
				Origin: origin("inherits", efile, 0),
			})
			inheritsAdj[e.From] = append(inheritsAdj[e.From], e.To)
			// The "inherits" edge conflates a Ruby superclass with an
			// include/extend/prepend (meta.via distinguishes them). A rule that
			// propagates filter registrations by inheritance must not conflate
			// them, so split the edge here by its via.
			if e.Meta["via"] == "mixin" {
				fs.Add(Fact{
					Pred:   "includes_module",
					Args:   []Atom{Node(e.From), Node(e.To)},
					Origin: origin("includes_module", efile, 0),
				})
			} else {
				fs.Add(Fact{
					Pred:   "class_super",
					Args:   []Atom{Node(e.From), Node(e.To)},
					Origin: origin("class_super", efile, 0),
				})
			}
		case graph.EdgeTypeContains:
			if typeOf[e.From] == graph.NodeTypeClass {
				name := labelOf[e.To]
				fs.Add(Fact{
					Pred:   "defines",
					Args:   []Atom{Node(e.From), Str(name), Node(e.To)},
					Origin: origin("defines", efile, 0),
				})
			}
		}
	}

	// ancestor_dist: BFS over inheritsAdj from every class node, emitting the
	// minimum hop count to each reachable ancestor. Nodes iterate in Snapshot
	// order and each frontier in edge-slice order, so the output is stable.
	for i := range s.Nodes {
		if typeOf[s.Nodes[i].ID] != graph.NodeTypeClass {
			continue
		}
		sub := s.Nodes[i].ID
		seen := map[string]bool{sub: true}
		frontier := []string{sub}
		for depth := 1; depth <= ancestorDistCap && len(frontier) > 0; depth++ {
			var next []string
			for _, cur := range frontier {
				for _, anc := range inheritsAdj[cur] {
					if seen[anc] {
						continue
					}
					seen[anc] = true
					next = append(next, anc)
					fs.Add(Fact{
						Pred:   "ancestor_dist",
						Args:   []Atom{Node(sub), Node(anc), Int(int64(depth))},
						Origin: origin("ancestor_dist", fileOf[sub], 0),
					})
				}
			}
			frontier = next
		}
	}

	for _, im := range s.Imports {
		fs.Add(Fact{
			Pred:   "import",
			Args:   []Atom{Str(im.File), Str(im.Package)},
			Origin: origin("import", im.File, 0),
		})
	}

	for _, r := range s.Resolved {
		fs.Add(Fact{
			Pred:   "resolved",
			Args:   []Atom{Node(r.Site), Str(r.Name), Node(r.Target), Int(int64(r.Depth))},
			Origin: origin("resolved", fileOf[r.Site], 0),
		})
	}
}
