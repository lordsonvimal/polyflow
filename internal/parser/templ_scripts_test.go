package parser

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// The templ parser stashes <script src> assets and element id= definitions on
// the enclosing component's meta for the cross-file linker passes to resolve.
func TestTemplParser_ScriptsAndIDs(t *testing.T) {
	p := &TemplParser{}
	nodes, _, _, err := p.Parse("testdata/scripts.templ", "app", nil)
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

	srcs := strings.Split(comp.Meta["script_srcs"], "\n")
	wantSrcs := map[string]bool{
		"js/liveclass-room.js": false, // from helpers.Asset("…") expression
		"/static/js/extra.js":  false, // from constant src
	}
	for _, s := range srcs {
		if _, ok := wantSrcs[s]; ok {
			wantSrcs[s] = true
		} else if s != "" {
			t.Errorf("unexpected script src %q (inline <script> must not appear)", s)
		}
	}
	for s, seen := range wantSrcs {
		if !seen {
			t.Errorf("missing script src %q; got %q", s, comp.Meta["script_srcs"])
		}
	}

	ids := strings.Split(comp.Meta["dom_ids"], "\n")
	gotIDs := map[string]bool{}
	for _, entry := range ids {
		if i := strings.LastIndexByte(entry, '@'); i >= 0 {
			gotIDs[entry[:i]] = true
		}
	}
	if !gotIDs["board-root"] || !gotIDs["white-clock"] {
		t.Errorf("expected ids board-root and white-clock; got %q", comp.Meta["dom_ids"])
	}
	if len(gotIDs) != 2 {
		t.Errorf("dom_ids = %q, want exactly 2 (class-only element excluded)", comp.Meta["dom_ids"])
	}
}
