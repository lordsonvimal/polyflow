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

// TestCedarSprocketsAssetsDebug is the FX.8 differential harness for the
// sprockets_assets migration (resolve_path step 4): it runs the two
// declarative frameworks (sprockets_directives + sprockets_includes) over a
// real cedar index and diffs their `imports` edges against the ones already
// in the DB (produced by the retired internal/linker/sprockets_assets.go +
// internal/sprockets). No reindex is needed — every scanned file already has
// a NodeTypeFile node (ensure_scanned_files ran before the retired pass too),
// so the existing DB's node/file set is a valid Snapshot to re-derive from.
//
//	PF_CEDAR_DB=$HOME/Projects/mdr/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestCedarSprocketsAssetsDebug -v
func TestCedarSprocketsAssetsDebug(t *testing.T) {
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

	rubySvc := map[string]bool{}
	for _, n := range idx.Nodes {
		if n.Language == "ruby" {
			rubySvc[n.Service] = true
		}
	}
	bySvc := map[string]*graph.Snapshot{}
	nodeSvc := map[string]string{}
	for _, n := range idx.Nodes {
		nodeSvc[n.ID] = n.Service
		if !rubySvc[n.Service] || n.Meta[graph.MetaIsTest] == "true" || graph.IsTestFilePath(n.File) {
			continue
		}
		s := bySvc[n.Service]
		if s == nil {
			s = &graph.Snapshot{}
			bySvc[n.Service] = s
		}
		s.Nodes = append(s.Nodes, *n)
	}
	for _, e := range idx.AllEdges() {
		if s := bySvc[nodeSvc[e.From]]; s != nil {
			ec := e
			s.Edges = append(s.Edges, ec)
		}
	}
	// resolve_path needs the whole service file list (declare-nothing assets
	// included), not just node files — but ensure_scanned_files means every
	// scanned file already has a node, so the node File set already is that
	// whole list.
	for _, s := range bySvc {
		seen := map[string]bool{}
		for _, n := range s.Nodes {
			if n.File != "" && !seen[n.File] {
				seen[n.File] = true
				s.Files = append(s.Files, n.File)
			}
		}
	}

	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var fws []*pipeline.Framework
	for _, f := range reg.All() {
		if f.Name == "sprockets_directives" || f.Name == "sprockets_includes" {
			fws = append(fws, f)
		}
	}
	if len(fws) != 2 {
		t.Fatalf("expected 2 sprockets frameworks registered, got %d", len(fws))
	}

	inSnap := map[string]bool{}
	for _, s := range bySvc {
		for _, n := range s.Nodes {
			inSnap[n.ID] = true
		}
	}

	type key struct{ from, to, label string }
	oldEdges := map[key]string{}
	for _, e := range idx.AllEdges() {
		if e.Type == graph.EdgeTypeImports && inSnap[e.From] &&
			(e.Meta["mechanism"] == "sprockets" || e.Meta["mechanism"] == "include_tag") {
			oldEdges[key{e.From, e.To, e.Label}] = e.Meta["mechanism"]
		}
	}

	newEdges := map[key]string{}
	var totalLedger int
	newLedger := map[string]bool{}
	for svc, snap := range bySvc {
		var files []pipeline.ParsedFile
		seen := map[string]bool{}
		for _, n := range snap.Nodes {
			if n.File == "" || seen[n.File] {
				continue
			}
			lang, grammar := "", ""
			switch {
			case hasSuffixAny(n.File, ".js", ".mjs", ".cjs"):
				lang, grammar = "javascript", "javascript"
			case hasSuffixAny(n.File, ".jsx"):
				lang, grammar = "javascript", "tsx"
			case hasSuffixAny(n.File, ".ts"):
				lang, grammar = "javascript", "typescript"
			case hasSuffixAny(n.File, ".tsx"):
				lang, grammar = "javascript", "tsx"
			case hasSuffixAny(n.File, ".erb"):
				lang, grammar = "erb", "erb"
			default:
				continue
			}
			seen[n.File] = true
			src, err := os.ReadFile(filepath.Join(root, n.File))
			if err != nil {
				continue
			}
			files = append(files, pipeline.ParsedFile{Path: n.File, Language: lang, Grammar: grammar, Src: src})
		}
		if len(files) == 0 {
			continue
		}
		res, err := pipeline.Run(fws, files, *snap)
		if err != nil {
			t.Fatalf("svc %s: %v", svc, err)
		}
		for _, e := range res.Edges {
			newEdges[key{e.From, e.To, e.Label}] = e.Meta["mechanism"]
		}
		totalLedger += len(res.Unresolved)
		for _, u := range res.Unresolved {
			newLedger[u.Kind+"|"+u.Name] = true
		}
	}

	idxUnresolved, err := store.ListUnresolvedRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var oldLedger []string
	for _, u := range idxUnresolved {
		if u.Kind == "sprockets_require_unresolved" || u.Kind == "sprockets_include_unresolved" {
			oldLedger = append(oldLedger, u.Kind+"|"+u.Name)
		}
	}

	var added, dropped int
	var sampleAdded, sampleDropped []string
	for k := range newEdges {
		if _, ok := oldEdges[k]; !ok {
			added++
			if len(sampleAdded) < 25 {
				sampleAdded = append(sampleAdded, k.from+" -> "+k.to+" : "+k.label)
			}
		}
	}
	for k := range oldEdges {
		if _, ok := newEdges[k]; !ok {
			dropped++
			if len(sampleDropped) < 25 {
				sampleDropped = append(sampleDropped, k.from+" -> "+k.to+" : "+k.label)
			}
		}
	}
	sort.Strings(sampleAdded)
	sort.Strings(sampleDropped)

	t.Logf("old imports (sprockets) = %d", len(oldEdges))
	t.Logf("new imports (sprockets) = %d", len(newEdges))
	t.Logf("added=%d dropped=%d old-ledger=%d new-ledger=%d", added, dropped, len(oldLedger), totalLedger)
	for _, s := range sampleAdded {
		t.Logf("  added: %s", s)
	}
	for _, s := range sampleDropped {
		t.Logf("  dropped: %s", s)
	}
}
