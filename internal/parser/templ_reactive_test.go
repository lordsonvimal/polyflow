package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Reactive datastar attributes (data-show, data-class, data-attr:*, data-when)
// emit a `reads` edge per distinct referenced signal. data-when's on/off value
// and dynamic `"$" + sig` names contribute no reads.
func TestTemplParser_ReactiveReads(t *testing.T) {
	p := &TemplParser{}
	nodes, edges, _, err := p.Parse("testdata/reactive.templ", "app", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Collect signal read-nodes by ID for edge resolution.
	nodeByID := map[string]graph.Node{}
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	readSignals := map[string]int{}
	var readEdges int
	for _, e := range edges {
		if e.Type != graph.EdgeTypeReads {
			continue
		}
		readEdges++
		readSignals[nodeByID[e.To].Meta["signal"]]++
	}

	// coachOpen(×2), streaming, input, evalVisible = 5 reads edges.
	if readEdges != 5 {
		t.Fatalf("reads edges = %d, want 5 (signals: %v)", readEdges, readSignals)
	}
	for _, want := range []string{"coachOpen", "streaming", "input", "evalVisible"} {
		if readSignals[want] == 0 {
			t.Errorf("missing reads edge for signal %q; got %v", want, readSignals)
		}
	}
	if readSignals["coachOpen"] != 2 {
		t.Errorf("coachOpen read %d times, want 2 (data-show + data-class)", readSignals["coachOpen"])
	}
	// The dynamic `"$" + openSignal` name must not surface as a signal.
	if readSignals["openSignal"] != 0 {
		t.Errorf("dynamic signal name leaked as a read: %v", readSignals)
	}

	// data-when="on" must not mint a signal read.
	if _, ok := readSignals["on"]; ok {
		t.Errorf("data-when on/off value mis-read as a signal")
	}
}
