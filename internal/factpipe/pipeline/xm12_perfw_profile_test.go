package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/factpipe"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// profileFrameworkLoopOnly is XM.12's corrected lens: XM12ProfileTest (the
// pipeline_test-package version) profiles a lone framework's Run() call,
// which re-runs parseFileRoots + the shared matcher every rep — in
// production those are shared once across every active framework of that
// language (Run's fwStart timer only starts after both are done, see
// pipeline.go's PerFramework accounting), so that profile is >85% tree-sitter
// parsing noise, not the framework's own cost. This is an in-package test
// (same package pipeline, not pipeline_test) so it can call the same
// unexported helpers Run does, parse+match once, and profile only the
// per-framework loop body (applyMatches lowering + resolve/config/table/hub +
// datalog Eval + emit) for one target framework — the actual slice
// PF_FACTPIPE_PROFILE's PerFramework map measured as cedar's post-XM.11 top
// frameworks.
func profileFrameworkLoopOnly(t *testing.T, db, root, lang, fwName, tag string) {
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

	snap := graph.Snapshot{}
	for _, n := range idx.Nodes {
		if n.Language == lang {
			snap.Nodes = append(snap.Nodes, *n)
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

	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var fw *Framework
	for _, f := range reg.All() {
		if f.Name == fwName {
			fw = f
		}
	}
	if fw == nil {
		t.Fatalf("%s not found", fwName)
	}

	var files []ParsedFile
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
		grammar := lang
		switch filepath.Ext(n.File) {
		case ".jsx", ".tsx":
			grammar = "tsx"
		case ".ts":
			grammar = "typescript"
		}
		files = append(files, ParsedFile{Path: n.File, Language: lang, Grammar: grammar, Src: src})
	}
	t.Logf("%s files=%d", lang, len(files))

	// Shared, one-time cost — matches production: parse once, match once,
	// against every active framework so the shared matcher builds its full
	// per-language pattern set (same as a real service call with `active`).
	roots, releaseRoots := parseFileRoots(files)
	defer releaseRoots()
	fws := reg.All() // build the shared matcher against the full registry, same footprint as a real multi-framework service call
	sm := buildSharedMatchers(fws)
	matchesByFw, err := sm.matchAll(files, roots)
	if err != nil {
		t.Fatal(err)
	}

	base := factpipe.NewFactSet()
	factpipe.GraphFacts(snap, base)
	baseIndex := indexByPred(base)

	cpuF, err := os.Create("/tmp/xm12b_" + tag + "_" + fwName + "_cpu.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer cpuF.Close()
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		t.Fatal(err)
	}

	const reps = 20
	var edgeCount int
	loopStart := time.Now()
	for i := 0; i < reps; i++ {
		fset := factpipe.NewFactSet()
		keep := frameworkKeepSet(fw)
		preds := make([]string, 0, len(keep))
		for p := range keep {
			preds = append(preds, p)
		}
		sort.Strings(preds)
		for _, p := range preds {
			for _, f := range baseIndex[p] {
				fset.Add(f)
			}
		}
		if err := applyMatches(fw, matchesByFw[fw.Name], fset); err != nil {
			t.Fatal(err)
		}
		factpipe.ApplyResolves(fw.Resolves, snap.Files, fset)
		factpipe.ApplyConfig(fw.Configs, snap.ServicePath, fset)
		factpipe.ApplyTable(fw.Tables, snap.ServicePath, fset)
		factpipe.ApplyHub(fw.Hubs, snap.Nodes, snap.Files, snap.ServicePath, snap.Links, snap.Schema, fset)
		factpipe.ApplyDerive(fw.Derives, fset)

		fr := factRelations(fw, fset)
		derived, prov, err := fw.Rules.Eval(fr)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range fw.Emits {
			er := e.Apply(derived[e.Relation()], prov)
			edgeCount = len(er.Edges)
		}
	}
	pprof.StopCPUProfile()
	elapsed := time.Since(loopStart)
	t.Logf("%d reps in %s (%.1fms/rep), edges/rep=%d", reps, elapsed, float64(elapsed.Milliseconds())/float64(reps), edgeCount)
	t.Logf("wrote /tmp/xm12b_%s_%s_cpu.pprof", tag, fwName)
}

// TestCedarFrameworkLoopProfile is XM.12's corrected per-framework profile:
// same 5 frameworks as TestCedarFrameworkProfile, but excluding shared
// parse+match cost so the profile shows only each framework's own marginal
// work.
//
//	PF_CEDAR_DB=$HOME/Projects/mdr/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestCedarFrameworkLoopProfile -v
func TestCedarFrameworkLoopProfile(t *testing.T) {
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
			profileFrameworkLoopOnly(t, db, root, c.lang, c.name, "cedar")
		})
	}
}
