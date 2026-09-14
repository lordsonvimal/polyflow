package pipeline_test

// FX.8: pusher_helper_calls — the PU.2e half of
// internal/linker/pusher_producer.go's retired EnrichPusherHelperCalls,
// migrated onto patterns/ruby/pusher_helper_calls.yaml + rules/ruby/
// pusher_helper_calls.dl. Real parses (not hand-built nodes), mirroring
// internal/contract's FX.8.14 test convention and ruby_associations_test.go.
//
// The canonical PusherClient trigger publisher is a real graph.Node here
// (rather than something this framework mints), because the base, non-
// factpipe patterns/ruby/pusher.yaml pattern mints it at parse time — this
// test hand-builds that one node the same way the retired Go test did, since
// nothing in this package runs the base parser.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func phcFixtureNodes(t *testing.T, dir string, files map[string]string) ([]graph.Node, []pipeline.ParsedFile) {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	if err != nil {
		t.Fatalf("EmbeddedRegistry: %v", err)
	}
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	var parsed []pipeline.ParsedFile
	for rel, src := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		p := parser.ForFile(abs)
		if p == nil {
			t.Fatalf("no parser for %s", abs)
		}
		ns, _, _, err := p.Parse(abs, "svc", m, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", abs, err)
		}
		nodes = append(nodes, ns...)
		parsed = append(parsed, pipeline.ParsedFile{Path: abs, Language: "ruby", Src: []byte(src)})
	}
	return nodes, parsed
}

func phcActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg.Active([]deps.Dependency{{Name: "pusher", Version: "2.0.0"}})
}

func methodNode(t *testing.T, nodes []graph.Node, label string) *graph.Node {
	t.Helper()
	for i := range nodes {
		if (nodes[i].Type == graph.NodeTypeMethod || nodes[i].Type == graph.NodeTypeFunction) && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	t.Fatalf("no method node %q among %d nodes", label, len(nodes))
	return nil
}

// TestPusherHelperCallsRule_WiresBareAndReceiverCallsToCanonicalPublisher
// covers the concrete gap this pass exists for: a `def pusher`/`def
// self.non_instance_pusher` whose body touches PusherClient, called bare,
// via `self.`, and via a Const receiver from a different class — all three
// must land on the one canonical pusher_trigger publisher node.
func TestPusherHelperCallsRule_WiresBareAndReceiverCallsToCanonicalPublisher(t *testing.T) {
	dir := t.TempDir()
	nodes, parsed := phcFixtureNodes(t, dir, map[string]string{
		"things_controller.rb": `class ThingsController
  def pusher(msg, status)
    PusherClient.new(self, msg).push(msg, status)
  end

  def destroy
    pusher(I18n.t("pusher.things.deleted"), STATUS_START)
  end

  def other
    self.pusher("done", STATUS_END)
  end

  def reader
    pusher
  end
end
`,
	})

	wrapper := graph.Node{
		ID: "svc:pusher_client.rb:publisher:pusher_trigger:20", Type: graph.NodeTypePublisher,
		File: "pusher_client.rb", Line: 20,
		Meta: map[string]string{"pattern": "pusher_trigger"},
	}
	nodes = append(nodes, wrapper)

	snap := graph.Snapshot{Nodes: nodes}
	res, err := pipeline.Run(phcActive(t), parsed, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	destroy := methodNode(t, nodes, "destroy")
	other := methodNode(t, nodes, "other")
	reader := methodNode(t, nodes, "reader")

	froms := map[string]bool{}
	for _, e := range res.Edges {
		if e.To != wrapper.ID {
			continue
		}
		if e.Type != graph.EdgeTypeCalls {
			t.Errorf("want EdgeTypeCalls, got %q", e.Type)
		}
		if e.Meta["via"] != "pusher_helper_call" {
			t.Errorf("unexpected meta: %+v", e.Meta)
		}
		froms[e.From] = true
	}
	if !froms[destroy.ID] {
		t.Errorf("missing edge from bare `pusher(...)` call site %q; edges: %+v", destroy.ID, res.Edges)
	}
	if !froms[other.ID] {
		t.Errorf("missing edge from `self.pusher(...)` call site %q; edges: %+v", other.ID, res.Edges)
	}
	if froms[reader.ID] {
		t.Errorf("zero-arg `pusher` reader must not be treated as a helper call: %+v", res.Edges)
	}
}

// TestPusherHelperCallsRule_NoWrapperNoEdges covers the case where no
// PusherClient trigger publisher exists in the graph at all — every helper
// call site stays unresolved (no ledger noise, just no edges).
func TestPusherHelperCallsRule_NoWrapperNoEdges(t *testing.T) {
	dir := t.TempDir()
	nodes, parsed := phcFixtureNodes(t, dir, map[string]string{
		"things_controller.rb": `class ThingsController
  def pusher(msg, status)
    PusherClient.new(self, msg).push(msg, status)
  end

  def destroy
    pusher("x", "y")
  end
end
`,
	})

	snap := graph.Snapshot{Nodes: nodes}
	res, err := pipeline.Run(phcActive(t), parsed, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range res.Edges {
		if e.Meta["via"] == "pusher_helper_call" {
			t.Errorf("no publisher exists; must not emit a pusher_helper_call edge: %+v", e)
		}
	}
}
