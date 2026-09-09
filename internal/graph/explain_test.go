package graph

import "testing"

func TestExplainEdge(t *testing.T) {
	idx := NewAdjacencyIndex()
	from := &Node{ID: "h1", Label: "Handler", File: "routes.go", Line: 12, Service: "api"}
	to := &Node{ID: "c1", Label: "Client", File: "client.js", Line: 3}
	idx.AddNode(from)
	idx.AddNode(to)
	e := &Edge{
		ID: "e1", From: "h1", To: "c1", Type: EdgeTypeHTTPCall, Label: "GET /x",
		Confidence: ConfidenceStatic,
		Sources: []SourceRef{
			{Provider: "static", Confidence: "candidate", Layer: "L5", Rule: "js_link", Ref: "routes.go:12"},
			{Provider: "runtime", Confidence: "observed", Ref: "sess/span"},
		},
	}
	idx.AddEdge(e)

	ledger := []UnresolvedRef{
		{Service: "api", File: "routes.go", Line: 12, Name: "dynamicURL", Kind: "dynamic_url"},
		{Service: "api", File: "other.go", Line: 1, Name: "x", Kind: "call_ref"},
	}

	ex, ok := ExplainEdge(idx, ledger, "e1")
	if !ok {
		t.Fatal("edge not found")
	}
	if ex.From != from || ex.To != to {
		t.Fatal("endpoints not resolved")
	}
	if len(ex.RuleChain) != 1 || ex.RuleChain[0].Rule != "js_link" || ex.RuleChain[0].Layer != "L5" {
		t.Fatalf("rule chain: %#v", ex.RuleChain)
	}
	if len(ex.LedgerRows) != 1 || ex.LedgerRows[0].Name != "dynamicURL" {
		t.Fatalf("ledger rows: %#v", ex.LedgerRows)
	}

	if _, ok := ExplainEdge(idx, ledger, "missing"); ok {
		t.Fatal("expected miss for unknown edge id")
	}
}
