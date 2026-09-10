// Package factpipe is the declarative framework pipeline (Tier FX,
// docs/declarative-framework-pipeline-plan.md). A framework or library that
// produces graph edges is expressed as one `<lang>/<framework>.yaml`
// (tree-sitter queries + `extract:` verbs + an `emit:` spec) and one
// `<lang>/<framework>.dl` (stratified datalog), with no framework-specific Go.
//
// This file is the fact IR — the single value type every stage of the pipeline
// exchanges. Stage 1 (extraction) and the FX.1 graph bridge produce Facts;
// the bridge into `internal/datalog` interns them to datalog.Tuple; stage 4
// (emit) turns derived relations back into graph edges.
//
// The engine never sees an Atom: AtomStr / AtomNode intern to a string symbol,
// AtomInt maps to a reserved numeric symbol range the datalog comparison
// builtins (lt/le/ne) understand.
package factpipe

import (
	"sort"
	"strconv"
)

// AtomKind tags which field of an Atom carries its value.
type AtomKind uint8

const (
	// AtomStr is an interned string: an identifier, a literal, a route path,
	// a framework name. The empty string is a legal value — always check Kind,
	// never `a.Str == ""`.
	AtomStr AtomKind = iota
	// AtomNode is a graph.Node.ID, real or synthesized (see the synthesize_node
	// verb). Rules treat it opaquely; stage 4 resolves it to an edge endpoint.
	AtomNode
	// AtomInt is an ordering index, a resolution depth, a source line, a port —
	// anything the lt/le/ne builtins or min/max/count aggregation compare.
	AtomInt
)

// Atom is one term of a fact tuple. Exactly one of Str / Node / Int is
// meaningful, selected by Kind.
type Atom struct {
	Str  string
	Node string
	Int  int64
	Kind AtomKind
}

// Str, Node and Int build the corresponding atom kinds.
func Str(s string) Atom  { return Atom{Str: s, Kind: AtomStr} }
func Node(id string) Atom { return Atom{Node: id, Kind: AtomNode} }
func Int(n int64) Atom    { return Atom{Int: n, Kind: AtomInt} }

// Value renders the atom as the string the datalog bridge interns. AtomInt
// values go to a reserved lexical form so a de-interned result sorts and
// compares consistently; the bridge maps them to the numeric symbol range.
func (a Atom) Value() string {
	switch a.Kind {
	case AtomNode:
		return a.Node
	case AtomInt:
		return strconv.FormatInt(a.Int, 10)
	default:
		return a.Str
	}
}

// OriginKind records which stage produced a fact, so a wrong edge can name the
// construct that caused it (rule-level provenance, SA.1 / criterion 4).
type OriginKind uint8

const (
	// OriginPattern — a tree-sitter pattern match plus its `extract:` verbs.
	OriginPattern OriginKind = iota
	// OriginGraph — the FX.1 bridge, asserting the graph-so-far (nodes, edges,
	// resolved names) as base relations.
	OriginGraph
	// OriginPrimitive — a fact-source primitive: resolve_path, config_value,
	// table_facts.
	OriginPrimitive
	// OriginPlugin — a fact-producer plugin (FX.9), still a generic operation.
	OriginPlugin
)

// Origin is the source construct behind a fact. File+Line locate it; Pattern
// names the pattern / bridge relation / primitive / plugin that emitted it;
// Pos is the fact's position within its parent AST node (0 when not
// applicable), which the ordering verbs (position_in_parent, preceded_by,
// nth_of_kind) surface as AtomInt.
type Origin struct {
	Kind    OriginKind
	File    string
	Line    int
	Pos     int
	Pattern string
}

// Fact is one asserted tuple for one predicate. Pred + len(Args) is the
// relation signature; a rule file that reads a predicate at the wrong arity is
// a load-time error in the datalog engine.
type Fact struct {
	Pred   string
	Args   []Atom
	Origin Origin
}

// FactSet is the accumulator a stage-1 run fills and the datalog bridge drains.
// It is file-addressable so FX.10 (incremental) can drop and re-extract one
// file's facts without a full re-run. Implementations must be deterministic:
// All() returns facts in insertion order, which is stable when extraction is.
type FactSet interface {
	Add(Fact)
	ByFile(file string) []Fact
	All() []Fact
	Len() int
}

// NewFactSet returns the default in-memory FactSet.
func NewFactSet() FactSet { return &factSet{byFile: map[string][]int{}} }

type factSet struct {
	facts  []Fact
	byFile map[string][]int
}

func (s *factSet) Add(f Fact) {
	s.byFile[f.Origin.File] = append(s.byFile[f.Origin.File], len(s.facts))
	s.facts = append(s.facts, f)
}

func (s *factSet) ByFile(file string) []Fact {
	idx := s.byFile[file]
	out := make([]Fact, len(idx))
	for i, j := range idx {
		out[i] = s.facts[j]
	}
	return out
}

func (s *factSet) All() []Fact { return s.facts }

func (s *factSet) Len() int { return len(s.facts) }

// Files returns every file that contributed a fact, sorted — the iteration
// order FX.10's per-file invalidation walks.
func Files(s FactSet) []string {
	fs, ok := s.(*factSet)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(fs.byFile))
	for f := range fs.byFile {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
