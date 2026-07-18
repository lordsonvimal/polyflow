package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pyPatternsDir = "../../patterns"

// parsePythonSrc runs the PythonParser on inline source written to a temp file.
func parsePythonSrc(t *testing.T, filename, src string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(pyPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &PythonParser{}
	nodes, edges, unresolved, err := p.Parse(file, "svc", m)
	require.NoError(t, err)
	return nodes, edges, unresolved
}

func pyNode(nodes []graph.Node, typ graph.NodeType, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

// TestPython_FunctionNodes verifies module-level and nested function definitions
// produce NodeTypeFunction nodes.
func TestPython_FunctionNodes(t *testing.T) {
	src := `
def module_func(x):
    return x

def outer():
    def inner():
        pass
    return inner
`
	nodes, _, _ := parsePythonSrc(t, "app.py", src)

	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "module_func"), "module_func missing")
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "outer"), "outer missing")
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "inner"), "inner missing")

	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction {
			assert.Equal(t, "python", n.Language, "language tag wrong for %s", n.Label)
		}
	}
}

// TestPython_AsyncFuncNode verifies async def produces a NodeTypeFunction node.
func TestPython_AsyncFuncNode(t *testing.T) {
	src := `
async def fetch_data(url):
    return {}
`
	nodes, _, _ := parsePythonSrc(t, "app.py", src)
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "fetch_data"), "async def node missing")
}

// TestPython_ClassNode verifies class definitions produce NodeTypeClass nodes.
func TestPython_ClassNode(t *testing.T) {
	src := `
class MyService(BaseService):
    def __init__(self):
        self.data = {}

    def process(self, item):
        return item
`
	nodes, _, _ := parsePythonSrc(t, "app.py", src)

	cls := pyNode(nodes, graph.NodeTypeClass, "MyService")
	require.NotNil(t, cls, "MyService class node missing")
	assert.Equal(t, "python", cls.Language)

	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "__init__"), "__init__ node missing")
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "process"), "process node missing")
}

// TestPython_ModuleLevelCallRef verifies a module-level call to an in-file
// function produces a calls edge attributed to the synthetic (module) node.
func TestPython_ModuleLevelCallRef(t *testing.T) {
	src := `
def setup():
    pass

setup()
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	var moduleID, setupID string
	for _, n := range nodes {
		if n.Label == "(module)" {
			moduleID = n.ID
		}
		if n.Type == graph.NodeTypeFunction && n.Label == "setup" {
			setupID = n.ID
		}
	}
	require.NotEmpty(t, moduleID, "(module) synthetic node missing")
	require.NotEmpty(t, setupID, "setup function node missing")

	found := false
	for _, e := range edges {
		if e.From == moduleID && e.To == setupID && e.Type == graph.EdgeTypeCalls {
			found = true
			break
		}
	}
	assert.True(t, found, "calls edge (module)→setup missing")
}

// TestPython_NestedDefAttribution verifies that a call inside a nested function
// attributes to that nested function, not the enclosing outer function.
func TestPython_NestedDefAttribution(t *testing.T) {
	src := `
def helper():
    pass

def outer():
    def inner():
        helper()
    inner()
`
	nodes, edges, _ := parsePythonSrc(t, "app.py", src)

	var innerID, helperID, outerID string
	for _, n := range nodes {
		switch {
		case n.Type == graph.NodeTypeFunction && n.Label == "inner":
			innerID = n.ID
		case n.Type == graph.NodeTypeFunction && n.Label == "helper":
			helperID = n.ID
		case n.Type == graph.NodeTypeFunction && n.Label == "outer":
			outerID = n.ID
		}
	}
	require.NotEmpty(t, innerID, "inner function node missing")
	require.NotEmpty(t, helperID, "helper function node missing")
	require.NotEmpty(t, outerID, "outer function node missing")

	helperCallerID := ""
	for _, e := range edges {
		if e.To == helperID && e.Type == graph.EdgeTypeCalls {
			helperCallerID = e.From
			break
		}
	}
	assert.Equal(t, innerID, helperCallerID, "helper() call must be attributed to inner, not outer")
}

// TestPython_UnresolvedCallRef verifies that a call to a non-in-file function
// produces an honest UnresolvedRef entry (not a silent gap).
func TestPython_UnresolvedCallRef(t *testing.T) {
	src := `
def process():
    external_library_call()
`
	_, _, unresolved := parsePythonSrc(t, "app.py", src)

	found := false
	for _, u := range unresolved {
		if u.Name == "external_library_call" && u.Kind == "call_ref" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected UnresolvedRef for external_library_call; got: %+v", unresolved)
}

// TestPython_TestFileIndexed verifies that test_*.py files are parsed and indexed
// like any other Python file (checklist item 10: test code indexed from day one).
func TestPython_TestFileIndexed(t *testing.T) {
	src := `
def add(a, b):
    return a + b

def test_add():
    result = add(1, 2)
    assert result == 3
`
	// Use test_*.py naming convention.
	nodes, edges, _ := parsePythonSrc(t, "test_math.py", src)

	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "add"), "add function not indexed in test file")
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "test_add"), "test_add function not indexed in test file")

	// test_add calls add() → must produce a calls edge.
	var addID, testAddID string
	for _, n := range nodes {
		if n.Type == graph.NodeTypeFunction && n.Label == "add" {
			addID = n.ID
		}
		if n.Type == graph.NodeTypeFunction && n.Label == "test_add" {
			testAddID = n.ID
		}
	}
	require.NotEmpty(t, addID)
	require.NotEmpty(t, testAddID)

	found := false
	for _, e := range edges {
		if e.From == testAddID && e.To == addID && e.Type == graph.EdgeTypeCalls {
			found = true
			break
		}
	}
	assert.True(t, found, "calls edge test_add→add missing")
}

// TestPython_DecoratedFunc verifies decorated functions (@decorator) are indexed.
func TestPython_DecoratedFunc(t *testing.T) {
	src := `
class MyClass:
    @classmethod
    def class_method(cls):
        pass

    @staticmethod
    def static_method():
        pass
`
	nodes, _, _ := parsePythonSrc(t, "app.py", src)

	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "class_method"), "class_method missing")
	assert.NotNil(t, pyNode(nodes, graph.NodeTypeFunction, "static_method"), "static_method missing")
}
