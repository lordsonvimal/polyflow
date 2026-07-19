package sidecar_test

// V.2 integration tests. TestMain builds the real polyflow-parse-templ
// binary once; the tests below exercise the sidecar through actual process
// spawns and pipes — the fallback tests use a genuinely missing/renamed/
// crashing binary, not a stubbed error path (pinned hazard, plan doc).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// sidecarBinDir holds the freshly built polyflow-parse-templ for this test
// run; "" when the build failed (tests needing it fail with buildErr).
var (
	sidecarBinDir string
	buildErr      error
)

func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "polyflow-sidecar-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdtemp: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "polyflow-parse-templ"), "./cmd/polyflow-parse-templ")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("build sidecar: %v\n%s", err, out)
	} else {
		sidecarBinDir = dir
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func builtSidecarDir(t *testing.T) string {
	t.Helper()
	if sidecarBinDir == "" {
		t.Fatalf("sidecar binary unavailable: %v", buildErr)
	}
	return sidecarBinDir
}

// fixtureTempl is a real datastar-v1 fixture exercised through the full
// parse path (bug-class rule 6: no hand-constructed nodes).
func fixtureTempl(t *testing.T) (path string, content []byte) {
	t.Helper()
	path = filepath.Join(repoRoot(), "internal", "parser", "testdata", "datastar.templ")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return path, content
}

func parseRequest(t *testing.T, file string, content []byte) []byte {
	t.Helper()
	req := sidecar.ParseRequest{
		File:       file,
		ContentB64: base64.StdEncoding.EncodeToString(content),
		Tool:       "templ",
		Version:    "0.3.1020",
		Service:    "websvc",
	}
	payload, err := json.Marshal(req)
	require.NoError(t, err)
	return payload
}

// Two-run determinism (bug-class rule 2): the same file parsed twice through
// the real sidecar must produce byte-identical response frames.
func TestSidecarTwoRunDeterminism(t *testing.T) {
	dir := builtSidecarDir(t)
	client, err := sidecar.StartClient(filepath.Join(dir, "polyflow-parse-templ"))
	require.NoError(t, err)
	defer client.Close()

	file, content := fixtureTempl(t)
	payload := parseRequest(t, file, content)

	first, err := client.RoundTrip(payload)
	require.NoError(t, err)
	second, err := client.RoundTrip(payload)
	require.NoError(t, err)
	require.True(t, bytes.Equal(first, second), "sidecar response frames differ across identical parses")

	var resp sidecar.ParseResponse
	require.NoError(t, json.Unmarshal(first, &resp))
	require.Empty(t, resp.Error)
	require.NotEmpty(t, resp.Nodes, "fixture must produce nodes — an empty parse proves nothing")
}

// The sidecar must reproduce the in-process parser's output exactly (same
// engine version, same code) — content equality after canonical sorting.
func TestSidecarParityWithInProcess(t *testing.T) {
	dir := builtSidecarDir(t)
	client, err := sidecar.StartClient(filepath.Join(dir, "polyflow-parse-templ"))
	require.NoError(t, err)
	defer client.Close()

	file, content := fixtureTempl(t)
	respBytes, err := client.RoundTrip(parseRequest(t, file, content))
	require.NoError(t, err)
	var got sidecar.ParseResponse
	require.NoError(t, json.Unmarshal(respBytes, &got))
	require.Empty(t, got.Error)

	nodes, edges, unresolved, err := (&parser.TemplParser{}).Parse(file, "websvc", nil)
	require.NoError(t, err)
	want := sidecar.ParseResponse{Nodes: nodes, Edges: edges, Unresolved: unresolved}
	want.Sort()

	assert.Equal(t, want.Nodes, got.Nodes)
	assert.Equal(t, want.Edges, got.Edges)
	assert.Equal(t, want.Unresolved, got.Unresolved)
}

// The datastar_variant field must reach the sidecar's vocabulary selection:
// a v0-hyphen fixture parsed under the v0 variant yields datastar nodes,
// under the v1 variant it must not (wrong-version negative in both shapes).
func TestSidecarDatastarVariantApplied(t *testing.T) {
	dir := builtSidecarDir(t)
	client, err := sidecar.StartClient(filepath.Join(dir, "polyflow-parse-templ"))
	require.NoError(t, err)
	defer client.Close()

	file := filepath.Join(repoRoot(), "internal", "parser", "testdata", "datastar_v0.templ")
	content, err := os.ReadFile(file)
	require.NoError(t, err)

	countHTTPClients := func(variant string) int {
		req := sidecar.ParseRequest{
			File: file, ContentB64: base64.StdEncoding.EncodeToString(content),
			Tool: "templ", Version: "0.3.1020", Service: "websvc", DatastarVariant: variant,
		}
		payload, err := json.Marshal(req)
		require.NoError(t, err)
		respBytes, err := client.RoundTrip(payload)
		require.NoError(t, err)
		var resp sidecar.ParseResponse
		require.NoError(t, json.Unmarshal(respBytes, &resp))
		require.Empty(t, resp.Error)
		n := 0
		for _, node := range resp.Nodes {
			if node.Type == graph.NodeTypeHTTPClient && node.Meta["datastar"] == "true" {
				n++
			}
		}
		return n
	}

	assert.Positive(t, countHTTPClients("datastar-v0"), "v0 fixture under v0 vocab must yield datastar actions")
	assert.Zero(t, countHTTPClients("datastar-v1"), "v0-hyphen syntax must not match under the v1 colon vocab")
}

// A request for a tool this sidecar does not parse is a labeled error frame,
// never a crash or a silent empty result.
func TestSidecarRejectsUnknownTool(t *testing.T) {
	dir := builtSidecarDir(t)
	client, err := sidecar.StartClient(filepath.Join(dir, "polyflow-parse-templ"))
	require.NoError(t, err)
	defer client.Close()

	payload, err := json.Marshal(sidecar.ParseRequest{File: "x.rb", Tool: "ruby"})
	require.NoError(t, err)
	respBytes, err := client.RoundTrip(payload)
	require.NoError(t, err)
	var resp sidecar.ParseResponse
	require.NoError(t, json.Unmarshal(respBytes, &resp))
	assert.Contains(t, resp.Error, "unsupported tool")
}

func templVersions() map[toolchain.Tool]string {
	return map[toolchain.Tool]string{toolchain.ToolTempl: "0.3.1020", toolchain.ToolDatastar: "1.1.0"}
}

// Real missing binary: an empty sidecar dir. The router must fall back to
// the in-process parser (same output), record one coverage note, and drop
// zero files.
func TestRouterFallbackMissingBinary(t *testing.T) {
	mgr := sidecar.NewManager(t.TempDir()) // empty dir — binary genuinely absent
	defer mgr.Shutdown()
	router := sidecar.NewRouter(mgr, toolchain.DefaultRegistry(), "websvc", templVersions())

	file, _ := fixtureTempl(t)
	p := router.ParserFor(file)
	require.NotNil(t, p, "templ files must be routed")

	nodes, edges, _, err := p.Parse(file, "websvc", nil)
	require.NoError(t, err, "fallback parse must succeed")
	require.NotEmpty(t, nodes)

	wantNodes, wantEdges, _, err := (&parser.TemplParser{}).Parse(file, "websvc", nil)
	require.NoError(t, err)
	assert.Equal(t, wantNodes, nodes, "fallback output must equal the in-process parse")
	assert.Equal(t, wantEdges, edges)

	notes := router.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, toolchain.ToolTempl, notes[0].Tool)
	assert.Equal(t, "in-process", notes[0].UsedProfile)
	assert.Contains(t, notes[0].Note, "unavailable")

	// Second file through the same router: still parsed, note not duplicated.
	_, _, _, err = p.Parse(file, "websvc", nil)
	require.NoError(t, err)
	assert.Len(t, router.Notes(), 1)
}

// Renamed binary (pinned hazard wording): the sidecar works when present,
// and a fresh run after renaming it away falls back with a note.
func TestRouterFallbackRenamedBinary(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(builtSidecarDir(t), "polyflow-parse-templ"))
	require.NoError(t, err)
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyflow-parse-templ")
	require.NoError(t, os.WriteFile(bin, src, 0o755))

	file, _ := fixtureTempl(t)

	mgr := sidecar.NewManager(dir)
	router := sidecar.NewRouter(mgr, toolchain.DefaultRegistry(), "websvc", templVersions())
	nodes, _, _, err := router.ParserFor(file).Parse(file, "websvc", nil)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)
	assert.Empty(t, router.Notes(), "sidecar present — no fallback note expected")
	mgr.Shutdown()

	require.NoError(t, os.Rename(bin, bin+".renamed"))

	mgr2 := sidecar.NewManager(dir)
	defer mgr2.Shutdown()
	router2 := sidecar.NewRouter(mgr2, toolchain.DefaultRegistry(), "websvc", templVersions())
	nodes2, _, _, err := router2.ParserFor(file).Parse(file, "websvc", nil)
	require.NoError(t, err)
	// The sidecar sorts by ID before framing; the in-process fallback emits
	// visitor order — canonicalize both for the content comparison.
	canon := sidecar.ParseResponse{Nodes: nodes2}
	canon.Sort()
	assert.Equal(t, nodes, canon.Nodes, "fallback must produce the same graph content")
	notes := router2.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, "in-process", notes[0].UsedProfile)
}

// Dead sidecar: a binary that exits immediately. RoundTrip fails, the
// router falls back, the captured stderr lands in the coverage note.
func TestRouterFallbackDeadSidecar(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho boom >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "polyflow-parse-templ"), []byte(script), 0o755))

	mgr := sidecar.NewManager(dir)
	defer mgr.Shutdown()
	router := sidecar.NewRouter(mgr, toolchain.DefaultRegistry(), "websvc", templVersions())

	file, _ := fixtureTempl(t)
	nodes, _, _, err := router.ParserFor(file).Parse(file, "websvc", nil)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)

	notes := router.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, "in-process", notes[0].UsedProfile)
	assert.Contains(t, notes[0].Note, "boom", "sidecar stderr must be captured into the note")
}

// Non-sidecar'd files are never routed.
func TestRouterOnlyRoutesTemplFiles(t *testing.T) {
	mgr := sidecar.NewManager(t.TempDir())
	defer mgr.Shutdown()
	router := sidecar.NewRouter(mgr, toolchain.DefaultRegistry(), "websvc", templVersions())
	assert.Nil(t, router.ParserFor("main.go"))
	assert.Nil(t, router.ParserFor("app.js"))
	assert.Empty(t, router.Notes(), "non-templ dispatch must not record templ notes")
}

// An out-of-range templ version routes to the nearest-newest sidecar with
// an inferred-selection coverage note (fail-safe, never an error).
func TestRouterInferredVersionStillRoutes(t *testing.T) {
	dir := builtSidecarDir(t)
	mgr := sidecar.NewManager(dir)
	defer mgr.Shutdown()
	versions := map[toolchain.Tool]string{toolchain.ToolTempl: "0.2.0"} // below every registry row
	router := sidecar.NewRouter(mgr, toolchain.DefaultRegistry(), "websvc", versions)

	file, _ := fixtureTempl(t)
	nodes, _, _, err := router.ParserFor(file).Parse(file, "websvc", nil)
	require.NoError(t, err)
	require.NotEmpty(t, nodes)

	notes := router.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, "templ-v0.3", notes[0].UsedProfile)
	assert.Contains(t, notes[0].Note, "nearest-newest")
}

// templWorkspace builds a one-service workspace containing a templ file
// whose go.mod resolves real templ/datastar versions.
func templWorkspace(t *testing.T) *workspace.WorkspaceConfig {
	t.Helper()
	dir := t.TempDir()
	svc := filepath.Join(dir, "web")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	gomod := `module example.com/web

go 1.22

require (
	github.com/a-h/templ v0.3.1020
	github.com/starfederation/datastar-go v1.1.0
)
`
	require.NoError(t, os.WriteFile(filepath.Join(svc, "go.mod"), []byte(gomod), 0o644))
	_, content := fixtureTempl(t)
	require.NoError(t, os.WriteFile(filepath.Join(svc, "page.templ"), content, 0o644))
	return &workspace.WorkspaceConfig{
		Name: "sidecar-test", Version: "1",
		Services: []workspace.Service{{Name: "web", Path: svc, Language: "go"}},
	}
}

// Full-pipeline fallback accounting: with the sidecar binary absent, an
// indexer run parses every file in-process (zero dropped, zero errors) and
// the coverage note reaches the stored ledger.
func TestIndexerFallbackMissingSidecar(t *testing.T) {
	t.Setenv(sidecar.SidecarDirEnv, t.TempDir()) // empty dir — binary absent
	cfg := templWorkspace(t)
	dbDir := t.TempDir()
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config: cfg, DBDir: dbDir, PatternsDir: filepath.Join(repoRoot(), "patterns"), Workers: 2, Full: true,
	})
	require.NoError(t, err)
	assert.Zero(t, stats.ErrorFiles, "sidecar absence must not error any file")
	assert.Equal(t, stats.TotalFiles, stats.ParsedFiles)
	assert.Positive(t, stats.Nodes)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	covJSON, err := store.GetMeta(context.Background(), "toolchain_coverage")
	require.NoError(t, err)
	var notes []toolchain.CoverageNote
	require.NoError(t, json.Unmarshal([]byte(covJSON), &notes))
	found := false
	for _, n := range notes {
		if n.Tool == toolchain.ToolTempl && n.UsedProfile == "in-process" {
			found = true
		}
	}
	assert.True(t, found, "in-process fallback must be ledgered in toolchain_coverage, got: %s", covJSON)
}

// With the sidecar present, the indexer routes templ files through it (no
// fallback note) and stamps the backend/profile selections into graph meta.
func TestIndexerSidecarUsedAndProfilesStamped(t *testing.T) {
	t.Setenv(sidecar.SidecarDirEnv, builtSidecarDir(t))
	cfg := templWorkspace(t)
	dbDir := t.TempDir()
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config: cfg, DBDir: dbDir, PatternsDir: filepath.Join(repoRoot(), "patterns"), Workers: 2, Full: true,
	})
	require.NoError(t, err)
	assert.Zero(t, stats.ErrorFiles)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()

	covJSON, err := store.GetMeta(context.Background(), "toolchain_coverage")
	require.NoError(t, err)
	var notes []toolchain.CoverageNote
	require.NoError(t, json.Unmarshal([]byte(covJSON), &notes))
	for _, n := range notes {
		assert.NotEqual(t, "in-process", n.UsedProfile,
			"sidecar present — no fallback note expected, got: %s", covJSON)
	}

	profJSON, err := store.GetMeta(context.Background(), "toolchain_profiles")
	require.NoError(t, err)
	var profiles map[string]map[string]struct {
		Profile  string `json:"profile"`
		Version  string `json:"version"`
		Inferred bool   `json:"inferred"`
	}
	require.NoError(t, json.Unmarshal([]byte(profJSON), &profiles))
	web := profiles["web"]
	require.NotNil(t, web)
	assert.Equal(t, "templ-v0.3", web["templ"].Profile)
	assert.Equal(t, "0.3.1020", web["templ"].Version)
	assert.False(t, web["templ"].Inferred)
	assert.Equal(t, "datastar-v1", web["datastar"].Profile)
}
