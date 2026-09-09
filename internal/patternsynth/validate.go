package patternsynth

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Hit is one match of a pattern on the corpus, identified by site. Two
// patterns matching the same line are two hits but one site — the acceptance
// comparison is over sites, because "did synthesis find this call" is the
// question, not "how many queries fired on it".
type Hit struct {
	Pattern string
	File    string
	Line    int
	Text    string // the source line, for the manual-label sample
}

// Site is the hit's identity in a comparison.
func (h Hit) Site() string { return fmt.Sprintf("%s:%d", h.File, h.Line) }

// Rejection reasons. They are values, not free text, so a report can be
// counted and a test can assert on one.
const (
	RejectCompile   = "query_does_not_compile"
	RejectWildcard  = "unanchored_wildcard_in_argument_list"
	RejectNoHits    = "fires_on_zero_real_sites"
	RejectFanout    = "hits_exceed_fanout_cap"
	RejectNoKey     = "no_extractable_capture"
	RejectPrecision = "manual_label_precision_below_floor"
	RejectUnlabeled = "no_manual_labels_supplied"
)

// Verdict is the gate's decision on one candidate, with everything the report
// needs to explain it.
type Verdict struct {
	Proposal  Proposal
	Accepted  bool
	Rejects   []string
	Hits      []Hit
	Files     int
	Sample    []Hit   // up to sampleSize hits, for manual labelling
	Precision float64 // -1 when no labels covered the sample
	Labeled   int
}

// sampleSize is the plan's "manual-label precision on a sampled 20".
const sampleSize = 20

// Validate compiles the candidate, runs it over the corpus, scores it and
// applies the gate.
//
// The compile step is the candidate's own, not the matcher's: TreeSitterMatcher
// logs a compile failure and silently drops the pattern, which for a gate would
// read as "compiles fine, matches nothing" — a rejection with the wrong reason
// attached.
func Validate(p Proposal, files []CorpusFile, opts Options) (Verdict, error) {
	v := Verdict{Proposal: p, Precision: -1}

	lang := patterns.GrammarFor(opts.Grammar())
	if lang == nil {
		return v, fmt.Errorf("validate: no tree-sitter grammar for %q", opts.Grammar())
	}
	q, err := sitter.NewQuery([]byte(p.Pattern.Query), lang)
	if err != nil {
		v.Rejects = append(v.Rejects, RejectCompile+": "+err.Error())
		return v, nil
	}
	q.Close()

	if where, bad := UnanchoredWildcard(p.Pattern.Query); bad {
		// A bare `_` binds anonymous tokens too, so `(argument_list _ (string))`
		// matches the `(` and shifts every argument left. The candidate would
		// still compile and still match — wrongly.
		v.Rejects = append(v.Rejects, fmt.Sprintf("%s: %s", RejectWildcard, where))
	}
	if !hasKeyCapture(p.Pattern) {
		// A pattern with no literal or callable capture records that a call
		// happened and nothing about it; it costs review budget and inflates
		// the pattern count for no graph.
		v.Rejects = append(v.Rejects, RejectNoKey)
	}

	hits, err := RunPattern(p.Pattern, files, opts)
	if err != nil {
		return v, err
	}
	v.Hits = hits
	v.Files = distinctFiles(hits)
	v.Sample = sampleHits(hits, files)

	switch {
	case len(hits) == 0:
		v.Rejects = append(v.Rejects, RejectNoHits)
	case opts.FanoutCap > 0 && len(hits) > opts.FanoutCap:
		v.Rejects = append(v.Rejects,
			fmt.Sprintf("%s: %d hits > cap %d", RejectFanout, len(hits), opts.FanoutCap))
	}

	if opts.MinPrecision > 0 {
		v.Precision, v.Labeled = score(v.Sample, opts.Labels)
		switch {
		case v.Labeled == 0:
			v.Rejects = append(v.Rejects, RejectUnlabeled)
		case v.Precision < opts.MinPrecision:
			v.Rejects = append(v.Rejects, fmt.Sprintf("%s: %.2f < %.2f",
				RejectPrecision, v.Precision, opts.MinPrecision))
		}
	}

	v.Accepted = len(v.Rejects) == 0
	return v, nil
}

// RunPattern matches one pattern over the corpus through the real matcher, so a
// candidate is scored by the same code path that will run it in production —
// predicates, capture handling and all.
func RunPattern(p patterns.Pattern, files []CorpusFile, opts Options) ([]Hit, error) {
	reg := patterns.NewRegistry()
	reg.RegisterFile(&patterns.PatternFile{Language: opts.Language, Patterns: []patterns.Pattern{p}})
	return runRegistry(reg, files, opts)
}

// RunFile matches a whole pattern file over the corpus — the baseline side of
// the acceptance comparison.
func RunFile(pf *patterns.PatternFile, files []CorpusFile, opts Options) ([]Hit, error) {
	reg := patterns.NewRegistry()
	// Copy: RegisterFile stamps the file-level gate onto each pattern and
	// hands the registry pointers into the slice.
	cp := *pf
	cp.Patterns = append([]patterns.Pattern(nil), pf.Patterns...)
	// The registry is keyed by language; register under the run's language so
	// the lookup in runRegistry cannot miss on a file whose `language:` field
	// spells the same grammar differently.
	cp.Language = opts.Language
	reg.RegisterFile(&cp)
	return runRegistry(reg, files, opts)
}

func runRegistry(reg *patterns.Registry, files []CorpusFile, opts Options) ([]Hit, error) {
	m := patterns.NewTreeSitterMatcher(reg)
	var hits []Hit
	for _, f := range files {
		results, err := m.MatchWithGrammar(opts.Language, opts.Grammar(), f.Path, f.Src)
		if err != nil {
			return nil, fmt.Errorf("match %s: %w", f.Path, err)
		}
		for _, r := range results {
			hits = append(hits, Hit{Pattern: r.PatternName, File: f.Path, Line: r.Line, Text: sourceLine(f.Src, r.Line)})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].File != hits[j].File {
			return hits[i].File < hits[j].File
		}
		if hits[i].Line != hits[j].Line {
			return hits[i].Line < hits[j].Line
		}
		return hits[i].Pattern < hits[j].Pattern
	})
	return hits, nil
}

// keyBearingNode reports whether a tree-sitter node type can carry a key a
// linker could join on: a string literal (a path, a topic, a table name) or a
// callable (a handler). Substring matching keeps it language-agnostic —
// `interpreted_string_literal` in Go, `string` in Ruby, `func_literal` in Go,
// `arrow_function` in JS.
func keyBearingNode(nodeType string) bool {
	for _, s := range []string{"string", "func_literal", "function", "lambda", "arrow"} {
		if strings.Contains(nodeType, s) {
			return true
		}
	}
	return false
}

// hasKeyCapture reports whether the candidate captures at least one string
// literal or callable.
//
// It reads the query rather than the inferred capture roles on purpose: roles
// are a guess the proposer made, and a gate that trusts the proposer's own
// labelling is not a gate. A call whose captured arguments are neither a
// literal nor a callable records that the call happened and nothing about it —
// it yields no joinable key, so it costs review budget and inflates the
// pattern count for no graph.
func hasKeyCapture(p patterns.Pattern) bool {
	toks := tokenizeQuery(p.Query)
	for i, t := range toks {
		if t != "@" || i == 0 {
			continue
		}
		// `(node_type) @name`: walk back over the closing paren to the type.
		if toks[i-1] != ")" || i < 3 {
			continue
		}
		if keyBearingNode(toks[i-2]) {
			return true
		}
	}
	return false
}

func distinctFiles(hits []Hit) int {
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.File] = true
	}
	return len(seen)
}

// sampleHits spreads the manual-label sample across the corpus instead of
// taking the first 20: the first 20 hits of a large corpus are usually all in
// one file, and a precision number from one file is a number about that file.
func sampleHits(hits []Hit, _ []CorpusFile) []Hit {
	if len(hits) <= sampleSize {
		return append([]Hit(nil), hits...)
	}
	out := make([]Hit, 0, sampleSize)
	step := float64(len(hits)) / float64(sampleSize)
	for i := 0; i < sampleSize; i++ {
		out = append(out, hits[int(float64(i)*step)])
	}
	return out
}

// score computes precision over the labelled part of the sample. Labels are
// supplied by a human (or by whatever review process the caller trusts) keyed
// by "<pattern>@<file>:<line>" — the tool prepares the sample and consumes the
// verdicts; it cannot invent them.
func score(sample []Hit, labels map[string]bool) (float64, int) {
	labeled, correct := 0, 0
	for _, h := range sample {
		v, ok := labels[LabelKey(h)]
		if !ok {
			continue
		}
		labeled++
		if v {
			correct++
		}
	}
	if labeled == 0 {
		return -1, 0
	}
	return float64(correct) / float64(labeled), labeled
}

// LabelKey is the key a manual label file uses for a hit.
func LabelKey(h Hit) string { return h.Pattern + "@" + h.Site() }

func sourceLine(src []byte, line int) string {
	if line <= 0 {
		return ""
	}
	lines := strings.Split(string(src), "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// UnanchoredWildcard reports a bare `_` inside an `argument_list` node that has
// no adjacent anchor, returning a human-readable location.
//
// This is the trap the plan requires the validator to check explicitly: a bare
// `_` (unlike `(_)`) matches anonymous token nodes, so `(argument_list _
// (string))` binds the opening parenthesis and every argument after it reads
// one position to the left. The query compiles and the mistake surfaces as
// silently wrong captures.
func UnanchoredWildcard(query string) (string, bool) {
	toks := tokenizeQuery(query)
	depth, argDepths := 0, map[int]bool{}
	for i, t := range toks {
		switch t {
		case "(":
			depth++
			// The node type follows its own paren, so `(argument_list` marks
			// depth as an argument list's body.
			if i+1 < len(toks) && toks[i+1] == "argument_list" {
				argDepths[depth] = true
			}
			continue
		case ")":
			delete(argDepths, depth)
			depth--
			continue
		case "_":
			if !argDepths[depth] {
				continue
			}
			if i == 0 || toks[i-1] == "(" { // `(_)` is the safe, named-node form
				continue
			}
			prevAnchor := toks[i-1] == "."
			nextAnchor := i+1 < len(toks) && toks[i+1] == "."
			if !prevAnchor && !nextAnchor {
				return fmt.Sprintf("bare `_` at token %d inside argument_list", i), true
			}
		}
	}
	return "", false
}

// tokenizeQuery splits a tree-sitter query into punctuation, string literals
// and words. Strings and comments are kept whole so a `_` inside them is never
// mistaken for a wildcard, and `@_body`-style capture names tokenize as one
// word rather than an anchor plus a wildcard.
func tokenizeQuery(q string) []string {
	var toks []string
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == ';': // comment to end of line
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case c == '"':
			j := i + 1
			for j < len(q) && q[j] != '"' {
				if q[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(q) {
				j++
			}
			toks = append(toks, q[i:j])
			i = j
		case strings.IndexByte("()[]@.#!?*+", c) >= 0:
			toks = append(toks, string(c))
			i++
		default:
			j := i
			for j < len(q) && (isWordByte(q[j])) {
				j++
			}
			if j == i {
				j++ // unknown byte: emit it alone rather than loop forever
			}
			toks = append(toks, q[i:j])
			i = j
		}
	}
	return toks
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
