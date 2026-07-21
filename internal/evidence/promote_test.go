package evidence_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
)

// writeProposalFiles writes a rule YAML and fixture JSON to dir and returns
// the path to the YAML.
func writeProposalFiles(t *testing.T, dir, yamlContent, fixtureContent string) string {
	t.Helper()
	yamlPath := filepath.Join(dir, "proposal.yaml")
	fixPath := filepath.Join(dir, "proposal.json")
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlContent), 0o644))
	require.NoError(t, os.WriteFile(fixPath, []byte(fixtureContent), 0o644))
	return yamlPath
}

// validHTTPProposalYAML is a minimal rule that matches http_client → http_handler.
const validHTTPProposalYAML = `version: "1"
contracts:
  - kind: http_call
    producer:
      node: http_client
      key: [method, path]
    consumer:
      node: http_handler
      key: [method, path]
    normalizers: [param_wildcard]
    match: [normalized, wildcard_anchored]
    edge:
      type: http_call
      id_prefix: proposed
      same_service: skip
    unmatched: ledger
`

// httpFixture builds a FixtureCase JSON with an http_client producer and http_handler consumer.
func httpFixture(t *testing.T, producerSvc, consumerSvc, method, path string) string {
	t.Helper()
	fc := evidence.FixtureCase{
		Nodes: []evidence.FixtureNode{
			{
				ID:      "producer-0",
				Type:    "http_client",
				Service: producerSvc,
				Meta:    map[string]string{"method": method, "path": path},
			},
			{
				ID:      "consumer-0",
				Type:    "http_handler",
				Service: consumerSvc,
				Meta:    map[string]string{"method": method, "path": path},
			},
		},
		ExpectedEdges: []evidence.FixtureEdge{
			{From: "producer-0", To: "consumer-0", Type: "http_call"},
		},
	}
	data, err := json.Marshal(fc)
	require.NoError(t, err)
	return string(data)
}

// TestRunPromotion_Green verifies that a proposal with matching fixture promotes
// successfully, copying files to contracts/ and testdata/contracts/.
func TestRunPromotion_Green(t *testing.T) {
	proposalDir := t.TempDir()
	wsDir := t.TempDir()

	fix := httpFixture(t, "svc-a", "svc-b", "GET", "/users")
	yamlPath := writeProposalFiles(t, proposalDir, validHTTPProposalYAML, fix)

	result, err := evidence.RunPromotion(yamlPath, wsDir)
	require.NoError(t, err)
	assert.Empty(t, result.Diff, "green path must have no diff")
	assert.NotEmpty(t, result.RuleDest)
	assert.NotEmpty(t, result.FixtureDest)

	// Verify files landed in expected locations.
	assert.FileExists(t, result.RuleDest)
	assert.FileExists(t, result.FixtureDest)
	assert.DirExists(t, filepath.Join(wsDir, "contracts"))
	assert.DirExists(t, filepath.Join(wsDir, "testdata", "contracts"))

	// Originals removed from proposals dir.
	assert.NoFileExists(t, yamlPath)
}

// TestRunPromotion_Red verifies that a fixture mismatch (wrong node types) returns
// a non-empty Diff and refuses to move files.
func TestRunPromotion_Red(t *testing.T) {
	proposalDir := t.TempDir()
	wsDir := t.TempDir()

	// Fixture expects http_call edges but the nodes use wrong types that the rule
	// won't match (the rule requires http_client + http_handler).
	fc := evidence.FixtureCase{
		Nodes: []evidence.FixtureNode{
			{ID: "producer-0", Type: "TODO_producer_node_type", Service: "svc-a", Meta: map[string]string{"method": "GET", "path": "/users"}},
			{ID: "consumer-0", Type: "TODO_consumer_node_type", Service: "svc-b", Meta: map[string]string{"method": "GET", "path": "/users"}},
		},
		ExpectedEdges: []evidence.FixtureEdge{
			{From: "producer-0", To: "consumer-0", Type: "http_call"},
		},
	}
	fixData, _ := json.Marshal(fc)
	yamlPath := writeProposalFiles(t, proposalDir, validHTTPProposalYAML, string(fixData))

	result, err := evidence.RunPromotion(yamlPath, wsDir)
	require.NoError(t, err, "RunPromotion returns diff, not error, on mismatch")
	assert.NotEmpty(t, result.Diff, "red path must have a non-empty diff")
	assert.Empty(t, result.RuleDest, "no files should be moved on red")

	// Original files must still be present.
	assert.FileExists(t, yamlPath)
}

// TestRunPromotion_MissingFixture returns an error when the companion JSON is absent.
func TestRunPromotion_MissingFixture(t *testing.T) {
	dir := t.TempDir()
	wsDir := t.TempDir()
	yamlPath := filepath.Join(dir, "proposal.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(validHTTPProposalYAML), 0o644))
	// No .json companion written.

	_, err := evidence.RunPromotion(yamlPath, wsDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture not found")
}

// TestRunPromotion_InvalidYAML returns an error for a proposal that fails validation.
func TestRunPromotion_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	wsDir := t.TempDir()
	// Missing unmatched field — fails validateRule.
	badYAML := `version: "1"
contracts:
  - kind: http_call
    producer:
      node: http_client
      key: [method, path]
    consumer:
      node: http_handler
      key: [method, path]
    normalizers: []
    match: [normalized]
    edge:
      type: http_call
      id_prefix: bad
      same_service: skip
`
	// unmatched is required — load must reject.
	yamlPath := filepath.Join(dir, "bad.yaml")
	fixPath := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(yamlPath, []byte(badYAML), 0o644))
	require.NoError(t, os.WriteFile(fixPath, []byte(`{"nodes":[],"expected_edges":[]}`), 0o644))

	_, err := evidence.RunPromotion(yamlPath, wsDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid proposal")
}

// TestRunPromotion_RoundTrip tests the full propose → promote cycle using an
// observed_only_gap edge: propose generates a skeleton, operator fills in node
// types, promote validates and moves the files.
func TestRunPromotion_RoundTrip(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /orders", From: "frontend", To: "api", Sources: []string{"runtime"}},
	}

	// Step 1: Generate proposals.
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	require.Len(t, proposals, 1)

	p := proposals[0]

	// Step 2: Write proposals to a temp directory (simulates --propose).
	proposalDir := t.TempDir()
	yamlPath := filepath.Join(proposalDir, p.YAMLFilename)
	fixPath := filepath.Join(proposalDir, p.FixtureFilename)
	require.NoError(t, os.WriteFile(yamlPath, []byte(p.YAMLContent), 0o644))
	require.NoError(t, os.WriteFile(fixPath, []byte(p.FixtureContent), 0o644))

	// Step 3: "Operator" fills in the fixture — replace TODO node types and meta.
	var fc evidence.FixtureCase
	require.NoError(t, json.Unmarshal([]byte(p.FixtureContent), &fc))
	fc.Nodes[0].Type = "http_client"
	fc.Nodes[0].Meta = map[string]string{"method": "GET", "path": "/orders"}
	fc.Nodes[1].Type = "http_handler"
	fc.Nodes[1].Meta = map[string]string{"method": "GET", "path": "/orders"}
	updatedFix, err := json.MarshalIndent(fc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixPath, updatedFix, 0o644))

	// Step 4: "Operator" fills in the rule YAML — set node types and keys.
	filledYAML := `version: "1"
contracts:
  - kind: http_call
    producer:
      node: http_client
      key: [method, path]
    consumer:
      node: http_handler
      key: [method, path]
    normalizers: [param_wildcard]
    match: [normalized, wildcard_anchored]
    edge:
      type: http_call
      id_prefix: proposed
      same_service: skip
    unmatched: ledger
`
	require.NoError(t, os.WriteFile(yamlPath, []byte(filledYAML), 0o644))

	// Step 5: Promote — must be green.
	wsDir := t.TempDir()
	result, err := evidence.RunPromotion(yamlPath, wsDir)
	require.NoError(t, err)
	assert.Empty(t, result.Diff, "filled-in proposal must pass the fixture")
	assert.FileExists(t, result.RuleDest)
	assert.FileExists(t, result.FixtureDest)
}
