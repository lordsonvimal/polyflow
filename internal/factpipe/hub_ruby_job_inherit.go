package factpipe

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_ruby_job_inherit.go is the "ruby_job_inherit" hub provider (see hub.go)
// — the Tier FX FX.8.15 migration of internal/linker/ruby_job_inherit.go's
// PromoteInheritedJobPerform.
//
// FX.8.15's original 2026-09-15 blocker diagnosis was structural, not
// algorithmic: the closure walk itself (hopsToRoot's bounded BFS) was already
// provably buildable from ancestor_dist (see rules/ruby/ruby_job_inherit.dl),
// but the pass also mutates the node set in place — replacing a candidate's
// ID with a promoted subscriber's, deleting every un-promoted candidate — and
// emit.go had no verb for either. FX.8.15's rescoping (2026-09-15) added
// exactly those two verbs (`replace:`/`delete:`, internal/factpipe/emit.go)
// and kept this framework in its own dedicated early pipeline.Run call
// (internal/indexer/link_passes.go, same slot the retired Go pass ran in,
// still ahead of `containment`) rather than folding it into the shared
// `factpipe_frameworks` slot — so no scheduling change was needed for the
// ~20 other active frameworks.
//
// This hub's job is classification only — every piece of jobInheritIndex
// that does NOT require walking `inherits` edges (a HubProvider gets `nodes`
// and `files`, not edges; the edge walk is `ancestor_dist`, already a base
// fact by the time `.dl` rules run). That turns out to be almost the whole
// retired index: classNodeFor (candidate -> its own class, by file+line+name)
// and performEdge (seed/candidate -> its `perform` method, by qualified name
// then nearest-in-file fallback) are both pure node-position lookups, ported
// near-verbatim below. Only hopsToRoot's superIDs walk is gone, replaced by
// `ancestor_dist`/`job_root`/`le(D,4)` in the `.dl`.
func init() { RegisterHub("ruby_job_inherit", rubyJobInheritHub) }

// Predicates this hub asserts:
//
//	job_seed(NodeID, JobClass)                the direct-match subscriber, root-confirmed already
//	job_seed_perform(NodeID, PerformID)        seed's resolved `perform` method target
//	job_candidate(NodeID, ClassID, JobClass)   a class-resolved, non-redundant candidate
//	job_candidate_perform(NodeID, PerformID)   candidate's resolved `perform` method target
//	job_drop_immediate(NodeID)                 redundant (seed already covers this position) or class-unresolved
const (
	jobSeedPred             = "job_seed"
	jobSeedPerformPred      = "job_seed_perform"
	jobCandidatePred        = "job_candidate"
	jobCandidatePerformPred = "job_candidate_perform"
	jobDropImmediatePred    = "job_drop_immediate"
)

func rubyJobInheritHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint) []Fact {
	ix := newJobInheritHubIndex(nodes)
	if len(ix.candidates) == 0 && len(ix.seeds) == 0 {
		return nil
	}

	var out []Fact
	for _, i := range ix.seeds {
		n := &nodes[i]
		jobClass := n.Meta["job_class"]
		origin := Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "ruby_job_inherit"}
		out = append(out, Fact{Pred: jobSeedPred, Args: []Atom{Node(n.ID), Str(jobClass)}, Origin: origin})
		if to, ok := ix.performTarget(n, jobClass); ok {
			out = append(out, Fact{Pred: jobSeedPerformPred, Args: []Atom{Node(n.ID), Node(to)}, Origin: origin})
		}
	}

	for _, i := range ix.candidates {
		cand := &nodes[i]
		jobClass := cand.Meta["job_class"]
		origin := Origin{Kind: OriginPrimitive, File: cand.File, Line: cand.Line, Pattern: "ruby_job_inherit"}

		if ix.seedAt(cand.Service, cand.File, cand.Line) {
			out = append(out, Fact{Pred: jobDropImmediatePred, Args: []Atom{Node(cand.ID)}, Origin: origin})
			continue
		}
		classID, ok := ix.classNodeFor(cand, jobClass)
		if !ok {
			out = append(out, Fact{Pred: jobDropImmediatePred, Args: []Atom{Node(cand.ID)}, Origin: origin})
			continue
		}
		out = append(out, Fact{Pred: jobCandidatePred, Args: []Atom{Node(cand.ID), Node(classID), Str(jobClass)}, Origin: origin})
		if to, ok := ix.performTarget(cand, jobClass); ok {
			out = append(out, Fact{Pred: jobCandidatePerformPred, Args: []Atom{Node(cand.ID), Node(to)}, Origin: origin})
		}
	}
	return out
}

// jobInheritHubIndex is the read-only, edge-free view of the graph this hub
// classifies — the node-position half of the retired jobInheritIndex
// (candidates/seeds/classesInFile/performByClass/performInFile/seedPos),
// ported verbatim. The edge-walk half (superIDs/hopsToRoot) has no
// counterpart here: it is `ancestor_dist`, a base fact the `.dl` reads
// directly.
type jobInheritHubIndex struct {
	nodes []graph.Node

	candidates []int
	seeds      []int

	seedPos map[string]bool

	classesInFile map[string][]jobClassAtLine

	performByClass map[string]map[string]string
	performInFile  map[string][]jobClassAtLine
}

type jobClassAtLine struct {
	id   string
	name string
	line int
}

func jobHubPosKey(service, file string, line int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", service, file, line)
}

func jobHubFileKey(service, file string) string {
	return service + "\x00" + file
}

func newJobInheritHubIndex(nodes []graph.Node) *jobInheritHubIndex {
	ix := &jobInheritHubIndex{
		nodes:          nodes,
		seedPos:        map[string]bool{},
		classesInFile:  map[string][]jobClassAtLine{},
		performByClass: map[string]map[string]string{},
		performInFile:  map[string][]jobClassAtLine{},
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Language != "ruby" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeSubscriber:
			if n.Meta["pattern"] == "aj_perform_method" {
				ix.seeds = append(ix.seeds, i)
				ix.seedPos[jobHubPosKey(n.Service, n.File, n.Line)] = true
			}
		case graph.NodeTypeVariable:
			if n.Meta["pattern"] == "aj_perform_method_candidate" {
				ix.candidates = append(ix.candidates, i)
			}
		case graph.NodeTypeFunction:
			if n.Label == "perform" && jobHubIsRubyFile(n.File) {
				k := jobHubFileKey(n.Service, n.File)
				if qn := n.Meta["qualified_name"]; qn != "" {
					if ix.performByClass[k] == nil {
						ix.performByClass[k] = map[string]string{}
					}
					ix.performByClass[k][qn] = n.ID
				}
				ix.performInFile[k] = append(ix.performInFile[k], jobClassAtLine{id: n.ID, name: n.Label, line: n.Line})
			}
		case graph.NodeTypeClass:
			k := jobHubFileKey(n.Service, n.File)
			ix.classesInFile[k] = append(ix.classesInFile[k], jobClassAtLine{id: n.ID, name: n.Label, line: n.Line})
		}
	}

	for k := range ix.classesInFile {
		sortJobClassAtLine(ix.classesInFile[k])
	}
	for k := range ix.performInFile {
		sortJobClassAtLine(ix.performInFile[k])
	}
	return ix
}

func jobHubIsRubyFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".rb" || ext == ".rake"
}

func sortJobClassAtLine(s []jobClassAtLine) {
	sort.Slice(s, func(i, j int) bool { return s[i].line < s[j].line })
}

func (ix *jobInheritHubIndex) seedAt(service, file string, line int) bool {
	return ix.seedPos[jobHubPosKey(service, file, line)]
}

// classNodeFor resolves the class node a candidate was matched inside, ported
// verbatim from the retired jobInheritIndex.classNodeFor.
func (ix *jobInheritHubIndex) classNodeFor(cand *graph.Node, jobClass string) (string, bool) {
	inFile := ix.classesInFile[jobHubFileKey(cand.Service, cand.File)]
	for _, c := range inFile {
		if c.line == cand.Line && c.name == jobClass {
			return c.id, true
		}
	}
	best := ""
	for _, c := range inFile {
		if c.name == jobClass && c.line <= cand.Line {
			best = c.id
		}
	}
	return best, best != ""
}

// performTarget resolves a seed or candidate's `perform` method node, ported
// verbatim from the retired jobInheritIndex.performEdge (minus edge
// construction, which the `.dl`/emit side now does).
func (ix *jobInheritHubIndex) performTarget(sub *graph.Node, jobClass string) (string, bool) {
	to := ""
	if jobClass != "" {
		short := jobClass
		if i := strings.LastIndex(jobClass, "::"); i >= 0 {
			short = jobClass[i+2:]
		}
		byClass := ix.performByClass[jobHubFileKey(sub.Service, sub.File)]
		for _, key := range []string{jobClass + "#perform", short + "#perform"} {
			if id, ok := byClass[key]; ok {
				to = id
				break
			}
		}
	}
	if to == "" {
		for _, p := range ix.performInFile[jobHubFileKey(sub.Service, sub.File)] {
			if p.line >= sub.Line {
				to = p.id
				break
			}
		}
	}
	if to == "" || to == sub.ID {
		return "", false
	}
	return to, true
}
