package parser_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/require"
)

// The method_decl pattern captures the receiver type identifier (bare — the
// query reaches inside pointer_type, so both pointer and value receivers yield
// the unadorned struct name) so the containment linker can draw struct→method
// edges. Plain functions carry no receiver.
func TestGoParser_MethodReceiverCapture(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/methods.go")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/methods.go", service, m)
	require.NoError(t, err)

	recv := map[string]string{}
	for _, n := range nodes {
		switch n.Type {
		case graph.NodeTypeMethod:
			recv[n.Label] = n.Meta["receiver"]
		case graph.NodeTypeFunction:
			if n.Label == "Helper" && n.Meta["receiver"] != "" {
				t.Errorf("plain function Helper has receiver %q", n.Meta["receiver"])
			}
		}
	}

	if recv["Save"] != "UserService" {
		t.Errorf("Save receiver = %q, want UserService (bare, pointer stripped by the query)", recv["Save"])
	}
	if recv["Load"] != "UserService" {
		t.Errorf("Load receiver = %q, want UserService", recv["Load"])
	}
}
