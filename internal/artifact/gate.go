package artifact

import (
	"fmt"
	"strings"
)

// Gate default thresholds — the values linker.LoadSchemaURLTables shipped as
// DefaultMinCorroboratedPaths / DefaultMinCorroboratedRatio /
// DefaultMinEntityDiscrimination. Kept here so a mapping that omits a threshold
// gets the tested default.
const (
	DefaultMinMatches        = 5
	DefaultMinRatio          = 0.25
	DefaultMinDiscrimination = 0.5
)

// Gate decides whether an artifact declares facts of a given kind by
// corroborating its normalised leaves against a relation the graph already
// knows (Against — e.g. "handler_path", "amqp_queue_name").
type Gate struct {
	Against           string
	Normalizers       []string
	MinMatches        int
	MinRatio          float64
	MinDiscrimination float64
}

// WithDefaults returns g with every zero threshold replaced by its default.
func (g Gate) WithDefaults() Gate {
	if g.MinMatches == 0 {
		g.MinMatches = DefaultMinMatches
	}
	if g.MinRatio == 0 {
		g.MinRatio = DefaultMinRatio
	}
	if g.MinDiscrimination == 0 {
		g.MinDiscrimination = DefaultMinDiscrimination
	}
	return g
}

// Verdict is the outcome of Gate.Evaluate.
type Verdict struct {
	Qualified   bool
	EntityDepth int    // the container level that discriminates entities
	Matched     []Leaf // leaves whose normalised value corroborated
	Distinct    int    // distinct normalised candidate values in the file
	MatchCount  int    // distinct normalised values that corroborated
	Ratio       float64
	GatePassed  bool    // match count and ratio both cleared the thresholds
	Coverage    float64 // share of corroborated leaves nested below EntityDepth
	Disc        float64 // discrimination at EntityDepth
	Reason      string  // set when !Qualified; suitable for the ledger
	Tuned       bool    // set by the caller when a threshold was loosened
}

// Norm normalises one value through the gate's chain, exposing the same
// canonicalisation callers must apply to their known-fact keys.
func (g Gate) Norm(v string) (string, bool) {
	return resolveChain(g.Normalizers).apply(v)
}

// Evaluate corroborates one artifact against the facts already known for its
// service. known is keyed by service, then by NORMALISED fact value — build it
// with the same chain (Gate.Norm), because corroboration is per-service by
// definition: a fact declared by service A must not qualify an artifact in
// service B.
//
// declared=true means the workspace named this file explicitly (an escape
// hatch): the match/ratio gate is skipped, but the entity level must still be
// discoverable, so a declared file with no discriminating container is still
// not qualified.
func (g Gate) Evaluate(a *Artifact, known map[string]map[string]bool, declared bool) Verdict {
	g = g.WithDefaults()
	chain := resolveChain(g.Normalizers)
	if chain.bad != "" {
		return Verdict{Reason: "unknown_normalizer=" + chain.bad}
	}
	facts := known[a.Service]

	norm := make([]string, len(a.Leaves))
	distinct := map[string]bool{}
	for i, l := range a.Leaves {
		s, ok := chain.apply(l.Value)
		if !ok {
			continue
		}
		norm[i] = s
		distinct[s] = true
	}
	var v Verdict
	v.Distinct = len(distinct)
	if v.Distinct == 0 {
		v.Reason = "no_candidate_leaves"
		return v
	}
	for s := range distinct {
		if facts[s] {
			v.MatchCount++
		}
	}
	v.Ratio = float64(v.MatchCount) / float64(v.Distinct)
	v.GatePassed = v.MatchCount >= g.MinMatches && v.Ratio >= g.MinRatio
	if !v.GatePassed && !declared {
		v.Reason = fmt.Sprintf("gate_not_met matched=%d distinct=%d ratio=%.3g", v.MatchCount, v.Distinct, v.Ratio)
		return v
	}

	corr := map[int]bool{}
	for i := range a.Leaves {
		if norm[i] != "" && facts[norm[i]] {
			corr[i] = true
			v.Matched = append(v.Matched, a.Leaves[i])
		}
	}
	depth, cov, disc := chooseEntityDepth(a.Leaves, corr, g.MinDiscrimination)
	if depth == 0 {
		v.Reason = "no_entity_level"
		return v
	}
	v.Qualified = true
	v.EntityDepth = depth
	v.Coverage = cov
	v.Disc = disc
	return v
}

// chooseEntityDepth picks the shallowest container level whose corroborated
// leaves are all nested below it (coverage 1.0), that has at least two distinct
// containers, and whose discrimination exceeds minDisc. Returns 0 when none
// qualifies. Ported from linker.chooseEntityDepth (Leaf.Path replaces chain).
func chooseEntityDepth(leaves []Leaf, corroborated map[int]bool, minDisc float64) (depth int, coverage, disc float64) {
	maxLen := 0
	for _, l := range leaves {
		if len(l.Path) > maxLen {
			maxLen = len(l.Path)
		}
	}
	var corrIdx []int
	for i := range leaves {
		if corroborated[i] {
			corrIdx = append(corrIdx, i)
		}
	}
	if len(corrIdx) == 0 {
		return 0, 0, 0
	}
	for d := 1; d < maxLen; d++ {
		containers := map[string]bool{}
		for _, l := range leaves {
			if len(l.Path) > d {
				containers[strings.Join(l.Path[:d], "\x00")] = true
			}
		}
		if len(containers) < 2 {
			continue
		}
		covered := 0
		owners := map[string]bool{}
		for _, i := range corrIdx {
			if len(leaves[i].Path) > d {
				covered++
				owners[strings.Join(leaves[i].Path[:d], "\x00")] = true
			}
		}
		cov := float64(covered) / float64(len(corrIdx))
		dsc := float64(len(owners)) / float64(len(containers))
		if cov == 1.0 && dsc > minDisc {
			return d, cov, dsc
		}
	}
	return 0, 0, 0
}
