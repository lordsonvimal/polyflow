package contract_test

// X.2 end-to-end test: real ActiveJob + delayed_job Ruby source through the
// actual parser -> matcher -> contract engine path (bug-class rule #6 — hand
// -built nodes alone are insufficient evidence for a capture/key-building
// change).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

const jobsFixture = "testdata/jobs_ruby/jobs.rb"

func parseJobsFixture(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	rp := &parser.RubyParser{}
	nodes, edges, _, err := rp.Parse(jobsFixture, "app", m, nil)
	require.NoError(t, err)
	return nodes, edges
}

func TestJobsE2E_RealParserPath(t *testing.T) {
	nodes, _ := parseJobsFixture(t)

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	var jobRules []contract.Rule
	for _, r := range rules {
		if r.Kind == contract.KindJob {
			jobRules = append(jobRules, r)
		}
	}
	require.NotEmpty(t, jobRules)

	e := &contract.Engine{}
	res := e.Link(nodes, jobRules, nil)

	byType := map[graph.EdgeType][]graph.Edge{}
	for _, edge := range res.Edges {
		byType[edge.Type] = append(byType[edge.Type], edge)
	}

	// ReportJob.perform_later -> ReportJob#perform (ActiveJob, X.2 step 1).
	require.NotEmpty(t, byType[graph.EdgeTypeJobEnqueue], "expected at least one job_enqueue edge")
	foundReportJob := false
	foundDelayDeliver := false
	for _, edge := range byType[graph.EdgeTypeJobEnqueue] {
		if strings.Contains(edge.To, "perform") {
			foundReportJob = true
		}
		if strings.Contains(edge.To, "deliver_email") {
			foundDelayDeliver = true
		}
	}
	assert.True(t, foundReportJob, "ReportJob.perform_later must link to ReportJob#perform")
	assert.True(t, foundDelayDeliver, "user.delay.deliver_email must link to User#deliver_email")

	// handle_asynchronously :rebuild -> Group#rebuild (job_perform).
	require.NotEmpty(t, byType[graph.EdgeTypeJobPerform], "expected a job_perform edge for handle_asynchronously")
	foundRebuild := false
	for _, edge := range byType[graph.EdgeTypeJobPerform] {
		if strings.Contains(edge.To, "rebuild") {
			foundRebuild = true
		}
	}
	assert.True(t, foundRebuild, "handle_asynchronously :rebuild must link to Group#rebuild")

	// Negative: the RSpec-wrapped perform_later must not mint a second,
	// test-DSL-sourced job_enqueue edge — X.0 demotes it to an ordinary call.
	for _, n := range nodes {
		if n.Meta["pattern"] == "aj_perform_later" && n.Line > 30 {
			assert.NotEqual(t, graph.NodeTypePublisher, n.Type,
				"RSpec-wrapped perform_later must be demoted, not a real publisher node")
		}
	}

	// Negative: unresolved receivers (chained call, unknown class) must
	// ledger, never fabricate an edge.
	require.NotEmpty(t, res.Unresolved, "unresolved delayed_job sites must reach the ledger")
}
