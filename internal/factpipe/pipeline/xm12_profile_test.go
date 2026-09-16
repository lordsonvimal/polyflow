package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// grammarForExt mirrors internal/indexer/factpipe_pass.go's patternLangForFile
// closely enough for profiling purposes: jsx/tsx files need their own
// tree-sitter grammar, everything else defaults to lang.
func grammarForExt(path, lang string) string {
	switch filepath.Ext(path) {
	case ".jsx":
		return "tsx"
	case ".tsx":
		return "tsx"
	case ".ts":
		return "typescript"
	default:
		return lang
	}
}

// profileFrameworkOnCorpus generalizes profileRailsFiltersOnCorpus (same
// file, orion_debug_test.go) to any single embedded framework/language, so
// XM.11's follow-up ("which of cedar's new top frameworks is precededBy-
// shaped, and which isn't") can be answered per-framework instead of just for
// rails_filters. Not a permanent measurement — no assertions, CPU+heap
// profiles only, deleted-in-spirit once the answer's written up in
// docs/factpipe-cross-framework-matching-plan.md (XM.12).
func profileFrameworkOnCorpus(t *testing.T, db, root, lang, fwName, tag string) {
	t.Helper()
	store, err := graph.NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	svc := ""
	snap := graph.Snapshot{}
	for _, n := range idx.Nodes {
		if n.Language == lang {
			snap.Nodes = append(snap.Nodes, *n)
			svc = n.Service
		}
	}
	inSvc := map[string]bool{}
	for _, n := range snap.Nodes {
		inSvc[n.ID] = true
	}
	for _, e := range idx.AllEdges() {
		if inSvc[e.From] {
			ec := e
			snap.Edges = append(snap.Edges, ec)
		}
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].ID < snap.Nodes[j].ID })
	sort.Slice(snap.Edges, func(i, j int) bool { return snap.Edges[i].ID < snap.Edges[j].ID })
	t.Logf("svc=%s nodes=%d edges=%d", svc, len(snap.Nodes), len(snap.Edges))

	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var fw *pipeline.Framework
	for _, f := range reg.All() {
		if f.Name == fwName {
			fw = f
		}
	}
	if fw == nil {
		t.Fatalf("%s not found", fwName)
	}

	var files []pipeline.ParsedFile
	seen := map[string]bool{}
	for _, n := range snap.Nodes {
		if n.File == "" || seen[n.File] {
			continue
		}
		seen[n.File] = true
		src, err := os.ReadFile(filepath.Join(root, n.File))
		if err != nil {
			continue
		}
		files = append(files, pipeline.ParsedFile{Path: n.File, Language: lang, Grammar: grammarForExt(n.File, lang), Src: src})
	}
	t.Logf("%s files=%d", lang, len(files))

	cpuF, err := os.Create("/tmp/xm12_" + tag + "_" + fwName + "_cpu.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer cpuF.Close()
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		t.Fatal(err)
	}

	const reps = 2
	var res pipeline.Result
	for i := 0; i < reps; i++ {
		repStart := time.Now()
		res, err = pipeline.Run([]*pipeline.Framework{fw}, files, snap)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("rep %d: %s (edges=%d)", i, time.Since(repStart), len(res.Edges))
	}
	pprof.StopCPUProfile()
	t.Logf("emitted edges=%d ledger=%d (per run, last of %d reps)", len(res.Edges), len(res.Ledger), reps)

	heapF, err := os.Create("/tmp/xm12_" + tag + "_" + fwName + "_heap.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer heapF.Close()
	if err := pprof.WriteHeapProfile(heapF); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote /tmp/xm12_%s_%s_{cpu,heap}.pprof", tag, fwName)
}

// TestCedarFrameworkProfile profiles one of cedar's post-XM.11 top-5
// frameworks by wall time (js_mobx, js_client_routes, ruby_http_hosts,
// rails_filters, js_hoc) individually, so a fix like XM.12's precededBy
// batching can be evaluated against the frameworks that actually dominate
// cedar's real reindex cost now, not just rails_filters.
//
//	PF_CEDAR_DB=$HOME/Projects/mdr/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestCedarFrameworkProfile -v
func TestCedarFrameworkProfile(t *testing.T) {
	db := os.Getenv("PF_CEDAR_DB")
	if db == "" {
		t.Skip("PF_CEDAR_DB unset")
	}
	root := filepath.Join(os.Getenv("HOME"), "Projects/mdr")

	cases := []struct {
		lang, name string
	}{
		{"javascript", "js_mobx"},
		{"javascript", "js_client_routes"},
		{"ruby", "ruby_http_hosts"},
		{"ruby", "rails_filters"},
		{"javascript", "js_hoc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profileFrameworkOnCorpus(t, db, root, c.lang, c.name, "cedar")
		})
	}
}
