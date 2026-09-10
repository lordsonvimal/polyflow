package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestCedarRailsFiltersDebug runs the FX.8 rails_filters framework over a real
// cedar index and dumps the relations the 0/0/0 diff cares about. Env-gated:
//
//	PF_CEDAR_DB=$HOME/Projects/mdr/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestCedarRailsFiltersDebug -v
func TestCedarRailsFiltersDebug(t *testing.T) {
	db := os.Getenv("PF_CEDAR_DB")
	if db == "" {
		t.Skip("PF_CEDAR_DB unset")
	}
	root := filepath.Join(os.Getenv("HOME"), "Projects/mdr")
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

	extra := []string{
		"filter_call", "filter_cb", "reg_owner", "subject", "effective",
		"cb_hit", "cb_target", "cb_ntargets", "cb_ambiguous", "filt_conf",
		"action", "owns", "owner_class",
	}
	derived, err := fw.EvalOnce(files, snap, extra...)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(extra, "class_filter", "action_filter", "filter_miss") {
		t.Logf("%-14s %d", rel, len(derived[rel]))
	}
	confDist := map[string]int{}
	for _, tup := range derived["filt_conf"] {
		if len(tup) >= 6 {
			confDist[tup[5]]++
		}
	}
	t.Logf("filt_conf value distribution: %v", confDist)

	res, err := pipeline.Run([]*pipeline.Framework{fw}, files, snap)
	if err != nil {
		t.Fatal(err)
	}
	edgeConf := map[string]int{}
	for _, e := range res.Edges {
		edgeConf[e.Confidence]++
	}
	t.Logf("emitted edges=%d confidence=%v ledger=%d", len(res.Edges), edgeConf, len(res.Ledger))
}
