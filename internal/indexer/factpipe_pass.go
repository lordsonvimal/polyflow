package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// runFactpipeFrameworks is the Tier FX (declarative framework pipeline) link
// pass. For each service it gates the embedded framework registry to the
// frameworks that service depends on, re-parses that service's source files of
// the relevant language, and runs extract → bridge → derive → emit
// (internal/factpipe/pipeline.Run). The resulting edges + blind-spot ledger
// rows are written exactly as the hand-written link passes wrote theirs.
//
// FX.7 migrated gin_middleware + express_middleware here; the per-framework Go
// files (internal/linker/{gin,express}_middleware.go) are gone.
func runFactpipeFrameworks(st *linkPipelineState) error {
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("factpipe: load registry: %w", err)
	}
	if len(reg.All()) == 0 {
		return nil
	}

	// XM.4 (docs/factpipe-cross-framework-matching-plan.md): PF_FACTPIPE_PROFILE
	// threads a *pipeline.RunStats through every Run call and accumulates it
	// across every service in this index run, then prints the phase totals +
	// top-5-by-wall-time frameworks at the end. Disabled by default (nil
	// *RunStats), same "caller must opt in" cost discipline PF_INDEX_TIMING
	// already uses — production reindexing pays nothing for this.
	profiling := os.Getenv("PF_FACTPIPE_PROFILE") != ""
	var totalStats pipeline.RunStats
	perFwTotal := map[string]time.Duration{}

	svcOf := make(map[string]string, len(st.enrichedNodes))
	for i := range st.enrichedNodes {
		svcOf[st.enrichedNodes[i].ID] = st.enrichedNodes[i].Service
	}

	for _, sf := range st.allSvcFiles {
		active := reg.Active(sf.deps)
		if len(active) == 0 {
			continue
		}
		langs := map[string]bool{}
		for _, fw := range active {
			langs[fw.Language] = true
		}

		files := parsedFilesForLanguages(sf.files, langs)
		if len(files) == 0 {
			continue
		}

		// Test-harness nodes (a spec's DummyController) are excluded, matching
		// the hand-written link passes' n.Meta[graph.MetaIsTest] skip.
		// Files is the whole service tree (asset resolution needs vendored +
		// declare-nothing files), not just the parsed subset.
		absSvcPath, err := filepath.Abs(sf.svc.Path)
		if err != nil {
			absSvcPath = sf.svc.Path
		}
		snap := graph.Snapshot{Files: sf.files, ServicePath: absSvcPath}
		realNode := make(map[string]bool)
		for i := range st.enrichedNodes {
			n := &st.enrichedNodes[i]
			if n.Service != sf.svc.Name || n.Meta[graph.MetaIsTest] == "true" || graph.IsTestFilePath(n.File) {
				continue
			}
			snap.Nodes = append(snap.Nodes, *n)
			realNode[n.ID] = true
		}
		for i := range st.allEdges {
			if realNode[st.allEdges[i].From] {
				snap.Edges = append(snap.Edges, st.allEdges[i])
			}
		}
		if len(snap.Nodes) == 0 {
			continue
		}

		var runStats *pipeline.RunStats
		if profiling {
			runStats = &pipeline.RunStats{}
		}
		res, err := pipeline.Run(active, files, snap, runStats)
		if err != nil {
			return fmt.Errorf("factpipe: service %s: %w", sf.svc.Name, err)
		}
		if profiling {
			totalStats.Bridge += runStats.Bridge
			totalStats.Extract += runStats.Extract
			totalStats.Derive += runStats.Derive
			totalStats.Emit += runStats.Emit
			for name, d := range runStats.PerFramework {
				perFwTotal[name] += d
			}
		}
		for i := range res.Nodes {
			n := res.Nodes[i]
			if svcOf[n.ID] != "" {
				continue // already a real node (containment reached it); mint is a gap-fill only
			}
			if err := st.bw.AddNode(st.ctx, &n); err != nil {
				return err
			}
			st.allNodes = append(st.allNodes, n)
			st.enrichedNodes = append(st.enrichedNodes, n)
			svcOf[n.ID] = n.Service
		}
		if err := st.bw.Flush(st.ctx); err != nil {
			return err
		}
		if err := st.writeEdges(res.Edges); err != nil {
			return err
		}
		st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
		st.allUnresolved = append(st.allUnresolved, res.Ledger...)
	}
	if profiling {
		printFactpipeProfile(totalStats, perFwTotal)
	}
	return nil
}

// printFactpipeProfile is XM.4's wall-time lens: phase totals summed across
// every service's pipeline.Run call in this index invocation, plus the top 5
// frameworks by wall time (extract-lowering + derive + emit) — the Pareto
// question of whether cost concentrates in a handful of frameworks or spreads
// evenly across the ~25-33 active ones.
func printFactpipeProfile(totalStats pipeline.RunStats, perFwTotal map[string]time.Duration) {
	fmt.Fprintf(os.Stderr, "  ⏱  factpipe_frameworks phase breakdown (PF_FACTPIPE_PROFILE):\n")
	fmt.Fprintf(os.Stderr, "      bridge  %8.2fs\n", totalStats.Bridge.Seconds())
	fmt.Fprintf(os.Stderr, "      extract %8.2fs\n", totalStats.Extract.Seconds())
	fmt.Fprintf(os.Stderr, "      derive  %8.2fs\n", totalStats.Derive.Seconds())
	fmt.Fprintf(os.Stderr, "      emit    %8.2fs\n", totalStats.Emit.Seconds())

	type fwTime struct {
		name string
		d    time.Duration
	}
	ranked := make([]fwTime, 0, len(perFwTotal))
	for name, d := range perFwTotal {
		ranked = append(ranked, fwTime{name, d})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].d > ranked[j].d })
	n := 5
	if len(ranked) < n {
		n = len(ranked)
	}
	fmt.Fprintf(os.Stderr, "      top %d frameworks by wall time:\n", n)
	for _, fw := range ranked[:n] {
		fmt.Fprintf(os.Stderr, "        %-28s %8.2fs\n", fw.name, fw.d.Seconds())
	}
}

// parsedFilesForLanguages reads the subset of paths whose extension maps to one
// of langs and pairs each with its pattern language + tree-sitter grammar. The
// path is kept as-is (walkService already yields it service-relative, matching
// graph.Node.File and the extract `file` verb).
func parsedFilesForLanguages(paths []string, langs map[string]bool) []pipeline.ParsedFile {
	var out []pipeline.ParsedFile
	for _, p := range paths {
		lang, grammar := patternLangForFile(p)
		if lang == "" || !langs[lang] {
			continue
		}
		// Match the hand-written link passes, which skip nodes with
		// graph.MetaIsTest: a spec's DummyController is not a real registration.
		if graph.IsTestFilePath(p) {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, pipeline.ParsedFile{Path: p, Language: lang, Grammar: grammar, Src: src})
	}
	return out
}

// patternLangForFile maps a source file extension to (pattern language,
// tree-sitter grammar). Only the languages the embedded frameworks target are
// recognised; everything else returns "".
func patternLangForFile(path string) (lang, grammar string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go", "go"
	case ".js", ".mjs", ".cjs":
		return "javascript", "javascript"
	case ".jsx":
		return "javascript", "tsx"
	case ".ts":
		return "javascript", "typescript"
	case ".tsx":
		return "javascript", "tsx"
	case ".rb", ".rake":
		return "ruby", "ruby"
	case ".erb":
		return "erb", "erb"
	}
	return "", ""
}
