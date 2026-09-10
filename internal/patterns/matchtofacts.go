package patterns

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
)

// fanoutVerbs are the verbs that turn one capture into N values. Two args that
// name the same capture and the same fan-out verb are zipped (entry k of one ↔
// entry k of the other — hash_pairs(key)/hash_pairs(value)); everything else
// multiplies.
var fanoutVerbs = map[string]bool{
	"list_elements": true,
	"hash_pairs":    true,
	"each_arg":      true,
}

// MatchToFacts lowers one pattern match to the facts its `facts:` block
// declares. capNodes are the match's captures (name → tree-sitter node),
// anchor is the pattern's `@...` anchor node (the source for args with no
// `capture:`). One spec normally yields one fact; a fan-out arg
// (list_elements / hash_pairs) yields one fact per element.
func MatchToFacts(m MatchResult, specs []FactSpec, ec *ExtractContext) []factpipe.Fact {
	if ec.Src == nil {
		ec.Src = m.Src
	}
	if ec.Grammar == "" {
		ec.Grammar = m.Grammar
	}
	if ec.File == "" {
		ec.File = m.File
	}
	anchor := m.AnchorNode
	var out []factpipe.Fact
	for _, spec := range specs {
		out = append(out, factsForSpec(spec, m, anchor, ec)...)
	}
	return out
}

func factsForSpec(spec FactSpec, m MatchResult, anchor *sitter.Node, ec *ExtractContext) []factpipe.Fact {
	n := len(spec.Args)
	vals := make([][]verbVal, n)
	fanKey := make([]string, n)
	for i, a := range spec.Args {
		vals[i] = evalArg(a, m.CaptureNodes, anchor, ec)
		base, _ := parseVerb(a.Extract)
		if a.Then != "" {
			base, _ = parseVerb(a.Then)
		}
		if fanoutVerbs[base] {
			fanKey[i] = a.Capture + "\x00" + base
		}
	}

	// Any fan-out arg that produced zero values ⇒ the spec produces no fact
	// (e.g. `only:` absent).
	for i := range spec.Args {
		if fanKey[i] != "" && len(vals[i]) == 0 {
			return nil
		}
		if len(vals[i]) == 0 {
			vals[i] = []verbVal{{Kind: factpipe.AtomStr}}
		}
	}

	// Build cartesian dimensions: zipped groups share a fanKey, everything
	// else is its own single-length dimension.
	type dim struct {
		args []int
		len  int
	}
	var dims []dim
	seen := map[string]int{}
	for i := range spec.Args {
		if fanKey[i] == "" {
			continue
		}
		if di, ok := seen[fanKey[i]]; ok {
			dims[di].args = append(dims[di].args, i)
			if len(vals[i]) < dims[di].len {
				dims[di].len = len(vals[i])
			}
			continue
		}
		seen[fanKey[i]] = len(dims)
		dims = append(dims, dim{args: []int{i}, len: len(vals[i])})
	}

	origin := factpipe.Origin{
		Kind:    factpipe.OriginPattern,
		File:    ec.File,
		Line:    m.Line,
		Pattern: m.PatternName,
	}

	// Non-fan-out args are pinned to element 0 (they are single-valued).
	pick := make([]int, n)
	var facts []factpipe.Fact
	var rec func(d int)
	rec = func(d int) {
		if d == len(dims) {
			args := make([]factpipe.Atom, n)
			for i := range spec.Args {
				args[i] = vals[i][pick[i]].atom()
			}
			facts = append(facts, factpipe.Fact{Pred: spec.Pred, Args: args, Origin: origin})
			return
		}
		for k := 0; k < dims[d].len; k++ {
			for _, ai := range dims[d].args {
				pick[ai] = k
			}
			rec(d + 1)
		}
	}
	rec(0)
	return facts
}
