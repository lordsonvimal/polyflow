// Bridge build (docs/global-fleet-registry-plan.md, "The bridge build" and
// "Phase GR.2"): once every fleet member's own local graph.db is ready
// (fleetsync.ResolveService, GR.1), Sync opens each member's DB unmodified,
// hands their combined node/edge set to indexer.BuildBridge (which reruns
// exactly the scopeCrossService linking passes internal/indexer/relink.go's
// loop already makes), and persists only what that produces — a small set
// of cross-service edges plus a stub node per endpoint — to a small
// bridge.db. It never copies a member's full node set.
package fleetsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// fleetsDirName mirrors the local-registry home-dir convention
// (internal/registry.DefaultPath): ~/.polyflow/fleets/<fleet-name>/bridge.db,
// or $POLYFLOW_HOME/fleets/<fleet-name>/bridge.db when overridden.
const fleetsDirName = "fleets"

// bridgeFileName is the file name inside a fleet's directory under the
// polyflow home dir.
const bridgeFileName = "bridge.db"

// refOverridesFileName is the branch-local ref-override file (see "Ref
// overrides" in the plan doc) — committed on the branch being built, never
// on the shared fleet definition.
const refOverridesFileName = ".polyflow-refs.yml"

// SyncOptions carries everything Sync needs beyond the fleet definition
// itself.
type SyncOptions struct {
	// RegistryPath, CacheDir, ScratchDir are passed through to
	// ResolveService for every member unchanged; see ResolveOptions.
	RegistryPath string
	CacheDir     string
	ScratchDir   string
	// BridgePath overrides the output bridge.db path. Empty means
	// DefaultBridgePath(cfg.Name).
	BridgePath string
	// RefOverrides maps service name to a ref to resolve instead of the
	// fleet definition's default, already merged by the caller in the
	// documented precedence order (flag beats file beats default) — Sync
	// applies whatever map it's given with no further merging.
	RefOverrides map[string]string
	// ContractsDir adds workspace-custom contract rules on top of the
	// embedded defaults (G.5), same meaning as indexer.Options.ContractsDir.
	ContractsDir string
	// Workers bounds how many members are resolved concurrently. 0 means
	// errgroup's default (no limit).
	Workers int
	// FleetConfigPath is the local path to the fleet definition file this
	// Sync call loaded cfg from (e.g. the --fleet flag's value). Recorded
	// into the registry's reverse index (GR.3) alongside each member's
	// fleet membership so a later query-time resolver can find and reload
	// it without being told again. Empty disables recording (membership
	// stays whatever it already was).
	FleetConfigPath string
}

// BridgeStats summarizes one fleet sync run.
type BridgeStats struct {
	Services int
	Nodes    int // endpoint/synthetic node rows written to bridge.db
	Edges    int // cross-service edges written to bridge.db
	Elapsed  time.Duration
}

// DefaultBridgePath returns the local path for fleetName's bridge.db,
// honoring $POLYFLOW_HOME like registry.DefaultPath.
func DefaultBridgePath(fleetName string) (string, error) {
	home := os.Getenv("POLYFLOW_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		home = filepath.Join(h, ".polyflow")
	}
	return filepath.Join(home, fleetsDirName, fleetName, bridgeFileName), nil
}

// LoadRefOverrides reads a `.polyflow-refs.yml` file in dir, if present. A
// missing file is not an error — it returns an empty map, since an override
// file is optional (most branches build against every service's default
// ref).
func LoadRefOverrides(dir string) (map[string]string, error) {
	path := filepath.Join(dir, refOverridesFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Overrides map[string]string `yaml:"overrides"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Overrides == nil {
		doc.Overrides = map[string]string{}
	}
	return doc.Overrides, nil
}

// Sync implements Phase GR.2's "bridge build": resolve every fleet member to
// a ready local graph.db (GR.1), rerun the cross-service-capable linking
// passes over their combined (in-memory only) node/edge set, and persist
// only what those passes newly produce to opts.BridgePath (or
// DefaultBridgePath(cfg.Name)).
func Sync(ctx context.Context, cfg *fleetconfig.Config, opts SyncOptions) (*BridgeStats, error) {
	start := time.Now()
	if len(cfg.Services) == 0 {
		return nil, fmt.Errorf("fleet %q has no services", cfg.Name)
	}

	dbPaths := make([]string, len(cfg.Services))
	{
		g, gctx := errgroup.WithContext(ctx)
		if opts.Workers > 0 {
			g.SetLimit(opts.Workers)
		}
		for i, svc := range cfg.Services {
			i, svc := i, svc
			g.Go(func() error {
				dbPath, _, err := ResolveService(gctx, svc, opts.RefOverrides[svc.Name], ResolveOptions{
					RegistryPath: opts.RegistryPath,
					CacheDir:     opts.CacheDir,
					ScratchDir:   opts.ScratchDir,
				})
				if err != nil {
					return fmt.Errorf("resolve %s: %w", svc.Name, err)
				}
				dbPaths[i] = dbPath
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	if opts.FleetConfigPath != "" {
		regPath := opts.RegistryPath
		if regPath == "" {
			var err error
			regPath, err = registry.DefaultPath()
			if err != nil {
				return nil, err
			}
		}
		for _, svc := range cfg.Services {
			if err := registry.RecordFleetMembership(regPath, svc.Name, cfg.Name, opts.FleetConfigPath); err != nil {
				return nil, fmt.Errorf("record fleet membership for %s: %w", svc.Name, err)
			}
		}
	}

	// Open every member's own DB unmodified (graph.BuildIndex, read-only
	// use) and union their node/edge sets in memory — never merged into a
	// shared store. Node IDs are already disjoint across services (FR.0's
	// (service, file, line, name, ...) keying), so a plain union is safe.
	var allNodes []graph.Node
	var allEdges []graph.Edge
	for i, svc := range cfg.Services {
		store, err := graph.NewSQLiteStore(dbPaths[i])
		if err != nil {
			return nil, fmt.Errorf("open %s db %s: %w", svc.Name, dbPaths[i], err)
		}
		idx, err := store.BuildIndex(ctx)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("read %s db %s: %w", svc.Name, dbPaths[i], err)
		}
		store.Close()

		ids := make([]string, 0, len(idx.Nodes))
		for id := range idx.Nodes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			allNodes = append(allNodes, *idx.Nodes[id])
		}
		allEdges = append(allEdges, idx.AllEdges()...)
	}

	result, err := indexer.BuildBridge(ctx, cfg.Links, opts.ContractsDir, allNodes, allEdges)
	if err != nil {
		return nil, err
	}

	bridgePath := opts.BridgePath
	if bridgePath == "" {
		bridgePath, err = DefaultBridgePath(cfg.Name)
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(bridgePath), 0o755); err != nil {
		return nil, fmt.Errorf("create bridge dir: %w", err)
	}
	// A sync rebuilds the bridge from scratch each time rather than patching
	// an existing one — the member set/refs may have changed since the last
	// run, and a bridge is cheap enough (a few hundred rows) that an
	// incremental update isn't worth the staleness risk.
	if err := os.Remove(bridgePath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale bridge db: %w", err)
	}
	bridgeStore, err := graph.NewSQLiteStore(bridgePath)
	if err != nil {
		return nil, fmt.Errorf("create bridge db %s: %w", bridgePath, err)
	}
	defer bridgeStore.Close()

	bridgeBW := graph.NewFreshBatchWriter(bridgeStore)
	for i := range result.Nodes {
		if err := bridgeBW.AddNode(ctx, &result.Nodes[i]); err != nil {
			return nil, fmt.Errorf("write bridge nodes: %w", err)
		}
	}
	if err := bridgeBW.Flush(ctx); err != nil {
		return nil, fmt.Errorf("write bridge nodes: %w", err)
	}
	for i := range result.Edges {
		if err := bridgeBW.AddEdge(ctx, &result.Edges[i]); err != nil {
			return nil, fmt.Errorf("write bridge edges: %w", err)
		}
	}
	if err := bridgeBW.Flush(ctx); err != nil {
		return nil, fmt.Errorf("write bridge edges: %w", err)
	}

	return &BridgeStats{
		Services: len(cfg.Services),
		Nodes:    len(result.Nodes),
		Edges:    len(result.Edges),
		Elapsed:  time.Since(start),
	}, nil
}
