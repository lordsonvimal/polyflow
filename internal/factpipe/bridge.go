package factpipe

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// GraphFacts is the FX.1 bridge: it asserts the graph-so-far (graph.Snapshot)
// into a FactSet as base relations, against a frozen schema a framework `.dl`
// rule joins against. See docs/declarative-framework-pipeline-plan.md § FX.1.
//
//	relation                          arity  source
//	node(Id, Type, File, Service)        4    every node
//	node_label(Id, Label)               2    every node with a non-empty Label
//	node_line(Id, Line, EndLine)         3    nodes with EndLine > 0 (int atoms)
//	node_meta(Id, Key, Value)            3    every Meta entry (keys sorted)
//	calls_edge(From, Label, To)          3    "calls" edges, label denormalized
//	calls_edge_any(From, Label)          2    projection of the above, for `not`
//	calls_edge_seq(From, Label, To, Seq) 4    "calls" edges + their slice ordinal
//	edge(From, Type, To)                 3    every edge
//	inherits(Sub, Super)                 2    "inherits" edges
//	defines(Klass, Name, Id)             3    class node --contains--> declaration
//	import(File, Package)                2    Snapshot.Imports
//	resolved(Site, Name, Target, Depth)  4    Snapshot.Resolved (Depth is an int atom)
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
