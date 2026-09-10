package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestCedarRailsModelTablesDebug is the FX.8 differential harness for the
// rails_model_tables migration: it runs the declarative framework over a real
// cedar index and diffs its backed_by edges against the ones already in the
// DB (produced by the retired internal/linker/rails_model_tables.go).
//
//	PF_CEDAR_DB=$HOME/Projects/mdr/.polyflow/graph.db \
//	  go test ./internal/factpipe/pipeline/ -run TestCedarRailsModelTablesDebug -v
func TestCedarRailsModelTablesDebug(t *testing.T) {
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

	// per-service snapshot, same shape as factpipe_pass builds: every non-test
	// node of a service that has any ruby source (tables included — they are
	// the backed_by targets), not just ruby-language nodes.
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

	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	var fw *pipeline.Framework
	for _, f := range reg.All() {
		if f.Name == "rails_model_tables" {
			fw = f
		}
	}
	if fw == nil {
		t.Fatal("rails_model_tables not registered")
	}

	inSnap := map[string]bool{}
	for _, s := range bySvc {
		for _, n := range s.Nodes {
			inSnap[n.ID] = true
		}
	}

	type key struct{ from, to string }
	oldEdges := map[key]string{}
	for _, e := range idx.AllEdges() {
		if e.Type == graph.EdgeTypeBackedBy && inSnap[e.From] {
			// AT.2 (rails) only — skip GORM's backed_by.
			if e.Meta["via"] == "gorm_convention" {
				continue
			}
			oldEdges[key{e.From, e.To}] = e.Meta["via"]
		}
	}

	idxUnresolved, err := store.ListUnresolvedRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	newEdges := map[key]string{}
	newLedger := map[string]bool{}
	var totalLedger int
	for svc, snap := range bySvc {
		inSvc := map[string]bool{}
		for _, n := range snap.Nodes {
			inSvc[n.ID] = true
		}
		var files []pipeline.ParsedFile
		seen := map[string]bool{}
		for _, n := range snap.Nodes {
			if n.File == "" || seen[n.File] {
				continue
			}
			if !hasSuffixAny(n.File, ".rb", ".rake") {
				continue
			}
			seen[n.File] = true
			src, err := os.ReadFile(filepath.Join(root, n.File))
			if err != nil {
				continue
			}
			files = append(files, pipeline.ParsedFile{Path: n.File, Language: "ruby", Grammar: "ruby", Src: src})
		}
		res, err := pipeline.Run([]*pipeline.Framework{fw}, files, *snap)
		if err != nil {
			t.Fatalf("svc %s: %v", svc, err)
		}
		for _, e := range res.Edges {
			newEdges[key{e.From, e.To}] = e.Meta["via"]
		}
		totalLedger += len(res.Unresolved)
		for _, u := range res.Unresolved {
			newLedger[u.Kind+"|"+u.Name] = true
		}
	}
	var oldLedger []string
	for _, u := range idxUnresolved {
		if u.Kind == "rails_model_table_unresolved" || u.Kind == "rails_table_unowned" {
			k := u.Kind + "|" + u.Name
			oldLedger = append(oldLedger, k)
			if !newLedger[k] {
				t.Logf("  ledger dropped: %s", k)
			}
		}
	}
	for k := range newLedger {
		found := false
		for _, o := range oldLedger {
			if o == k {
				found = true
			}
		}
		if !found {
			t.Logf("  ledger added: %s", k)
		}
	}

	var added, dropped, viaChanged int
	for k, nv := range newEdges {
		if ov, ok := oldEdges[k]; !ok {
			added++
			t.Logf("  added: %s -> %s (via %s)", k.from, k.to, nv)
		} else if ov != nv {
			viaChanged++
		}
	}
	var sampleDropped []string
	for k := range oldEdges {
		if _, ok := newEdges[k]; !ok {
			dropped++
			if len(sampleDropped) < 25 {
				sampleDropped = append(sampleDropped, k.from+" -> "+k.to)
			}
		}
	}
	sort.Strings(sampleDropped)

	t.Logf("old backed_by (rails) = %d", len(oldEdges))
	t.Logf("new backed_by (rails) = %d", len(newEdges))
	t.Logf("added=%d dropped=%d via-changed=%d ledger=%d", added, dropped, viaChanged, totalLedger)
	for _, s := range sampleDropped {
		t.Logf("  dropped: %s", s)
	}
}

func hasSuffixAny(s string, sfx ...string) bool {
	for _, x := range sfx {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}
