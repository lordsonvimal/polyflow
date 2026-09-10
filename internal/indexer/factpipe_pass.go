package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		snap := graph.Snapshot{Files: sf.files}
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

		res, err := pipeline.Run(active, files, snap)
		if err != nil {
			return fmt.Errorf("factpipe: service %s: %w", sf.svc.Name, err)
		}
		if err := st.writeEdges(res.Edges); err != nil {
			return err
		}
		st.allUnresolved = append(st.allUnresolved, res.Unresolved...)
		st.allUnresolved = append(st.allUnresolved, res.Ledger...)
	}
	return nil
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
	}
	return "", ""
}
