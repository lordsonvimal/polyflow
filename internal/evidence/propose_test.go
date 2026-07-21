package evidence_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/evidence"
)

// TestProposeWithFixtures_Clustering verifies that multiple gaps with the same
// (kind, key) merge into one proposal (bug-class rule 1: fan-out into the same
// proposal cluster, not duplicated).
func TestProposeWithFixtures_Clustering(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /users", From: "svc-a", To: "svc-b", Sources: []string{"runtime"}},
		{Kind: "http_call", Key: "GET /users", From: "svc-a", To: "svc-c", Sources: []string{"contract"}}, // same channel
		{Kind: "job_enqueue", Key: "send_email", From: "svc-x", To: "svc-y", Sources: []string{"runtime"}},
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	require.Len(t, proposals, 2, "two unique (kind,key) channels → two proposals")

	// Sorted by (kind, key): http_call < job_enqueue.
	assert.Equal(t, 1, proposals[0].Position)
	assert.Equal(t, 2, proposals[1].Position)
	assert.Contains(t, proposals[0].YAMLFilename, "http-call")
	assert.Contains(t, proposals[1].YAMLFilename, "job-enqueue")
}

// TestProposeWithFixtures_SortedByKindKey ensures positions are stable regardless
// of input order.
func TestProposeWithFixtures_SortedByKindKey(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "job_enqueue", Key: "send_email", From: "a", To: "b"},
		{Kind: "http_call", Key: "GET /users", From: "x", To: "y"},
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	require.Len(t, proposals, 2)

	// http_call < job_enqueue alphabetically.
	assert.Equal(t, 1, proposals[0].Position)
	assert.Contains(t, proposals[0].YAMLFilename, "1-")
	assert.Contains(t, proposals[0].YAMLFilename, "http-call")
	assert.Equal(t, 2, proposals[1].Position)
	assert.Contains(t, proposals[1].YAMLFilename, "2-")
}

// TestProposeWithFixtures_Determinism runs twice on the same input and checks
// byte-identical output (bug-class rule 2).
func TestProposeWithFixtures_Determinism(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /users", From: "svc-a", To: "svc-b", Sources: []string{"runtime"}},
		{Kind: "amqp", Key: "events.created", From: "svc-p", To: "svc-c"},
	}

	p1, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	p2, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)

	require.Equal(t, len(p1), len(p2))
	for i := range p1 {
		assert.Equal(t, p1[i].YAMLFilename, p2[i].YAMLFilename, "filenames must be identical")
		assert.Equal(t, p1[i].YAMLContent, p2[i].YAMLContent, "YAML content must be byte-identical")
		assert.Equal(t, p1[i].FixtureFilename, p2[i].FixtureFilename, "fixture filenames must be identical")
		assert.Equal(t, p1[i].FixtureContent, p2[i].FixtureContent, "fixture content must be byte-identical")
	}
}

// TestProposeWithFixtures_YAMLValidates verifies every generated YAML passes the
// contract loader's validation (rule 3, docs/phases.md: reject parsed-but-unenforced).
func TestProposeWithFixtures_YAMLValidates(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /api/users", From: "svc-a", To: "svc-b"},
		{Kind: "job_enqueue", Key: "send_report", From: "svc-x", To: "svc-y"},
		{Kind: "amqp", Key: "order.created", From: "svc-p", To: "svc-c"},
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)

	for _, p := range proposals {
		rules, parseErr := contract.ParseAndValidateBytes([]byte(p.YAMLContent))
		assert.NoError(t, parseErr, "proposal %s must pass contract.ParseAndValidateBytes", p.YAMLFilename)
		assert.NotEmpty(t, rules, "proposal %s must contain at least one rule", p.YAMLFilename)
	}
}

// TestProposeWithFixtures_NoGaps returns empty slice without error.
func TestProposeWithFixtures_NoGaps(t *testing.T) {
	proposals, err := evidence.ProposeWithFixtures(nil)
	require.NoError(t, err)
	assert.Empty(t, proposals)
}

// TestProposeWithFixtures_FixtureStructure verifies fixture JSON has nodes and expected_edges.
func TestProposeWithFixtures_FixtureStructure(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /orders", From: "frontend", To: "api"},
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	require.Len(t, proposals, 1)

	p := proposals[0]
	assert.Equal(t, strings.Replace(p.YAMLFilename, ".yaml", ".json", 1), p.FixtureFilename)

	var fc evidence.FixtureCase
	require.NoError(t, json.Unmarshal([]byte(p.FixtureContent), &fc))
	require.Len(t, fc.Nodes, 2)
	assert.Equal(t, "producer-0", fc.Nodes[0].ID)
	assert.Equal(t, "consumer-0", fc.Nodes[1].ID)
	assert.Equal(t, "frontend", fc.Nodes[0].Service)
	assert.Equal(t, "api", fc.Nodes[1].Service)
	require.Len(t, fc.ExpectedEdges, 1)
	assert.Equal(t, "producer-0", fc.ExpectedEdges[0].From)
	assert.Equal(t, "consumer-0", fc.ExpectedEdges[0].To)
	assert.Equal(t, "http_call", fc.ExpectedEdges[0].Type)
}

// TestProposeWithFixtures_FilenamePositionPrefix verifies filenames include "<n>-" prefix.
func TestProposeWithFixtures_FilenamePositionPrefix(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /a"},
		{Kind: "http_call", Key: "GET /b"},
		{Kind: "http_call", Key: "GET /c"},
	}
	proposals, err := evidence.ProposeWithFixtures(gaps)
	require.NoError(t, err)
	require.Len(t, proposals, 3)

	for i, p := range proposals {
		prefix := string(rune('1'+i)) + "-"
		assert.True(t, strings.HasPrefix(p.YAMLFilename, prefix), "filename %q must start with %s", p.YAMLFilename, prefix)
	}
}

// TestSlugify verifies the slug function handles various inputs.
func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http_call", "http-call"},
		{"GET /api/users", "get-api-users"},
		{"job.enqueue", "job-enqueue"},
		{"  leading  ", "leading"},
		{"a--b", "a-b"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, evidence.Slugify(c.in), "Slugify(%q)", c.in)
	}
}
