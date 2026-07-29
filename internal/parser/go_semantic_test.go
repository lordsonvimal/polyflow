package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeGoModule lays out a minimal two-file Go module with a cross-file call
// (main → helper) and returns its directory.
func writeGoModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	helper()
}
`,
		"util.go": `package main

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestGoSemanticRelativeNodePaths is the regression test for the empty call
// graph bug: the indexer stores workspace-relative file paths in node IDs
// while go/packages reports absolute positions. The analyzer must still
// resolve functions and emit the main → helper edge.
func TestGoSemanticRelativeNodePaths(t *testing.T) {
	dir := writeGoModule(t)
	t.Chdir(dir) // node IDs below are relative to the workspace root, like the indexer's

	known := map[string]bool{
		"svc:main.go:function:main:3":   true,
		"svc:util.go:function:helper:3": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	found := false
	for _, e := range res.Edges {
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:util.go:function:helper:3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main → helper call edge, got %d edges: %+v", len(res.Edges), res.Edges)
	}
}

// TestGoSemanticGoroutineWorkerOutflow: calls inside a `go func(){…}` body
// must flow out of the worker node (when one exists at the literal's line),
// and the spawn itself must be a spawns edge deduplicating with the
// tree-sitter pattern edge — not a semantic calls edge.
func TestGoSemanticGoroutineWorkerOutflow(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	go func() {
		helper()
	}()
}

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	workerID := "svc:main.go:worker:goroutine_anon:4"
	known := map[string]bool{
		"svc:main.go:function:main:3":   true,
		"svc:main.go:function:helper:9": true,
		workerID:                        true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var workerCallsHelper, mainSpawnsWorker, mainCallsHelper bool
	for _, e := range res.Edges {
		if e.From == workerID && e.To == "svc:main.go:function:helper:9" && e.Type == "calls" {
			workerCallsHelper = true
		}
		if e.From == "svc:main.go:function:main:3" && e.To == workerID {
			if e.Type != "spawns" {
				t.Fatalf("main → worker edge must be spawns, got %s", e.Type)
			}
			if e.ID != "spawns:svc:main.go:function:main:3->"+workerID {
				t.Fatalf("spawns edge ID must match the pattern edge for dedup, got %s", e.ID)
			}
			mainSpawnsWorker = true
		}
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:main.go:function:helper:9" {
			mainCallsHelper = true
		}
	}
	if !workerCallsHelper {
		t.Fatalf("expected worker → helper calls edge, got: %+v", res.Edges)
	}
	if !mainSpawnsWorker {
		t.Fatalf("expected main → worker spawns edge, got: %+v", res.Edges)
	}
	if mainCallsHelper {
		t.Fatalf("goroutine body call must not attribute to main, got: %+v", res.Edges)
	}
}

// TestGoSemanticClosureFallback: anonymous functions with no worker node at
// their line (plain closures) keep the old behaviour — body calls attribute
// to the parent function via name-stripping.
func TestGoSemanticClosureFallback(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	f := func() {
		helper()
	}
	f()
}

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:3":    true,
		"svc:main.go:function:helper:10": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	found := false
	for _, e := range res.Edges {
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:main.go:function:helper:10" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main → helper edge via closure fallback, got: %+v", res.Edges)
	}
}

// TestGoSemanticReferenced: functions referenced without being called must be
// reported for callback classification — a function value stored in a
// package-level composite literal (the cobra RunE shape) and methods
// satisfying an external interface (sort.Interface here; templ's Visitor in
// production). A plain unreferenced function must NOT be reported.
func TestGoSemanticReferenced(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

import "sort"

type command struct{ run func() error }

var cmd = command{run: runIndex}

func runIndex() error { return nil }

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func deadCode() {}

func main() {
	_ = cmd
	sort.Sort(byLen{"a"})
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:21":     true,
		"svc:main.go:function:runIndex:9":  true,
		"svc:main.go:function:deadCode:19": true,
		"svc:main.go:method:Len:13":        true,
		"svc:main.go:method:Less:14":       true,
		"svc:main.go:method:Swap:15":       true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	ref := map[string]bool{}
	for _, id := range res.Referenced {
		ref[id] = true
	}
	if !ref["svc:main.go:function:runIndex:9"] {
		t.Errorf("runIndex stored in a composite literal must be referenced; got %v", res.Referenced)
	}
	for _, m := range []string{"Len", "Less", "Swap"} {
		found := false
		for id := range ref {
			if strings.Contains(id, ":method:"+m+":") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s satisfies sort.Interface (external) and must be referenced; got %v", m, res.Referenced)
		}
	}
	if ref["svc:main.go:function:deadCode:19"] {
		t.Errorf("deadCode must not be referenced; got %v", res.Referenced)
	}
}

// TestGoSemanticTemplRenders is the T.4 fixture: a `Component(args).Render(ctx,
// w)` call site must emit a renders edge from the enclosing func to the .templ
// component node (resolved through the generated `_templ.go` twin). When the
// same function also opens a Datastar SSE stream, the renders edge is tagged
// sse=true and mirrored by an sse_endpoint edge. A struct method named Render
// with a non-templ signature must NOT produce a renders edge.
func TestGoSemanticTemplRenders(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"templ/templ.go": `package templ

import (
	"context"
	"io"
)

type Component interface {
	Render(ctx context.Context, w io.Writer) error
}
`,
		"datastar/datastar.go": `package datastar

type SSE struct{}

func NewSSE() *SSE { return &SSE{} }
`,
		// The generated twin: PuzzleRows lives in a *_templ.go file and returns
		// templ.Component, exactly like templ codegen output.
		"view_templ.go": `package main

import (
	"context"
	"io"

	"example.com/semtest/templ"
)

type comp struct{}

func (comp) Render(ctx context.Context, w io.Writer) error { return nil }

func PuzzleRows() templ.Component { return comp{} }
`,
		"handler.go": `package main

import (
	"bytes"
	"context"

	"example.com/semtest/datastar"
)

func Rows() {
	var buf bytes.Buffer
	PuzzleRows().Render(context.Background(), &buf)
	_ = datastar.NewSSE()
}

func Page() {
	var buf bytes.Buffer
	PuzzleRows().Render(context.Background(), &buf)
}

type widget struct{}

func (widget) Render() {}

func Other() {
	widget{}.Render()
}
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	const compID = "svc:view.templ:component:PuzzleRows:1"
	known := map[string]bool{
		"svc:view_templ.go:function:PuzzleRows:14": true,
		"svc:handler.go:function:Rows:10":          true,
		"svc:handler.go:function:Page:16":          true,
		"svc:handler.go:function:Other:25":         true,
		compID:                                     true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var rowsRenders, rowsSSEEndpoint, pageRenders bool
	var rowsRendersSSEMeta, pageRendersSSEMeta string
	otherRenders := false
	for _, e := range res.Edges {
		if e.To == compID && e.Type == "renders" {
			switch e.From {
			case "svc:handler.go:function:Rows:10":
				rowsRenders = true
				rowsRendersSSEMeta = e.Meta["sse"]
			case "svc:handler.go:function:Page:16":
				pageRenders = true
				pageRendersSSEMeta = e.Meta["sse"]
			case "svc:handler.go:function:Other:25":
				otherRenders = true
			}
		}
		if e.From == "svc:handler.go:function:Rows:10" && e.To == compID && e.Type == "sse_endpoint" {
			rowsSSEEndpoint = true
		}
	}

	if !rowsRenders {
		t.Fatalf("expected Rows → PuzzleRows renders edge, got: %+v", res.Edges)
	}
	if rowsRendersSSEMeta != "true" {
		t.Errorf("Rows renders edge must be tagged sse=true (opens NewSSE), got meta sse=%q", rowsRendersSSEMeta)
	}
	if !rowsSSEEndpoint {
		t.Errorf("Rows streams over SSE and must emit an sse_endpoint edge to PuzzleRows, got: %+v", res.Edges)
	}
	if !pageRenders {
		t.Fatalf("expected Page → PuzzleRows renders edge, got: %+v", res.Edges)
	}
	if pageRendersSSEMeta != "" {
		t.Errorf("Page does not stream SSE; its renders edge must not be tagged sse, got sse=%q", pageRendersSSEMeta)
	}
	if otherRenders {
		t.Errorf("widget.Render() has a non-templ signature and must not produce a renders edge")
	}
}

// TestGoSemanticInterfaceDispatchDisambiguates is the regression test for a
// real precision bug: interface-invoke call sites used to resolve by method
// name alone, fanning out to every same-named method across the service
// regardless of whether its receiver type actually implements the invoked
// interface. Other.Do(x int) shares a name with Real.Do() but has a different
// signature, so Other does not implement Doer — only Real.Do may be linked.
func TestGoSemanticInterfaceDispatchDisambiguates(t *testing.T) {
	dir := t.TempDir()
	// Real and Other live in separate files (as in the real-world fan-out bug,
	// which linked call sites to same-named methods in unrelated files):
	// resolveFunc keys purely by file+name, so two same-named methods in the
	// *same* file would collide there regardless of matchesInvoke, which would
	// not isolate the behavior under test.
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

type Doer interface {
	Do()
}

type Real struct{}

func (Real) Do() {}

func run(d Doer) {
	d.Do()
}

func main() {
	run(Real{})
}
`,
		"other.go": `package main

type Other struct{}

func (Other) Do(x int) {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:15": true,
		"svc:main.go:function:run:11":  true,
		"svc:main.go:method:Do:9":      true, // Real.Do
		"svc:other.go:method:Do:5":     true, // Other.Do
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var runCallsRealDo, runCallsOtherDo bool
	for _, e := range res.Edges {
		if e.From != "svc:main.go:function:run:11" {
			continue
		}
		switch e.To {
		case "svc:main.go:method:Do:9":
			runCallsRealDo = true
		case "svc:other.go:method:Do:5":
			runCallsOtherDo = true
		}
	}
	if !runCallsRealDo {
		t.Fatalf("expected run -> Real.Do calls edge (Real implements Doer), got: %+v", res.Edges)
	}
	if runCallsOtherDo {
		t.Fatalf("run -> Other.Do must NOT be linked: Other.Do(x int) does not implement Doer.Do(), only the method name matches (regression for the interface-dispatch fan-out bug)")
	}
}

// TestGoSemanticIntegrationTaggedTestFileIncluded is the regression test for
// a real recall bug: *_test.go files gated behind a build tag like
// `//go:build integration` are invisible to go/packages under default build
// constraints, so real test-file callers in such files were silently missing
// from the call graph entirely — not just deprioritized, absent. The analyzer
// must discover and force-enable such tags so these files are analyzed too.
func TestGoSemanticIntegrationTaggedTestFileIncluded(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func Helper() {}

func main() {}
`,
		"helper_integration_test.go": `//go:build integration

package main

import "testing"

func TestHelperIntegration(t *testing.T) {
	Helper()
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:Helper:3":                                   true,
		"svc:main.go:function:main:5":                                     true,
		"svc:helper_integration_test.go:function:TestHelperIntegration:7": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	found := false
	for _, e := range res.Edges {
		if e.From == "svc:helper_integration_test.go:function:TestHelperIntegration:7" && e.To == "svc:main.go:function:Helper:3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected integration-tagged test file's call to Helper to be captured, got: %+v", res.Edges)
	}
}

// TestGoSemanticConflictingBuildTagDeclined is the regression test for the
// widening safety net: a tag found on a _test.go file can also gate a
// *production* file that conflicts with another always-on file (a
// compile-time feature toggle between two competing implementations, e.g.
// `legacy_x`). Enabling such a tag blindly would break the whole package's
// build. The analyzer must detect the conflict, decline the tag, and fall
// back to the untagged baseline cleanly — no warning, no crash, and no false
// call edge from the now-invisible gated test file.
func TestGoSemanticConflictingBuildTagDeclined(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func Helper() {}

func main() {}
`,
		"legacy_stub.go": `package main

type Thing struct{}
`,
		"real_impl.go": `//go:build legacy_thing

package main

type Thing struct{}
`,
		"real_impl_test.go": `//go:build legacy_thing

package main

import "testing"

func TestReal(t *testing.T) {
	Helper()
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:Helper:3":             true,
		"svc:main.go:function:main:5":               true,
		"svc:real_impl_test.go:function:TestReal:7": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("expected a clean baseline fallback with no warning, got: %s", res.Warning)
	}

	for _, e := range res.Edges {
		if e.From == "svc:real_impl_test.go:function:TestReal:7" {
			t.Fatalf("legacy_thing conflicts with legacy_stub.go's Thing declaration and must be declined, but TestReal's edge was emitted: %+v", e)
		}
	}
}

// TestGoSemanticLazyExternalInterfaceStubs is the Y.1 regression: a synthetic
// external-interface node must be minted only when an in-service struct
// actually satisfies it. A struct implementing io.Reader yields exactly one
// io.Reader node + implements edge; the many other exported interfaces in the
// imported io/encoding/json packages (io.Writer, json.Marshaler, …) that
// nothing implements must leave NO dangling stub nodes.
func TestGoSemanticLazyExternalInterfaceStubs(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

import (
	"encoding/json"
	"io"
)

type myReader struct{}

func (m myReader) Read(p []byte) (int, error) { return 0, nil }

func main() {
	var r io.Reader = myReader{}
	_ = r
	_, _ = json.Marshal(0)
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:12": true,
		"svc:main.go:method:Read:10":   true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var ifaceNodes []string
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeInterface {
			ifaceNodes = append(ifaceNodes, n.Label)
		}
	}
	// Only io.Reader (the satisfied interface) may be minted — no io.Writer,
	// json.Marshaler, or any other unimplemented external interface.
	if len(ifaceNodes) != 1 || ifaceNodes[0] != "io.Reader" {
		t.Fatalf("expected exactly one interface node [io.Reader], got %v", ifaceNodes)
	}

	// The implements edge must target the minted io.Reader node.
	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeImplements && strings.HasSuffix(e.To, "interface:io.Reader:0") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected implements edge → io.Reader, got %d edges", len(res.Edges))
	}
}

// TestGoSemanticZeroResolutionWarns ensures the analyzer fails loudly instead
// of silently returning an empty edge set when no function matches the node
// index (e.g. a future path-format regression).
func TestGoSemanticZeroResolutionWarns(t *testing.T) {
	dir := writeGoModule(t)

	known := map[string]bool{
		"svc:does/not/exist.go:function:main:1": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning == "" {
		t.Fatalf("expected zero-resolution warning, got %d edges and no warning", len(res.Edges))
	}
}

// TestGoSemanticConstReadEdges (Y.2): package-level consts are compile-time
// folded, so SSA never surfaces a load — the typed-AST Uses pass must emit a
// reads edge from each function that references a same-package const. An
// unreferenced const must stay dangling (unused is a real state, not a bug: #12).
func TestGoSemanticConstReadEdges(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/consttest\n\ngo 1.25.0\n",
		"main.go": `package main

const Foo = 3

const Bar = 5

func useA() int {
	return Foo + 1
}

func useB() int {
	return Foo * 2
}

func main() {
	_ = useA()
	_ = useB()
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	const (
		fooID  = "svc:main.go:variable:Foo:3"
		barID  = "svc:main.go:variable:Bar:5"
		useAID = "svc:main.go:function:useA:7"
		useBID = "svc:main.go:function:useB:11"
	)
	known := map[string]bool{
		useAID:                         true,
		useBID:                         true,
		"svc:main.go:function:main:15": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	readsToFoo := map[string]bool{}
	readsToBar := 0
	for _, e := range res.Edges {
		if e.Type != graph.EdgeTypeReads {
			continue
		}
		if e.To == fooID {
			readsToFoo[e.From] = true
		}
		if e.To == barID {
			readsToBar++
		}
	}
	if !readsToFoo[useAID] || !readsToFoo[useBID] || len(readsToFoo) != 2 {
		t.Fatalf("expected reads edges to Foo from exactly {useA, useB}, got %v", readsToFoo)
	}
	if readsToBar != 0 {
		t.Fatalf("expected zero reads edges to unused const Bar, got %d", readsToBar)
	}

	// Bar's node must still exist (it is a real, if unused, package member) and
	// must genuinely dangle — no fabricated edge (#12).
	var barPresent bool
	for _, n := range res.Nodes {
		if n.ID == barID {
			barPresent = true
		}
	}
	if !barPresent {
		t.Fatalf("expected const Bar node %q to be minted", barID)
	}
	for _, e := range res.Edges {
		if e.From == barID || e.To == barID {
			t.Fatalf("unused const Bar must dangle, got edge %+v", e)
		}
	}
}

// TestGoSemanticSSEWrapperAndPatchArg locks Tier W-SSE: a handler that opens its
// SSE stream through an in-service forwarder (views.NewSSE → datastar.NewSSE,
// WS.1) and renders a templ fragment by passing it to a datastar patch method
// (sse.PatchElementTempl(Fragment()), WS.2) must emit a renders{sse:true} edge
// and a mirrored sse_endpoint edge to the fragment component — neither of which
// the direct-Render / direct-NewSSE detector captured. A handler that opens SSE
// directly but only pushes signals (PatchSignals) must NOT gain a fabricated
// fragment edge.
func TestGoSemanticSSEWrapperAndPatchArg(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/ssetest\n\ngo 1.25.0\n",
		"templ/templ.go": `package templ

import (
	"context"
	"io"
)

type Component interface {
	Render(ctx context.Context, w io.Writer) error
}
`,
		// datastar-go shape: NewSSE returns *ServerSentEventGenerator, whose
		// PatchElementTempl renders a templ component passed as an argument and
		// PatchSignals pushes signals (no fragment).
		// datastar-go declares its OWN component interface (TemplComponent), so a
		// templ.Component passed to PatchElementTempl is an interface→interface
		// ChangeType over the templ call — the real WS.2 shape templComponentFor
		// must unwrap.
		"datastar/datastar.go": `package datastar

import (
	"context"
	"io"
)

type TemplComponent interface {
	Render(ctx context.Context, w io.Writer) error
}

type ServerSentEventGenerator struct{}

func NewSSE() *ServerSentEventGenerator { return &ServerSentEventGenerator{} }

func (s *ServerSentEventGenerator) PatchElementTempl(c TemplComponent) {}
func (s *ServerSentEventGenerator) PatchSignals(m map[string]any) {}
`,
		// In-service SSE-constructor forwarder (WS-a).
		"views/sse.go": `package views

import "example.com/ssetest/datastar"

func NewSSE() *datastar.ServerSentEventGenerator { return datastar.NewSSE() }
`,
		// Generated templ twin for the Fragment component.
		"fragment_templ.go": `package main

import (
	"context"
	"io"

	"example.com/ssetest/templ"
)

type frag struct{}

func (frag) Render(ctx context.Context, w io.Writer) error { return nil }

func Fragment() templ.Component { return frag{} }
`,
		"handler.go": `package main

import (
	"example.com/ssetest/datastar"
	"example.com/ssetest/views"
)

func Stream() {
	sse := views.NewSSE()
	sse.PatchElementTempl(Fragment())
}

func Signals() {
	sse := datastar.NewSSE()
	sse.PatchSignals(map[string]any{"x": 1})
}
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	const compID = "svc:fragment.templ:component:Fragment:1"
	known := map[string]bool{
		"svc:fragment_templ.go:function:Fragment:14": true,
		"svc:handler.go:function:Stream:8":           true,
		"svc:handler.go:function:Signals:13":         true,
		"svc:views/sse.go:function:NewSSE:5":         true,
		compID:                                       true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var streamRenders, streamSSE bool
	var streamSSEMeta string
	for _, e := range res.Edges {
		if e.From == "svc:handler.go:function:Stream:8" && e.To == compID {
			switch e.Type {
			case "renders":
				streamRenders = true
				streamSSEMeta = e.Meta["sse"]
			case "sse_endpoint":
				streamSSE = true
			}
		}
		// Signals pushes no templ fragment — it must not draw any edge to compID.
		if e.From == "svc:handler.go:function:Signals:13" && e.To == compID {
			t.Errorf("Signals() pushes only signals and must not render a fragment, got %+v", e)
		}
	}

	if !streamRenders {
		t.Fatalf("expected Stream → Fragment renders edge (WS.2 PatchElementTempl arg), got: %+v", res.Edges)
	}
	if streamSSEMeta != "true" {
		t.Errorf("Stream opens SSE via views.NewSSE wrapper (WS.1); renders edge must be sse=true, got sse=%q", streamSSEMeta)
	}
	if !streamSSE {
		t.Errorf("Stream must emit an sse_endpoint edge to Fragment, got: %+v", res.Edges)
	}
}
