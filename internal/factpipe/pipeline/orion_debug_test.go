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

// TestOrionRailsFiltersProfile is XM.4's lens 2/3 harness
// (docs/factpipe-cross-framework-matching-plan.md): runs rails_filters alone
// against real orion data (the single framework factpipe_pass.go's
// PF_FACTPIPE_PROFILE run just named as ~74% of factpipe_frameworks' total
// wall time) under a CPU profile + a heap (alloc) profile, so `go tool
// pprof` can be pointed at the exact hot path instead of the whole
// ~30-framework loop. Env-gated, throwaway — same shape as
// TestCedarRailsFiltersDebug, not a permanent measurement (no assertions,
// PF_ORION_DB unset skips it).
//
//	PF_ORION_DB=$HOME/Projects/nextGen/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestOrionRailsFiltersProfile -v
func TestOrionRailsFiltersProfile(t *testing.T) {
	db := os.Getenv("PF_ORION_DB")
	if db == "" {
		t.Skip("PF_ORION_DB unset")
	}
	root := filepath.Join(os.Getenv("HOME"), "Projects/nextGen")
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
		if n.Language == "ruby" {
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
		if f.Name == "rails_filters" {
			fw = f
		}
	}
	if fw == nil {
		t.Fatal("rails_filters not found")
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
		files = append(files, pipeline.ParsedFile{Path: n.File, Language: "ruby", Grammar: "ruby", Src: src})
	}
	t.Logf("ruby files=%d", len(files))

	cpuF, err := os.Create("/tmp/xm9_orion_railsfilters_cpu.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer cpuF.Close()
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		t.Fatal(err)
	}

	// Repeat several times so a single-service run's CPU profile has enough
	// samples to be meaningful — pprof's sampling profiler needs wall time,
	// not just one ~1s call, to attribute cycles reliably.
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

	heapF, err := os.Create("/tmp/xm9_orion_railsfilters_heap.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer heapF.Close()
	if err := pprof.WriteHeapProfile(heapF); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote /tmp/xm9_orion_railsfilters_cpu.pprof and /tmp/xm9_orion_railsfilters_heap.pprof")
}
