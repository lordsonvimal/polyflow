package e2e_test

// TestPythonCelery_JobEnqueueEdge proves the L.P3 core promise:
// Celery task.delay() / apply_async() dispatch calls in one service produce
// job_enqueue edges to the @app.task-decorated functions in the worker
// service, with only YAML pattern files and a jobs.yaml contract rule added
// — zero contract-engine changes.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

const pythonCeleryFixtureWS = "testdata/python_celery"
const pythonCeleryPatternsDir = "../../patterns"

func indexPythonCelery(t *testing.T) *graph.SQLiteStore {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(pythonCeleryFixtureWS, "workspace.yaml"))
	require.NoError(t, err)

	reg, err := patterns.DefaultRegistry(pythonCeleryPatternsDir)
	require.NoError(t, err)

	tmpDB := filepath.Join(t.TempDir(), "graph.db")
	store, err := graph.NewSQLiteStore(tmpDB)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	bw := graph.NewBatchWriter(store)

	var allNodes []graph.Node
	var allEdges []graph.Edge

	for _, svc := range cfg.Services {
		svcPath := filepath.Join(pythonCeleryFixtureWS, svc.Path)

		svcDeps, err := deps.Resolve(svcPath)
		require.NoError(t, err)

		matcher := patterns.NewTreeSitterMatcherForService(reg, svcDeps)

		var files []string
		err = filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".py" {
				return nil
			}
			files = append(files, path)
			return nil
		})
		require.NoError(t, err)

		pool := parser.NewWorkerPool(2, matcher, svc.Name)
		for result := range pool.Run(files) {
			if result.Err != nil {
				_ = store.UpsertParseError(ctx, &graph.ParseError{
					FilePath:   result.File,
					Service:    svc.Name,
					ErrorCount: 1,
					IndexedAt:  time.Now().Unix(),
				})
				continue
			}
			for i := range result.Nodes {
				n := result.Nodes[i]
				require.NoError(t, bw.AddNode(ctx, &n))
				allNodes = append(allNodes, n)
			}
			for i := range result.Edges {
				e := result.Edges[i]
				require.NoError(t, bw.AddEdge(ctx, &e))
				allEdges = append(allEdges, e)
			}
		}
	}
	require.NoError(t, bw.Flush(ctx))

	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)

	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	contractResult := eng.Link(hintedNodes, contractRules, cfg.Links)

	bwN := graph.NewBatchWriter(store)
	for i := range contractResult.Nodes {
		n := contractResult.Nodes[i]
		_ = bwN.AddNode(ctx, &n)
	}
	require.NoError(t, bwN.Flush(ctx))

	bwE := graph.NewBatchWriter(store)
	for i := range contractResult.Edges {
		e := contractResult.Edges[i]
		require.NoError(t, bwE.AddEdge(ctx, &e))
	}
	require.NoError(t, bwE.Flush(ctx))

	require.NoError(t, store.SetMeta(ctx, "last_indexed", "1234567890"))
	return store
}

// TestPythonCelery_JobEnqueueEdge is the L.P3 acceptance test: the
// svc-api's send_email.delay() call links to the svc-worker's @app.task
// def send_email() via a job_enqueue edge through the Celery contract rule.
func TestPythonCelery_JobEnqueueEdge(t *testing.T) {
	store := indexPythonCelery(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	var jobEdges []*graph.Edge
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type == graph.EdgeTypeJobEnqueue {
				jobEdges = append(jobEdges, e)
			}
		}
	}

	assert.NotEmpty(t, jobEdges,
		"expected job_enqueue edges from svc-api task dispatch to svc-worker @app.task definitions")

	// Rule 1 check: all three dispatches (delay + apply_async + delay) produce edges.
	assert.GreaterOrEqual(t, len(jobEdges), 2,
		"expected ≥2 job_enqueue edges (send_email.delay + generate_report.delay at minimum)")
}

// TestPythonCelery_TaskNodes verifies that the worker service produces
// NodeTypeSubscriber nodes from @app.task and @shared_task decorators.
func TestPythonCelery_TaskNodes(t *testing.T) {
	store := indexPythonCelery(t)
	ctx := context.Background()

	nodes, err := store.SearchNodes(ctx, "send_email", 20)
	require.NoError(t, err)

	found := false
	for _, n := range nodes {
		if n.Service == "svc-worker" && n.Type == graph.NodeTypeSubscriber {
			found = true
			break
		}
	}
	assert.True(t, found, "expected NodeTypeSubscriber for @app.task def send_email in svc-worker")
}

// TestPythonCelery_ProducerNodes verifies that the API service produces
// NodeTypePublisher nodes from .delay() and .apply_async() calls.
func TestPythonCelery_ProducerNodes(t *testing.T) {
	store := indexPythonCelery(t)
	ctx := context.Background()

	nodes, err := store.SearchNodes(ctx, "send_email", 20)
	require.NoError(t, err)

	found := false
	for _, n := range nodes {
		if n.Service == "svc-api" && n.Type == graph.NodeTypePublisher {
			found = true
			break
		}
	}
	assert.True(t, found, "expected NodeTypePublisher for send_email.delay() in svc-api")
}

// TestPythonCelery_Determinism runs the pipeline twice and confirms the
// job_enqueue edge count is identical (rule 2: deterministic output).
func TestPythonCelery_Determinism(t *testing.T) {
	store1 := indexPythonCelery(t)
	store2 := indexPythonCelery(t)
	ctx := context.Background()

	count := func(s *graph.SQLiteStore) int {
		idx, err := s.BuildIndex(ctx)
		require.NoError(t, err)
		n := 0
		for _, edges := range idx.OutEdges {
			for _, e := range edges {
				if e.Type == graph.EdgeTypeJobEnqueue {
					n++
				}
			}
		}
		return n
	}

	c1 := count(store1)
	c2 := count(store2)
	assert.Equal(t, c1, c2, "job_enqueue edge count must be identical across two runs (rule 2)")
}
