package linker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/vgbaseline"
)

// TestCaptureVGBaseline freezes the current JS URL-resolver output as the
// Tier VG differential baseline (docs/js-value-graph-pilot-plan.md, VG.0).
//
// It does not index — it reads an already-cold graph DB, so the caller
// controls determinism:
//
//	rm -rf $POLYFLOW_CORPUS/.polyflow && ./dist/polyflow index
//	PF_VG_CAPTURE=internal/linker/testdata/vg_baseline/cedar.json \
//	  POLYFLOW_CORPUS=$POLYFLOW_CORPUS \
//	  go test ./internal/linker/ -run TestCaptureVGBaseline -count=1
//
// With PF_VG_CAPTURE unset the test skips, so `make test` is unaffected.
func TestCaptureVGBaseline(t *testing.T) {
	out := os.Getenv("PF_VG_CAPTURE")
	if out == "" {
		t.Skip("PF_VG_CAPTURE unset — set it to the baseline JSON path to capture")
	}
	corpus := expandTilde(os.Getenv("POLYFLOW_CORPUS"))
	if corpus == "" {
		t.Fatal("POLYFLOW_CORPUS unset — see docs/js-value-graph-pilot-plan.md 'Corpora'")
	}
	dbPath := filepath.Join(corpus, meta.DBDir, meta.DBFile)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("no cold index at %s — run `rm -rf %s && polyflow index` first (%v)",
			dbPath, filepath.Join(corpus, meta.DBDir), err)
	}

	st, err := graph.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer st.Close()
	ctx := context.Background()

	nodes, err := st.ListNodesByType(ctx, string(graph.NodeTypeHTTPClient), "", 1<<30)
	if err != nil {
		t.Fatalf("list http_client nodes: %v", err)
	}

	var clients []vgbaseline.ClientRecord
	for _, n := range nodes {
		if !fromVGPass(n) {
			continue
		}
		edgesOut, err := st.ListEdgesFrom(ctx, n.ID)
		if err != nil {
			t.Fatalf("edges from %s: %v", n.ID, err)
		}
		var ers []vgbaseline.EdgeRecord
		for _, e := range edgesOut {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			ers = append(ers, vgbaseline.EdgeRecord{From: e.From, To: e.To, Type: string(e.Type)})
		}
		clients = append(clients, vgbaseline.ClientRecord{
			ID:      n.ID,
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			Path:    n.Meta["path"],
			Method:  n.Meta["method"],
			URL:     n.Meta["url"],
			Edges:   ers,
		})
	}

	refs, err := st.ListUnresolvedRefs(ctx)
	if err != nil {
		t.Fatalf("list unresolved refs: %v", err)
	}
	var ledger []vgbaseline.LedgerRecord
	for _, r := range refs {
		if r.Kind != "prop_client_dynamic_url" {
			continue
		}
		ledger = append(ledger, vgbaseline.LedgerRecord{
			Key:     r.File + "\x00" + strconv.Itoa(r.Line),
			Service: r.Service,
			File:    r.File,
			Line:    r.Line,
			Name:    r.Name,
			Kind:    r.Kind,
		})
	}

	b := &vgbaseline.Baseline{
		Corpus:  strings.TrimSuffix(filepath.Base(out), ".json"),
		Clients: clients,
		Ledger:  ledger,
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(out); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("wrote %s: %d http_client rows, %d ledger rows", out, len(clients), len(ledger))
}

// fromVGPass reports whether an http_client node was minted or mutated by one
// of the four passes the pilot re-expresses: js_local_url (stamps
// url_origin=local_binding on the node it mutates and on every branch it
// mints), js_prop_urls / js_prop_transport / js_prop_client (each mints nodes
// carrying a distinct Meta["pattern"]).
func fromVGPass(n *graph.Node) bool {
	switch n.Meta["pattern"] {
	case "prop_url", "prop_transport", "prop_client":
		return true
	}
	return n.Meta["url_origin"] == localURLOriginLocalBinding
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
