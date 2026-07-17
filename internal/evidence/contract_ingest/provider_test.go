package contract_ingest_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/evidence/contract_ingest"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// testdataDir returns the path to the testdata directory.
func testdataDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

// wsForDir builds a minimal WorkspaceConfig whose single service path is dir.
func wsForDir(dir string) *workspace.WorkspaceConfig {
	return &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "test-svc", Path: dir}},
	}
}

// ── OpenAPI ──────────────────────────────────────────────────────────────────

func TestOpenAPI_ParsesOperations(t *testing.T) {
	dir := filepath.Join(testdataDir(), "openapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.yaml"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)

	// Expect two operations: getGame and createGame.
	labels := labelsOf(ev.Edges)
	assert.Contains(t, labels, "get /games/*", "GET /games/{gameID} must normalize to get /games/*")
	assert.Contains(t, labels, "post /games", "POST /games must normalize to post /games")
	assert.Len(t, ev.Edges, 2, "exactly two operations")

	for _, e := range ev.Edges {
		require.NotEmpty(t, e.Sources, "every edge must carry Sources[]")
		assert.Equal(t, "contract", e.Sources[0].Provider)
		assert.Equal(t, graph.ConfidenceDeclared, e.Sources[0].Confidence)
		assert.NotEmpty(t, e.Sources[0].Ref, "Ref must name the spec operation")
		assert.Equal(t, graph.EdgeTypeHTTPCall, e.Type)
	}
}

func TestOpenAPI_Negative(t *testing.T) {
	dir := filepath.Join(testdataDir(), "openapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"negative.yaml"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "empty spec must produce zero edges")
}

// TestOpenAPI_ParamSyntaxVerifiedFlip asserts the critical join: an OpenAPI
// spec that uses {param} syntax and a static edge that uses :param syntax both
// normalize to the same wildcard key — so the reconciler marks the static edge
// as "verified" when the contract provider confirms it.
func TestOpenAPI_ParamSyntaxVerifiedFlip(t *testing.T) {
	dir := filepath.Join(testdataDir(), "openapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.yaml"} // has GET /games/{gameID}

	// Static edge uses gin-style ":gameID".
	staticNodes := []graph.Node{
		{ID: "handler-1", Type: graph.NodeTypeHTTPHandler, Label: "getGame", File: "main.go", Line: 10},
		{ID: "client-1", Type: graph.NodeTypeHTTPClient, Label: "fetchGame", File: "client.go", Line: 5},
	}
	staticEdge := graph.Edge{
		ID:         "link:client-1->handler-1",
		From:       "client-1",
		To:         "handler-1",
		Type:       graph.EdgeTypeHTTPCall,
		Label:      "get /games/*", // post-normalization key from gin ":gameID"
		Confidence: graph.ConfidenceInferred,
	}

	sp := evidence.NewStaticProvider(staticNodes, []graph.Edge{staticEdge}, nil)
	contractProv := contract_ingest.NewContractProvider()

	rec, err := evidence.NewReconciler(sp, contractProv)
	require.NoError(t, err)

	result, err := rec.Reconcile(context.Background(), ws)
	require.NoError(t, err)

	var found *graph.Edge
	for i := range result.Edges {
		if result.Edges[i].ID == "link:client-1->handler-1" {
			e := result.Edges[i]
			found = &e
			break
		}
	}
	require.NotNil(t, found, "static edge must survive reconciliation")
	assert.Equal(t, graph.StateVerified, found.VerificationState,
		"static (:param) edge must be verified when OpenAPI ({param}) spec confirms the same channel")

	// Verify the contract source was appended (not replaced).
	providers := make(map[string]bool)
	for _, s := range found.Sources {
		providers[s.Provider] = true
	}
	assert.True(t, providers["static"], "static source must be present")
	assert.True(t, providers["contract"], "contract source must be present")
}

// ── Protobuf ─────────────────────────────────────────────────────────────────

func TestProtobuf_ParsesService(t *testing.T) {
	dir := filepath.Join(testdataDir(), "protobuf")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.proto"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)

	labels := labelsOf(ev.Edges)
	assert.Contains(t, labels, "/GameService/GetGame")
	assert.Contains(t, labels, "/GameService/ListGames")
	assert.Len(t, ev.Edges, 2)

	for _, e := range ev.Edges {
		assert.Equal(t, graph.EdgeTypeGRPCCall, e.Type)
		assert.Equal(t, "contract", e.Sources[0].Provider)
		assert.Equal(t, graph.ConfidenceDeclared, e.Sources[0].Confidence)
	}
}

func TestProtobuf_Negative(t *testing.T) {
	dir := filepath.Join(testdataDir(), "protobuf")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"negative.proto"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "no service definitions → zero edges")
}

// ── GraphQL ───────────────────────────────────────────────────────────────────

func TestGraphQL_ParsesSchema(t *testing.T) {
	dir := filepath.Join(testdataDir(), "graphql")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.graphql"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)

	labels := labelsOf(ev.Edges)
	assert.Contains(t, labels, "game", "Query.game must appear")
	assert.Contains(t, labels, "games", "Query.games must appear")
	assert.Contains(t, labels, "createGame", "Mutation.createGame must appear")
	assert.Contains(t, labels, "deleteGame", "Mutation.deleteGame must appear")
	assert.Len(t, ev.Edges, 4)

	for _, e := range ev.Edges {
		assert.Equal(t, graph.EdgeTypeGraphQLCall, e.Type)
		assert.Equal(t, "contract", e.Sources[0].Provider)
	}
}

func TestGraphQL_Negative(t *testing.T) {
	dir := filepath.Join(testdataDir(), "graphql")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"negative.graphql"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "no root types → zero edges")
}

// ── AsyncAPI ──────────────────────────────────────────────────────────────────

func TestAsyncAPI_ParsesChannels(t *testing.T) {
	dir := filepath.Join(testdataDir(), "asyncapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.yaml"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)

	labels := labelsOf(ev.Edges)
	assert.Contains(t, labels, "game-created", "publish channel must appear")
	assert.Contains(t, labels, "game-ended", "subscribe channel must appear")
	assert.Len(t, ev.Edges, 2)

	for _, e := range ev.Edges {
		assert.Equal(t, "contract", e.Sources[0].Provider)
		assert.Equal(t, graph.EdgeTypeKafkaPublish, e.Type, "kafka binding → kafka_publish edge type")
	}
}

func TestAsyncAPI_Negative(t *testing.T) {
	dir := filepath.Join(testdataDir(), "asyncapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"negative.yaml"}

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "empty channels → zero edges")
}

// ── Multi-file fan-out (bug-class rule 1) ─────────────────────────────────────

// TestContractProvider_FanOut verifies that two static edges sharing one
// channel key both receive the contract source — never only the first found.
func TestContractProvider_FanOut(t *testing.T) {
	dir := filepath.Join(testdataDir(), "openapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.yaml"} // declares GET /games/{gameID}

	sharedLabel := "get /games/*"
	staticNodes := []graph.Node{
		{ID: "site-a", File: "a.go", Line: 1},
		{ID: "site-b", File: "b.go", Line: 2},
	}
	edges := []graph.Edge{
		{ID: "e-a", From: "site-a", To: "handler", Type: graph.EdgeTypeHTTPCall, Label: sharedLabel, Confidence: graph.ConfidenceInferred},
		{ID: "e-b", From: "site-b", To: "handler", Type: graph.EdgeTypeHTTPCall, Label: sharedLabel, Confidence: graph.ConfidenceInferred},
	}

	sp := evidence.NewStaticProvider(staticNodes, edges, nil)
	contractProv := contract_ingest.NewContractProvider()
	rec, err := evidence.NewReconciler(sp, contractProv)
	require.NoError(t, err)

	result, err := rec.Reconcile(context.Background(), ws)
	require.NoError(t, err)

	byID := make(map[string]graph.Edge)
	for _, e := range result.Edges {
		byID[e.ID] = e
	}

	for _, id := range []string{"e-a", "e-b"} {
		e, ok := byID[id]
		require.True(t, ok, "edge %s must survive reconciliation", id)
		assert.Equal(t, graph.StateVerified, e.VerificationState,
			"edge %s must be verified when contract confirms its channel", id)
		// Both static and contract sources must be present.
		pset := make(map[string]bool)
		for _, s := range e.Sources {
			pset[s.Provider] = true
		}
		assert.True(t, pset["static"] && pset["contract"],
			"edge %s must have both static and contract sources", id)
	}
}

// ── Determinism (bug-class rule 2) ───────────────────────────────────────────

func TestContractProvider_Determinism(t *testing.T) {
	dir := filepath.Join(testdataDir(), "openapi")
	ws := wsForDir(dir)
	ws.Evidence.ContractGlobs = []string{"input.yaml"}

	collect := func() []byte {
		p := contract_ingest.NewContractProvider()
		ev, err := p.Collect(context.Background(), ws)
		require.NoError(t, err)
		b, err := json.Marshal(ev.Edges)
		require.NoError(t, err)
		return b
	}

	first := collect()
	second := collect()
	assert.Equal(t, first, second, "ContractProvider output must be byte-identical across runs")
}

// ── No-spec degradation ────────────────────────────────────────────────────────

func TestContractProvider_NoSpecs(t *testing.T) {
	// A service directory with no spec files → zero edges, no error.
	dir := t.TempDir()
	ws := wsForDir(dir)

	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges)
	assert.Empty(t, ev.Unresolved)
}

// ── OpenAPI unsupported-construct ledgering (bug-class rule 3) ────────────────

func TestOpenAPI_WebhooksLedgered(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(`
openapi: "3.1.0"
info:
  title: Webhook Test
  version: "1.0"
paths: {}
webhooks:
  newPet:
    post:
      operationId: onNewPet
`), 0o644))

	ws := wsForDir(dir)
	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), ws)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges, "webhook operations must not become edges")
	require.NotEmpty(t, ev.Unresolved, "webhooks must be ledgered")

	found := false
	for _, u := range ev.Unresolved {
		if u.Name == "webhooks" {
			found = true
		}
	}
	assert.True(t, found, "ledger entry for webhooks must be present")
}

// ── Nil workspace ─────────────────────────────────────────────────────────────

func TestContractProvider_NilWorkspace(t *testing.T) {
	p := contract_ingest.NewContractProvider()
	ev, err := p.Collect(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, ev.Edges)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func labelsOf(edges []graph.Edge) []string {
	labels := make([]string, 0, len(edges))
	for _, e := range edges {
		labels = append(labels, e.Label)
	}
	sort.Strings(labels)
	return labels
}
