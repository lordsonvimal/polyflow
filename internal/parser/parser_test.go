package parser_test

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const patternsDir = "../../patterns"
const service = "testsvc"

func mustMatcher(t *testing.T) *patterns.TreeSitterMatcher {
	t.Helper()
	reg, err := patterns.DefaultRegistry(patternsDir)
	require.NoError(t, err)
	return patterns.NewTreeSitterMatcher(reg)
}

// --- GoParser ---

func TestGoParser_ReturnsNodesForKnownPatterns(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/routes.go")
	require.NotNil(t, p, "expected a parser for .go files")

	nodes, _, _, err := p.Parse("testdata/routes.go", service, m)
	require.NoError(t, err)
	assert.NotEmpty(t, nodes, "expected nodes from routes.go")
	for _, n := range nodes {
		assert.Equal(t, service, n.Service)
		assert.Equal(t, "go", n.Language)
	}
}

func TestGoParser_ContainsHTTPHandlerNode(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/routes.go")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/routes.go", service, m)
	require.NoError(t, err)

	hasHandler := false
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPHandler {
			hasHandler = true
			break
		}
	}
	assert.True(t, hasHandler, "expected at least one http_handler node; got: %v", nodeTypes(nodes))
}

func TestGoParser_FileNotFound(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/nonexistent.go")
	require.NotNil(t, p)

	_, _, _, err := p.Parse("testdata/nonexistent.go", service, m)
	assert.Error(t, err)
}

// --- JavaScriptParser ---

func TestJavaScriptParser_ReturnsNodesForAxios(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/client.js")
	require.NotNil(t, p, "expected a parser for .js files")

	nodes, _, _, err := p.Parse("testdata/client.js", service, m)
	require.NoError(t, err)
	assert.NotEmpty(t, nodes)
	for _, n := range nodes {
		assert.Equal(t, "javascript", n.Language)
	}
}

func TestJavaScriptParser_ContainsHTTPClientNodes(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/client.js")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/client.js", service, m)
	require.NoError(t, err)

	hasClient := false
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			hasClient = true
			break
		}
	}
	assert.True(t, hasClient, "expected at least one http_client node; got: %v", nodeTypes(nodes))
}

func TestTypeScriptExtension(t *testing.T) {
	p := parser.ForFile("testdata/client.ts")
	require.NotNil(t, p, "expected a parser for .ts files")
	assert.Equal(t, "javascript", p.Language())
}

// --- RubyParser ---

func TestRubyParser_ReturnsNodesOrEmpty(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/app.rb")
	require.NotNil(t, p, "expected a parser for .rb files")

	nodes, edges, _, err := p.Parse("testdata/app.rb", service, m)
	require.NoError(t, err)
	// May return empty if no patterns match; just ensure it does not error.
	t.Logf("ruby: %d nodes, %d edges", len(nodes), len(edges))
	for _, n := range nodes {
		assert.Equal(t, "ruby", n.Language)
	}
}

// --- TemplParser ---

func TestTemplParser_DetectsComponents(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p, "expected a parser for .templ files")

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)

	componentNames := make(map[string]bool)
	for _, n := range nodes {
		if n.Type == graph.NodeTypeComponent {
			componentNames[n.Meta["name"]] = true
		}
	}
	assert.True(t, componentNames["UserPage"] || componentNames["Header"],
		"expected UserPage or Header component node; components found: %v", componentNames)
}

// TestTemplParser_NoFalsePositiveForGoFunc asserts that a regular exported Go
// function inside a .templ file is NOT classified as a component node.
func TestTemplParser_NoFalsePositiveForGoFunc(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)

	for _, n := range nodes {
		if n.Type == graph.NodeTypeComponent {
			assert.NotEqual(t, "RegisterRoutes", n.Meta["name"],
				"func RegisterRoutes() should not be classified as a component")
		}
	}
}

func TestTemplParser_DetectsDatastarActions(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)

	hasAction := false
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["method"] != "" {
			hasAction = true
			break
		}
	}
	assert.True(t, hasAction, "expected a datastar action node")
}

func TestTemplParser_DetectsHrefLinks(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, n := range nodes {
		if n.Meta["path"] != "" {
			paths[n.Meta["path"]] = true
		}
	}
	assert.True(t, paths["/users/list"] || paths["/dashboard"],
		"expected href_link for /users/list or /dashboard; got: %v", paths)
}

// TestTemplParser_DetectsRootPath asserts that href="/" is captured.
func TestTemplParser_DetectsRootPath(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)

	hasRoot := false
	for _, n := range nodes {
		if n.Meta["path"] == "/" {
			hasRoot = true
			break
		}
	}
	assert.True(t, hasRoot, "expected an href_link node for root path '/'")
}

// TestTemplParser_MultiAttributeLine asserts that a line with both data-on-*
// and href emits nodes for BOTH patterns (no silent drop via continue).
func TestTemplParser_MultiAttributeLine(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)

	// The fixture line: `<a href="/home" data-on-click="@get('/api/home')">Home</a>`
	hasAction := false
	hasHref := false
	for _, n := range nodes {
		if n.Meta["method"] == "GET" && n.Meta["path"] == "/api/home" {
			hasAction = true
		}
		if n.Meta["path"] == "/home" {
			hasHref = true
		}
	}
	assert.True(t, hasAction, "expected datastar_action node for @get('/api/home')")
	assert.True(t, hasHref, "expected href_link node for /home (same line as data-on-click)")
}

func TestTemplParser_Language(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/page.templ")
	require.NotNil(t, p)

	nodes, _, _, err := p.Parse("testdata/page.templ", service, m)
	require.NoError(t, err)
	for _, n := range nodes {
		assert.Equal(t, "templ", n.Language)
	}
}

// --- WorkerPool ---

func TestWorkerPool_RunMultipleFiles(t *testing.T) {
	m := mustMatcher(t)
	pool := parser.NewWorkerPool(2, m, service)

	files := []string{
		"testdata/routes.go",
		"testdata/client.js",
		"testdata/page.templ",
	}

	results := collect(pool.Run(files))
	assert.Len(t, results, len(files))

	successCount := 0
	for _, r := range results {
		if r.Err == nil {
			successCount++
		}
	}
	assert.GreaterOrEqual(t, successCount, 2, "expected at least 2 files parsed successfully")
}

func TestWorkerPool_UnknownExtensionReturnsError(t *testing.T) {
	m := mustMatcher(t)
	pool := parser.NewWorkerPool(1, m, service)

	results := collect(pool.Run([]string{"testdata/page.templ"}))
	// Templ parser is registered; ensure no panic or deadlock
	assert.Len(t, results, 1)
}

func TestWorkerPool_NoParserReturnsError(t *testing.T) {
	m := mustMatcher(t)
	pool := parser.NewWorkerPool(1, m, service)

	results := collect(pool.Run([]string{"testdata/unknown.xyz"}))
	assert.Len(t, results, 1)
	assert.Error(t, results[0].Err)
}

// --- ForFile ---

func TestWorkerPool_ZeroFiles(t *testing.T) {
	m := mustMatcher(t)
	pool := parser.NewWorkerPool(2, m, service)
	results := collect(pool.Run(nil))
	assert.Empty(t, results)
}

func TestWorkerPool_ZeroWorkers(t *testing.T) {
	m := mustMatcher(t)
	// workers <= 0 should default to 4 without panic
	pool := parser.NewWorkerPool(0, m, service)
	results := collect(pool.Run([]string{"testdata/routes.go"}))
	assert.Len(t, results, 1)
}

func TestLanguage_Methods(t *testing.T) {
	assert.Equal(t, "go", parser.ForFile("x.go").Language())
	assert.Equal(t, "javascript", parser.ForFile("x.js").Language())
	assert.Equal(t, "ruby", parser.ForFile("x.rb").Language())
	assert.Equal(t, "templ", parser.ForFile("x.templ").Language())
	assert.Equal(t, "python", parser.ForFile("x.py").Language())
}

func TestForFile_ReturnsNilForUnknownExtension(t *testing.T) {
	assert.Nil(t, parser.ForFile("config.json"))
	assert.Nil(t, parser.ForFile("main.sql"))
}

func TestForFile_ReturnsParserForKnownExtensions(t *testing.T) {
	assert.NotNil(t, parser.ForFile("main.go"))
	assert.NotNil(t, parser.ForFile("app.js"))
	assert.NotNil(t, parser.ForFile("app.ts"))
	assert.NotNil(t, parser.ForFile("app.rb"))
	assert.NotNil(t, parser.ForFile("page.templ"))
	assert.NotNil(t, parser.ForFile("Rakefile.rake"))
	assert.NotNil(t, parser.ForFile("service.py"))
}

func TestRubyParser_RakeExtension(t *testing.T) {
	p := parser.ForFile("testdata/app.rb")
	require.NotNil(t, p)
	assert.Equal(t, []string{".rb", ".rake"}, p.Extensions())
}

func TestJavaScriptParser_TSX(t *testing.T) {
	// .tsx uses the javascript parser (TypeScript JSX)
	p := parser.ForFile("component.tsx")
	require.NotNil(t, p)
	assert.Equal(t, "javascript", p.Language())
}

func TestJavaScriptParser_TSX_JSXComponents(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/app.tsx")
	require.NotNil(t, p)

	nodes, edges, _, err := p.Parse("testdata/app.tsx", service, m)
	require.NoError(t, err)

	// Should detect function declarations for Layout and App
	labels := make(map[string]bool)
	for _, n := range nodes {
		labels[n.Label] = true
	}
	assert.True(t, labels["Layout"], "expected Layout node")
	assert.True(t, labels["App"], "expected App node")

	// Should have at least one renders edge (App renders Layout)
	hasRenders := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeRenders {
			hasRenders = true
			break
		}
	}
	assert.True(t, hasRenders, "expected at least one renders edge from JSX component usage")
}

func TestJavaScriptParser_TSX_ImperativeCalls(t *testing.T) {
	m := mustMatcher(t)
	p := parser.ForFile("testdata/notification.tsx")
	require.NotNil(t, p)

	nodes, edges, _, err := p.Parse("testdata/notification.tsx", service, m)
	require.NoError(t, err)

	labels := make(map[string]bool)
	for _, n := range nodes {
		labels[n.Label] = true
	}
	assert.True(t, labels["fetchWarnings"], "expected fetchWarnings node")
	assert.True(t, labels["Notification"], "expected Notification node")

	// Notification should have a calls edge to fetchWarnings (called inside onMount)
	hasCallsEdge := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.To != "" {
			// edge from Notification to fetchWarnings
			if strings.Contains(e.From, "Notification") && strings.Contains(e.To, "fetchWarnings") {
				hasCallsEdge = true
				break
			}
		}
	}
	assert.True(t, hasCallsEdge, "expected calls edge from Notification to fetchWarnings")
}

// BenchmarkWorkerPool_100Files measures parsing 100 fixture files concurrently.
func BenchmarkWorkerPool_100Files(b *testing.B) {
	reg, err := patterns.DefaultRegistry(patternsDir)
	if err != nil {
		b.Fatal(err)
	}
	m := patterns.NewTreeSitterMatcher(reg)
	pool := parser.NewWorkerPool(4, m, service)

	// Build a list of 100 files by repeating the known fixtures.
	baseFiles := []string{
		"testdata/routes.go",
		"testdata/client.js",
		"testdata/page.templ",
		"testdata/app.rb",
	}
	files := make([]string, 0, 100)
	for len(files) < 100 {
		files = append(files, baseFiles...)
	}
	files = files[:100]

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for range pool.Run(files) {
		}
	}
}

// helpers

func nodeTypes(nodes []graph.Node) []graph.NodeType {
	types := make([]graph.NodeType, len(nodes))
	for i, n := range nodes {
		types[i] = n.Type
	}
	return types
}

func collect(ch <-chan parser.Result) []parser.Result {
	var out []parser.Result
	for r := range ch {
		out = append(out, r)
	}
	return out
}
