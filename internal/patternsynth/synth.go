// Package patternsynth is Tier PS: it synthesizes L1 code patterns
// (patterns/<lang>/*.yaml) from a corpus instead of hand-authoring them.
//
// The pipeline is sample → propose → validate → gate → render. Only the
// proposer is pluggable; everything else is fixed, and deliberately so. A
// proposer — template today, an LLM tomorrow — is untrusted: it may emit a
// query that does not compile, one that matches nothing, or one that matches
// half the corpus, and the gate rejects all three without knowing or caring
// which proposer produced it.
//
// The gate is the tier. Hand-authoring long-tail patterns is uneconomic
// because reviewing them is, and a synthesizer that produces candidates faster
// than review can absorb them has moved the cost rather than removed it. Hence
// the two rejections that look optional and are not: a query with an unanchored
// bare `_` inside an argument_list (which compiles, matches, and silently binds
// the wrong arguments), and a pattern that fires on zero real sites.
package patternsynth

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Options configures one synthesis run.
type Options struct {
	Package  string // import path the patterns are attributed to
	Corpus   string // directory sampled for call sites
	Language string // pattern language; "go" today
	Name     string // output slug; derived from Package when empty

	// FanoutCap rejects a candidate that matches more than this many sites.
	// Zero disables the check.
	FanoutCap int

	// MinPrecision, when > 0, requires manual labels covering the sampled
	// hits and rejects a candidate scoring below it. Off by default: the tool
	// cannot label its own output, so a floor with no labels supplied would
	// reject every candidate.
	MinPrecision float64
	Labels       map[string]bool

	Proposer Proposer
}

// Grammar is the tree-sitter grammar the run parses with. Pattern language and
// grammar are separate concepts in this codebase (javascript patterns run
// against the tsx grammar); they coincide for Go.
func (o Options) Grammar() string { return o.Language }

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug is the output file's base name and the prefix of every pattern name in
// it.
func (o Options) Slug() string {
	s := o.Name
	if s == "" {
		s = goImportIdent(o.Package)
	}
	s = slugUnsafe.ReplaceAllString(strings.ToLower(s), "_")
	return strings.Trim(s, "_")
}

// Result is one synthesis run: every verdict (accepted and rejected, because a
// rejection is the reviewable part) plus the file the survivors render to.
type Result struct {
	Options   Options
	CorpusSHA string
	Files     []CorpusFile
	Clusters  []Cluster
	Verdicts  []Verdict
	YAML      string // rendered survivors; empty when none survived
}

// Accepted returns the surviving verdicts, in output order.
func (r Result) Accepted() []Verdict {
	var out []Verdict
	for _, v := range r.Verdicts {
		if v.Accepted {
			out = append(out, v)
		}
	}
	return out
}

// Run executes the whole pipeline. It never writes: the caller decides where
// the YAML goes, which keeps the pipeline testable without a filesystem and
// keeps `--dry-run` from being a second code path.
func Run(opts Options) (Result, error) {
	if opts.Package == "" {
		return Result{}, fmt.Errorf("synth: --package is required")
	}
	if opts.Corpus == "" {
		return Result{}, fmt.Errorf("synth: --corpus is required")
	}
	if opts.Language == "" {
		opts.Language = "go"
	}
	if opts.Proposer == nil {
		opts.Proposer = TemplateProposer{}
	}

	clusters, files, err := Sample(opts)
	if err != nil {
		return Result{}, err
	}
	res := Result{Options: opts, Files: files, Clusters: clusters, CorpusSHA: CorpusSHA(files)}

	// Two clusters routinely reduce to the same name — the same dominant
	// method registered with three different handler shapes is one name and
	// three patterns. They are disambiguated by suffix, not dropped: on gotify
	// the three `GET` clusters hold 8, 40 and 1 sites, and dropping the
	// collisions would have cost the largest one. Clusters arrive in Key
	// order, so the suffix a cluster gets is the same on every run.
	taken := map[string]int{}
	for _, c := range clusters {
		props, err := opts.Proposer.Propose(c, opts)
		if err != nil {
			return res, fmt.Errorf("propose %s: %w", c.Key(), err)
		}
		for i := range props {
			v, err := Validate(props[i], files, opts)
			if err != nil {
				return res, err
			}
			// Only survivors consume a name: a rejected candidate is never
			// written, and letting it reserve `gin_use` would leave the file
			// holding a lone `gin_use_2`.
			if v.Accepted {
				rename(&v, disambiguate(v.Proposal.Pattern.Name, taken))
			}
			res.Verdicts = append(res.Verdicts, v)
		}
	}
	sort.SliceStable(res.Verdicts, func(i, j int) bool {
		return res.Verdicts[i].Proposal.Pattern.Name < res.Verdicts[j].Proposal.Pattern.Name
	})

	accepted := res.Accepted()
	if len(accepted) > 0 {
		pats := make([]patterns.Pattern, 0, len(accepted))
		for _, v := range accepted {
			pats = append(pats, v.Proposal.Pattern)
		}
		res.YAML = Render(pats, opts, res.CorpusSHA)
	}
	return res, nil
}

// Comparison is the acceptance check: the synthesized patterns' hit sites
// against a hand-written file's, over the same corpus. If synthesis cannot
// reproduce a pattern that already works, it will not discover ones that do
// not exist yet.
type Comparison struct {
	Baseline  string   // path of the hand-written file
	BaseSites []string // sorted "file:line"
	SynthHits []string
	Missing   []string // baseline sites the synthesis did not cover
	Extra     []string // sites only the synthesis matched
	ExtraFrac float64  // len(Extra) / len(BaseSites)
}

// Superset reports whether every baseline site is covered.
func (c Comparison) Superset() bool { return len(c.Missing) == 0 }

// Compare runs the hand-written baseline and the run's survivors over the same
// corpus and diffs their hit sites.
func Compare(res Result, baselinePath string) (Comparison, error) {
	pf, err := patterns.LoadFile(baselinePath)
	if err != nil {
		return Comparison{}, fmt.Errorf("load baseline %s: %w", baselinePath, err)
	}
	baseHits, err := RunFile(pf, res.Files, res.Options)
	if err != nil {
		return Comparison{}, err
	}
	base := siteSet(baseHits)

	synth := map[string]bool{}
	for _, v := range res.Accepted() {
		for _, h := range v.Hits {
			synth[h.Site()] = true
		}
	}

	c := Comparison{Baseline: baselinePath, BaseSites: sortedKeys(base), SynthHits: sortedKeys(synth)}
	for _, s := range c.BaseSites {
		if !synth[s] {
			c.Missing = append(c.Missing, s)
		}
	}
	for _, s := range c.SynthHits {
		if !base[s] {
			c.Extra = append(c.Extra, s)
		}
	}
	if len(base) > 0 {
		c.ExtraFrac = float64(len(c.Extra)) / float64(len(base))
	}
	return c, nil
}

// rename applies a disambiguated name to the verdict and everything that
// quotes it — the hits carry the pattern name, and a label key that names a
// pattern the output file does not contain is unusable.
func rename(v *Verdict, name string) {
	old := v.Proposal.Pattern.Name
	if old == name {
		return
	}
	v.Proposal.Pattern.Name = name
	for i := range v.Hits {
		if v.Hits[i].Pattern == old {
			v.Hits[i].Pattern = name
		}
	}
	for i := range v.Sample {
		if v.Sample[i].Pattern == old {
			v.Sample[i].Pattern = name
		}
	}
}

// disambiguate returns name, or name with the lowest free numeric suffix. The
// prefix is preserved because pattern *names* select the node type
// (classifyPattern is name-driven), so `gin_get_route_2` must stay a route.
func disambiguate(name string, taken map[string]int) string {
	if taken[name] == 0 {
		taken[name] = 1
		return name
	}
	for n := taken[name] + 1; ; n++ {
		cand := fmt.Sprintf("%s_%d", name, n)
		if taken[cand] == 0 {
			taken[name] = n
			taken[cand] = 1
			return cand
		}
	}
}

func siteSet(hits []Hit) map[string]bool {
	out := map[string]bool{}
	for _, h := range hits {
		out[h.Site()] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
