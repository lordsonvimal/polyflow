package parser

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestTemplParser_DOMDataAttrs covers IA.5's producer capture: a computed
// data-* attribute (Go string-literal prefix + expression) records only its
// static prefix (marked with a trailing "*"), and a plain constant data-*
// attribute records its full value verbatim — both under dom_data_attrs so
// LinkDOMContracts can resolve the templ->JS dom_contract seam.
func TestTemplParser_DOMDataAttrs(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/dom_attrs.templ", "app", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var comp *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeComponent {
			comp = &nodes[i]
		}
	}
	if comp == nil {
		t.Fatal("no component node emitted")
	}

	entries := strings.Split(comp.Meta["dom_data_attrs"], "\n")
	got := map[string]bool{}
	for _, e := range entries {
		if i := strings.LastIndexByte(e, '@'); i >= 0 {
			got[e[:i]] = true
		}
	}
	if !got["data-testid=promotion-button-*"] {
		t.Errorf("expected a prefix entry for the computed data-testid; got %q", comp.Meta["dom_data_attrs"])
	}
	if !got["data-role=button"] {
		t.Errorf("expected an exact entry for the constant data-role; got %q", comp.Meta["dom_data_attrs"])
	}
	if len(got) != 2 {
		t.Errorf("dom_data_attrs = %q, want exactly 2 entries", comp.Meta["dom_data_attrs"])
	}
}
