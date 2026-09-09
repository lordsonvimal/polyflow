package vgbaseline

import (
	"strings"
	"testing"
)

func client(id, url string, edges ...EdgeRecord) ClientRecord {
	return ClientRecord{ID: id, Service: "orion", File: "react/A.jsx", Line: 4, URL: url, Method: "GET", Edges: edges}
}

func TestCompareEmptyDiff(t *testing.T) {
	b := &Baseline{Clients: []ClientRecord{client("n1", "/api/a")}}
	if d := Compare(b, b); d.HasDiff() {
		t.Fatalf("identical baselines differ:\n%s", d)
	}
}

func TestCompareIgnoresLayerAndRule(t *testing.T) {
	base := &Baseline{Clients: []ClientRecord{
		client("n1", "/api/a", EdgeRecord{From: "n1", To: "h1", Type: "http_call"}),
	}}
	cand := &Baseline{Clients: []ClientRecord{
		client("n1", "/api/a", EdgeRecord{From: "n1", To: "h1", Type: "http_call", Layer: "L2", Rule: "valuegraph/javascript#literals"}),
	}}
	if d := Compare(base, cand); d.HasDiff() {
		t.Fatalf("Layer/Rule difference was not ignored:\n%s", d)
	}
}

func TestCompareGained(t *testing.T) {
	base := &Baseline{}
	cand := &Baseline{Clients: []ClientRecord{client("n1", "/api/a")}}
	d := Compare(base, cand)
	if got := d.String(); !strings.Contains(got, "GAINED") || d.Regressions() != 0 {
		t.Fatalf("want one GAINED, no regression, got:\n%s", got)
	}
}

func TestCompareLost(t *testing.T) {
	base := &Baseline{Clients: []ClientRecord{client("n1", "/api/a")}}
	cand := &Baseline{}
	d := Compare(base, cand)
	if got := d.String(); !strings.Contains(got, "LOST") || d.Regressions() != 1 {
		t.Fatalf("want one LOST regression, got:\n%s", got)
	}
}

func TestCompareChangedSameID(t *testing.T) {
	base := &Baseline{Clients: []ClientRecord{client("n1", "/api/a")}}
	cand := &Baseline{Clients: []ClientRecord{client("n1", "/api/b")}}
	d := Compare(base, cand)
	if got := d.String(); !strings.Contains(got, "CHANGED") || d.Regressions() != 1 {
		t.Fatalf("want one CHANGED regression, got:\n%s", got)
	}
}

func TestCompareChangedSameSiteDifferentID(t *testing.T) {
	// prop_url IDs embed the path, so a path change looks like a lost id and
	// a gained id at the same (service, file, line) — Compare re-pairs them.
	base := &Baseline{Clients: []ClientRecord{client("orion:react/A.jsx:http_client:prop_url:4:/api/a", "/api/a")}}
	cand := &Baseline{Clients: []ClientRecord{client("orion:react/A.jsx:http_client:prop_url:4:/api/b", "/api/b")}}
	d := Compare(base, cand)
	rows := d.Rows
	if len(rows) != 1 || rows[0].Class != Changed {
		t.Fatalf("want a single CHANGED row, got:\n%s", d)
	}
}

func TestCompareLedger(t *testing.T) {
	base := &Baseline{Ledger: []LedgerRecord{{Key: "react/A.jsx\x0012", File: "react/A.jsx", Line: 12, Kind: "prop_client_dynamic_url"}}}
	cand := &Baseline{Ledger: []LedgerRecord{{Key: "react/B.jsx\x005", File: "react/B.jsx", Line: 5, Kind: "prop_client_dynamic_url"}}}
	d := Compare(base, cand)
	got := d.String()
	if !strings.Contains(got, "GAINED  ledger") || !strings.Contains(got, "LOST    ledger") {
		t.Fatalf("want one gained + one lost ledger row, got:\n%s", got)
	}
	if d.Regressions() != 1 {
		t.Fatalf("candidate ledger row = one regression, got %d", d.Regressions())
	}
}

func TestMarshalDeterministic(t *testing.T) {
	mk := func() *Baseline {
		return &Baseline{
			Corpus: "cedar",
			Clients: []ClientRecord{
				client("n2", "/api/b", EdgeRecord{From: "n2", To: "hZ", Type: "http_call"}, EdgeRecord{From: "n2", To: "hA", Type: "http_call"}),
				client("n1", "/api/a"),
			},
			Ledger: []LedgerRecord{{Key: "z\x001"}, {Key: "a\x002"}},
		}
	}
	a, err := mk().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := mk().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("Marshal not deterministic:\n%s\n---\n%s", a, b)
	}
}
