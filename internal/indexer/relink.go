package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// RelinkOptions configures a scoped relink (FR.5c): merge one service's
// freshly-built per-service DB into the workspace's merged graph.db, then
// rerun only the cross-service-capable linking passes (FR.5b's
// scopeCrossService set) restricted to edges touching that service —
// instead of a full `polyflow index`, which re-links every service.
type RelinkOptions struct {
	Config *workspace.WorkspaceConfig
	// Service is the one service whose fresh per-service DB (built by a
	// prior `polyflow index <service>`, FR.2) to merge and relink against.
	Service string
	// DBDir is the workspace root DB dir; default meta.DBDir, same default
	// as Options.DBDir. The per-service DB is expected at
	// <DBDir>/services/<Service>/graph.db — FR.2's default layout, the same
	// path `polyflow index <service>` (cmd/polyflow/main.go's runIndex)
	// writes to.
	DBDir string
	// ContractsDir, like Options.ContractsDir, adds workspace-custom
	// contract rules on top of the embedded defaults (G.5). Leaving it
	// empty means a relink only sees the embedded rules, diverging from
	// what a full `polyflow index` would have matched against.
	ContractsDir string
}

// Relink merges Service's per-service DB into the workspace's merged
// graph.db and reruns only the passes FR.5b classified scopeCrossService,
// each restricted (via linkPipelineState.filterByTargetServices) to edges
// touching Service. scopeSameServiceOnly passes are skipped entirely — a
// prior `polyflow index <service>` already computed them for Service alone,
// and MergeServiceDBs (FR.3) leaves every other service's rows untouched,
// so their results are still correct without rerunning them here.
func Relink(ctx context.Context, opts RelinkOptions) (*Stats, error) {
	start := time.Now()
	cfg := opts.Config
	if cfg == nil {
		return nil, fmt.Errorf("indexer: nil workspace config")
	}
	if opts.Service == "" {
		return nil, fmt.Errorf("relink: service name required")
	}
	if !cfg.HasService(opts.Service) {
		return nil, fmt.Errorf("relink: no service %q in workspace", opts.Service)
	}
	dbDir := opts.DBDir
	if dbDir == "" {
		dbDir = meta.DBDir
	}

	svcDBPath := filepath.Join(dbDir, "services", opts.Service, meta.DBFile)
	if _, err := os.Stat(svcDBPath); err != nil {
		return nil, fmt.Errorf("relink: service %q has no indexed DB at %s — run `polyflow index %s` first", opts.Service, svcDBPath, opts.Service)
	}

	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dbDir, err)
	}
	finalDBPath := filepath.Join(dbDir, meta.DBFile)
	dst, err := graph.NewSQLiteStore(finalDBPath)
	if err != nil {
		return nil, fmt.Errorf("relink: open %s: %w", finalDBPath, err)
	}
	defer dst.Close()

	if _, err := MergeServiceDBs(ctx, dst, map[string]string{opts.Service: svcDBPath}); err != nil {
		return nil, fmt.Errorf("relink: merge: %w", err)
	}
	// dst is opened via NewSQLiteStore (a pre-existing DB, not a known-empty
	// build store), so without this its ftsJournal is nil and every node
	// write below (persistComposedRoutes, contract_engine's minted nodes,
	// ...) pays nodes_fts's O(n) id-scan delete — id is UNINDEXED on an FTS5
	// table, so a delete-by-id can't use a real index. rebuildNodesFTS just
	// ran inside MergeServiceDBs, so nodes_fts is known-correct right now:
	// warm the journal from it so later same-content writes become no-ops
	// instead of a full scan each (measured: this dominated a relink's wall
	// time on a 9-service/50k-node fleet).
	if err := dst.WarmFTSJournal(ctx); err != nil {
		return nil, fmt.Errorf("relink: warm fts journal: %w", err)
	}

	idx, err := dst.BuildIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("relink: build index: %w", err)
	}
	// Sorted, not a direct map iteration (bug-class rule #2) — determinism
	// matters here since node/edge insertion order can affect which FTS/DB
	// rows a test observes mid-batch.
	nodeIDs := make([]string, 0, len(idx.Nodes))
	for id := range idx.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)
	allNodes := make([]graph.Node, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		allNodes = append(allNodes, *idx.Nodes[id])
	}

	stats := &Stats{}
	st := &linkPipelineState{
		ctx:            ctx,
		store:          dst,
		bw:             graph.NewBatchWriter(dst),
		cfg:            cfg,
		opts:           Options{ContractsDir: opts.ContractsDir},
		stats:          stats,
		allNodes:       allNodes,
		allEdges:       idx.AllEdges(),
		targetServices: []string{opts.Service},
	}

	for _, pass := range buildLinkPasses(st) {
		if pass.scope != scopeCrossService {
			continue
		}
		if err := pass.exec(); err != nil {
			return nil, fmt.Errorf("relink pass %s: %w", pass.name, err)
		}
	}

	nodeCount, edgeCount, err := dst.Stats(ctx)
	if err != nil {
		return nil, fmt.Errorf("relink: stats: %w", err)
	}
	stats.Nodes = nodeCount
	stats.Edges = edgeCount
	stats.Elapsed = time.Since(start)
	return stats, nil
}
