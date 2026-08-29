package pluginloader

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
)

// buildFakePlugin compiles internal/pluginloader/testdata/fakeplugin into
// tmpDir and returns the binary path. Phase 0's acceptance criterion is that
// this is an ordinary `go build` of a plugin author's own package — no
// polyflow source change required outside sdk/, internal/pluginloader/, and
// this test's own directory.
func buildFakePlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeplugin")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fakeplugin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build fakeplugin: %v\n%s", err, out)
	}
	return bin
}

// fixtureNodes is the synthetic fixture: one "source" node and one "target"
// node in the same file. This mirrors what a real component would receive
// batched per (component, service, file-batch).
func fixtureNodes() []lpgraph.Node {
	return []lpgraph.Node{
		{ID: "n1", Type: "function", Label: "source", Service: "svc", File: "a.go", Line: 1},
		{ID: "n2", Type: "function", Label: "target", Service: "svc", File: "a.go", Line: 5},
	}
}

// handWrittenEquivalent replicates fakeplugin's Link logic in-process,
// exactly as an equivalent hand-written internal/linker/*.go pass would —
// the byte-identical comparison Phase 0's test plan calls for.
func handWrittenEquivalent(nodes []lpgraph.Node) []graph.Edge {
	var sources, targets []lpgraph.Node
	for _, n := range nodes {
		switch n.Label {
		case "source":
			sources = append(sources, n)
		case "target":
			targets = append(targets, n)
		}
	}
	var edges []graph.Edge
	for _, s := range sources {
		for _, tgt := range targets {
			if s.File != tgt.File {
				continue
			}
			edges = append(edges, graph.Edge{
				ID:   "fakeplugin:" + s.ID + "->" + tgt.ID,
				From: s.ID,
				To:   tgt.ID,
				Type: "fake_edge",
			})
		}
	}
	return edges
}

func TestRoundTrip_HandshakeLinkResult(t *testing.T) {
	bin := buildFakePlugin(t)

	m := &Manifest{
		Name:            "fakeplugin",
		ProtocolVersion: linkplugin.ProtocolVersion,
		Dir:             filepath.Dir(bin),
		Entrypoint:      filepath.Base(bin),
		Components: []Component{
			{ID: "fake", Package: "fakeplugin-marker", Language: "go", Requires: nil},
		},
	}

	if note := CheckProtocolVersion(m); note != nil {
		t.Fatalf("CheckProtocolVersion: unexpected skip: %+v", note)
	}

	plugin, err := Launch(m)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer plugin.Close()

	if plugin.Name != "fakeplugin" {
		t.Errorf("Handshake name = %q, want fakeplugin", plugin.Name)
	}

	caps, err := plugin.Client.Requires(context.Background(), "fake")
	if err != nil {
		t.Fatalf("Requires: %v", err)
	}
	if len(caps) != 0 {
		t.Errorf("Requires(fake) = %v, want zero capabilities", caps)
	}

	nodes := fixtureNodes()
	result, err := plugin.Client.Link(context.Background(), linkplugin.LinkCallRequest{
		ComponentID: "fake",
		Service:     "svc",
		Files:       []string{"a.go"},
		Nodes:       nodes,
	})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}

	got := make([]graph.Edge, 0, len(result.Edges))
	for _, e := range result.Edges {
		got = append(got, graph.Edge{ID: e.ID, From: e.From, To: e.To, Type: graph.EdgeType(e.Type)})
	}
	want := handWrittenEquivalent(nodes)

	sortEdges(got)
	sortEdges(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plugin edges = %+v, want byte-identical to hand-written equivalent %+v", got, want)
	}
}

func sortEdges(edges []graph.Edge) {
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
}
