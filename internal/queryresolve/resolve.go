// Package queryresolve implements Phase GR.3's query-time federation
// resolver (docs/global-fleet-registry-plan.md): given a starting directory,
// find the nearest local .polyflow/graph.db (the existing upward walk, moved
// here so hook_context_inject.go's findPolyflowDB and every CLI/MCP query
// path share one implementation), then check whether that local workspace is
// a known fleet member (internal/registry's reverse index, GR.3) and, if
// exactly one fleet claims it, resolve — building or refreshing on demand —
// that fleet's bridge.db (GR.2).
package queryresolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// Result is what Resolve found for one starting directory.
type Result struct {
	// LocalDBPath is the nearest .polyflow/graph.db found by the upward
	// walk, or "" if none exists.
	LocalDBPath string
	// WorkspaceRoot is LocalDBPath's workspace root (the directory
	// containing .polyflow), or "" alongside an empty LocalDBPath.
	WorkspaceRoot string
	// FleetName is the fleet claiming WorkspaceRoot, if exactly one does.
	// Empty when the workspace is not a registered fleet member.
	FleetName string
	// BridgePath is FleetName's bridge.db, built or refreshed on demand
	// unless Options.NoSync. Empty whenever there is no cross-service data
	// available (no fleet, sync disabled, sync failed, or the fleet's
	// definition file location is unknown) — callers must treat an empty
	// BridgePath as "nothing to stitch in", never as an error.
	BridgePath string
}

// ErrAmbiguousFleet is returned when more than one fleet claims the same
// local workspace and Options.Fleet did not pick one — "list candidates,
// require --fleet", never a silent pick.
type ErrAmbiguousFleet struct {
	Candidates []string
}

func (e *ErrAmbiguousFleet) Error() string {
	return fmt.Sprintf("this workspace is claimed by multiple fleets (%s) — pass --fleet to pick one",
		strings.Join(e.Candidates, ", "))
}

// Options configures Resolve.
type Options struct {
	// RegistryPath overrides registry.DefaultPath().
	RegistryPath string
	// Fleet, if non-empty, picks which fleet to resolve when more than one
	// claims the workspace. Ignored (harmlessly) when only one candidate
	// exists.
	Fleet string
	// NoSync disables building or refreshing a stale/missing bridge —
	// Resolve only reports a BridgePath that already exists on disk. Used
	// by latency-sensitive callers (hook injection) that must never block
	// on a clone or a cross-service relink pass.
	NoSync bool
	// SyncOpts is passed through to fleetsync.Sync when a rebuild is
	// triggered; RegistryPath, BridgePath, ContractsDir, and
	// FleetConfigPath are always overridden by Resolve itself.
	SyncOpts fleetsync.SyncOptions
}

// FindLocalDB walks upward from startDir (6-level cap, same as the original
// hook-injection walk) looking for .polyflow/graph.db. Returns "" if none is
// found — callers must no-op on empty, never treat it as an error.
func FindLocalDB(startDir string) string {
	d, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	for i := 0; i < 6; i++ {
		cand := filepath.Join(d, meta.DBDir, meta.DBFile)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return ""
}

// Resolve implements the four consumers' shared lookup: local DB, fleet
// membership, bridge path.
func Resolve(ctx context.Context, startDir string, opts Options) (*Result, error) {
	res := &Result{LocalDBPath: FindLocalDB(startDir)}
	if res.LocalDBPath == "" {
		return res, nil
	}
	res.WorkspaceRoot = filepath.Dir(filepath.Dir(res.LocalDBPath))

	regPath := opts.RegistryPath
	if regPath == "" {
		var err error
		regPath, err = registry.DefaultPath()
		if err != nil {
			return res, err
		}
	}
	reg, err := registry.Load(regPath)
	if err != nil {
		return res, err
	}

	candidates := reg.FleetsForPath(res.WorkspaceRoot)
	switch {
	case len(candidates) == 0:
		return res, nil
	case len(candidates) == 1:
		res.FleetName = candidates[0]
	case opts.Fleet != "":
		res.FleetName = opts.Fleet
	default:
		return res, &ErrAmbiguousFleet{Candidates: candidates}
	}

	bridgePath, err := fleetsync.DefaultBridgePath(res.FleetName)
	if err != nil {
		return res, err
	}

	if !opts.NoSync && needsSync(bridgePath, res.LocalDBPath) {
		if fleetConfigPath := reg.FleetConfigPaths[res.FleetName]; fleetConfigPath != "" {
			if cfg, cfgErr := fleetconfig.Load(fleetConfigPath); cfgErr == nil {
				so := opts.SyncOpts
				so.RegistryPath = regPath
				so.BridgePath = bridgePath
				so.FleetConfigPath = fleetConfigPath
				if so.ContractsDir == "" {
					so.ContractsDir = filepath.Dir(fleetConfigPath)
				}
				// Best-effort: a sync failure (network down, dirty sibling
				// checkouts) must not break the query that triggered it —
				// fall through and report whatever bridge already exists,
				// possibly none.
				_, _ = fleetsync.Sync(ctx, cfg, so)
			}
		}
	}

	if _, statErr := os.Stat(bridgePath); statErr == nil {
		res.BridgePath = bridgePath
	}
	return res, nil
}

// needsSync reports whether bridgePath is missing or older than
// localDBPath's own last modification — "stale" per the plan doc's cheap
// timestamp check, not a full re-resolve on every query.
func needsSync(bridgePath, localDBPath string) bool {
	bInfo, err := os.Stat(bridgePath)
	if err != nil {
		return true
	}
	lInfo, err := os.Stat(localDBPath)
	if err != nil {
		return false
	}
	return bInfo.ModTime().Before(lInfo.ModTime())
}
