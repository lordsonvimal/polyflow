package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hasCallsEdge(edges []graph.Edge, from, to string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to && e.Type == graph.EdgeTypeCalls {
			return true
		}
	}
	return false
}

// TestPython_SelfAttrCall verifies self.method() resolves to a same-class
// sibling method (Tier PC) — this call shape was previously invisible:
// patterns/python/functions.yaml only matched bare-identifier callees.
func TestPython_SelfAttrCall(t *testing.T) {
	t.Parallel()
	src := `
class Widget:
    def render(self):
        self.prepare()

    def prepare(self):
        pass
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	render := pyNode(nodes, graph.NodeTypeFunction, "render")
	prepare := pyNode(nodes, graph.NodeTypeFunction, "prepare")
	require.NotNil(t, render)
	require.NotNil(t, prepare)

	assert.True(t, hasCallsEdge(edges, render.ID, prepare.ID), "render -> prepare calls edge missing")
}

// TestPython_ClsAttrCall verifies cls.method() (classmethod receiver)
// resolves the same way self does.
func TestPython_ClsAttrCall(t *testing.T) {
	t.Parallel()
	src := `
class Widget:
    @classmethod
    def create(cls):
        return cls.build()

    @classmethod
    def build(cls):
        pass
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	create := pyNode(nodes, graph.NodeTypeFunction, "create")
	build := pyNode(nodes, graph.NodeTypeFunction, "build")
	require.NotNil(t, create)
	require.NotNil(t, build)

	assert.True(t, hasCallsEdge(edges, create.ID, build.ID), "create -> build calls edge missing")
}

// TestPython_TypedLocalAttrCall verifies `x = Foo(); x.bar()` resolves
// against the class Foo when Foo is defined in the same file.
func TestPython_TypedLocalAttrCall(t *testing.T) {
	t.Parallel()
	src := `
class Repo:
    def get(self):
        pass

def load():
    repo = Repo()
    repo.get()
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	load := pyNode(nodes, graph.NodeTypeFunction, "load")
	get := pyNode(nodes, graph.NodeTypeFunction, "get")
	require.NotNil(t, load)
	require.NotNil(t, get)

	assert.True(t, hasCallsEdge(edges, load.ID, get.ID), "load -> Repo.get calls edge missing")
}

// TestPython_UntypedReceiverNoEdge verifies an attribute call on an
// untyped receiver (a bare parameter) produces neither a calls edge nor a
// ledger entry — the deliberate no-op policy for the honestly-unknown case
// (docs/python-parity-plan.md Tier PC).
func TestPython_UntypedReceiverNoEdge(t *testing.T) {
	t.Parallel()
	src := `
class Widget:
    def get(self):
        pass

def process(obj):
    obj.get()
`
	nodes, edges, unresolved := parsePythonSrc(t, "app.py", src)

	process := pyNode(nodes, graph.NodeTypeFunction, "process")
	get := pyNode(nodes, graph.NodeTypeFunction, "get")
	require.NotNil(t, process)
	require.NotNil(t, get)

	assert.False(t, hasCallsEdge(edges, process.ID, get.ID), "untyped obj.get() must not resolve to Widget.get")
	for _, u := range unresolved {
		assert.NotEqual(t, "get", u.Name, "untyped attribute call must not be ledgered")
	}
}

// TestPython_SelfCallToUndefinedMethod verifies self.method() where method
// isn't defined in this class produces no edge and no ledger entry (e.g. an
// inherited/base-class method not visible in this file).
func TestPython_SelfCallToUndefinedMethod(t *testing.T) {
	t.Parallel()
	src := `
class Widget:
    def render(self):
        self.inherited_method()
`
	nodes, edges, unresolved := parsePythonSrc(t, "app.py", src)

	render := pyNode(nodes, graph.NodeTypeFunction, "render")
	require.NotNil(t, render)
	for _, e := range edges {
		assert.False(t, e.From == render.ID && e.Type == graph.EdgeTypeCalls,
			"render must not gain a calls edge for an undefined self method, got edge to %s", e.To)
	}
	for _, u := range unresolved {
		assert.NotEqual(t, "inherited_method", u.Name, "undefined self-method call must not be ledgered")
	}
}

// TestPython_CrossClassNoFalseResolve verifies self.method() in one class
// does not resolve to a same-named method on an unrelated class.
func TestPython_CrossClassNoFalseResolve(t *testing.T) {
	t.Parallel()
	src := `
class Alpha:
    def run(self):
        self.step()

class Beta:
    def step(self):
        pass
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	run := pyNode(nodes, graph.NodeTypeFunction, "run")
	require.NotNil(t, run)

	var betaStepID string
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && n.Label == "step" {
			betaStepID = n.ID
		}
	}
	require.NotEmpty(t, betaStepID)

	assert.False(t, hasCallsEdge(edges, run.ID, betaStepID), "Alpha.run must not resolve self.step() to Beta.step")
}
