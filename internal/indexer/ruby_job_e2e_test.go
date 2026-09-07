package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_ActiveJobInheritedPerform is Tier CJ end to end: a job whose base
// class is a *project* class, not ApplicationJob, is invisible to the pattern
// file's direct-superclass predicate. Before CJ this workspace produced one
// subscriber node (none, in fact — no class here names ApplicationJob
// directly) and an unresolved `kind=job` naming a class that plainly exists.
//
// The enqueue edge must land on report_job.rb specifically, and there must be
// exactly one of it: a second subscriber node for the same class would make
// the contract engine mint fan-out, which this tier is not allowed to do.
func TestRun_ActiveJobInheritedPerform(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "app", "jobs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "app", "controllers"), 0o755))
	// The activejob pattern file is package-gated, so the fixture has to
	// resolve the gem the way a real Rails app does.
	writeFile(t, svc, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\n")
	writeFile(t, svc, "Gemfile.lock", `GEM
  remote: https://rubygems.org/
  specs:
    activejob (7.1.3)
    rails (7.1.3)

PLATFORMS
  ruby

DEPENDENCIES
  rails
`)

	jobs := filepath.Join(svc, "app", "jobs")
	writeFile(t, jobs, "application_job.rb", `class ApplicationJob < ActiveJob::Base
end
`)
	writeFile(t, jobs, "orion_base_job.rb", `class OrionBaseJob < ApplicationJob
  def perform(*)
    raise NotImplementedError
  end
end
`)
	writeFile(t, jobs, "report_job.rb", `class ReportJob < OrionBaseJob
  def perform(id)
    Report.generate(id)
  end
end
`)
	// A PORO with a `perform` method and a superclass of its own: nominated by
	// the candidate pattern, and required to leave no trace in the graph.
	writeFile(t, jobs, "presenter.rb", `class Presenter < BasePresenter
  def perform(data)
    render(data)
  end
end
`)
	writeFile(t, filepath.Join(svc, "app", "controllers"), "reports_controller.rb",
		`class ReportsController < ApplicationController
  def create
    ReportJob.perform_later(params[:id])
  end
end
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "orion", Path: svc, Language: "ruby"}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	byID := idx.Nodes
	subscribers := map[string]string{} // job_class -> node ID
	for _, n := range byID {
		if n.Type == graph.NodeTypeSubscriber && n.Meta["pattern"] == "aj_perform_method" {
			assert.NotContains(t, subscribers, n.Meta["job_class"],
				"two subscriber nodes for %s — the enqueue matcher would fan out", n.Meta["job_class"])
			subscribers[n.Meta["job_class"]] = n.ID
		}
		assert.NotEqual(t, "aj_perform_method_candidate", n.Meta["pattern"],
			"candidate node %s survived linking", n.ID)
	}

	require.Contains(t, subscribers, "ReportJob", "the 2-hop job must have a subscriber node")
	require.Contains(t, subscribers, "OrionBaseJob",
		"a base class that defines a real perform is a legitimate target too")
	assert.NotContains(t, subscribers, "Presenter", "a PORO with perform is not a job")

	var enqueue []graph.Edge
	performTargets := map[string]string{}
	for _, e := range idx.AllEdges() {
		switch e.Type {
		case graph.EdgeTypeJobEnqueue:
			enqueue = append(enqueue, e)
		case graph.EdgeTypeJobPerform:
			performTargets[e.From] = e.To
		}
		// The FK the promotion's node-ID rewrite could have broken.
		require.Contains(t, byID, e.From, "edge %s has a dangling From", e.ID)
		require.Contains(t, byID, e.To, "edge %s has a dangling To", e.ID)
	}

	require.Len(t, enqueue, 1, "one perform_later call site, one job_enqueue edge: %+v", enqueue)
	target := byID[enqueue[0].To]
	require.NotNil(t, target)
	assert.True(t, strings.HasSuffix(target.File, "app/jobs/report_job.rb"),
		"perform_later must resolve to the class that defines perform, not to its base")

	for class, subID := range subscribers {
		to, ok := performTargets[subID]
		require.True(t, ok, "%s subscriber has no job_perform edge", class)
		assert.Equal(t, "perform", byID[to].Label)
		assert.Equal(t, byID[subID].File, byID[to].File, "%s: job_perform crossed files", class)

		// Every job subscriber hangs off its class, promoted or not. The
		// candidate node used to be typed `function`, which made it the
		// innermost enclosing scope for the class-line subscriber of its own
		// class and stole this edge from the 20 directly-matched jobs on the
		// audit corpus.
		var owner *graph.Node
		for _, in := range idx.InEdges[subID] {
			if in.Type == graph.EdgeTypeContains {
				owner = byID[in.From]
			}
		}
		require.NotNil(t, owner, "%s subscriber has no contains edge from its class", class)
		assert.Equal(t, graph.NodeTypeClass, owner.Type)
		assert.Equal(t, class, owner.Label)
	}

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	for _, u := range unresolved {
		assert.NotEqual(t, "job", u.Kind, "unresolved job ref %+v: every job class here is in the graph", u)
	}
}
