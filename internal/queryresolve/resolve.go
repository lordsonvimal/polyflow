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

// selectFleet loads the registry and applies the shared candidate-selection
// rule (one candidate wins outright, more than one requires opts.Fleet, zero
// means "not a fleet member") used by both Resolve and FleetMembers.
// fleetName is "" (no error) when workspaceRoot belongs to no fleet.
func selectFleet(workspaceRoot string, opts Options) (fleetName string, reg *registry.Registry, regPath string, err error) {
	regPath = opts.RegistryPath
	if regPath == "" {
		regPath, err = registry.DefaultPath()
		if err != nil {
			return "", nil, "", err
		}
	}
	reg, err = registry.Load(regPath)
	if err != nil {
		return "", nil, "", err
	}

	candidates := reg.FleetsForPath(workspaceRoot)
	switch {
	case len(candidates) == 0:
		return "", reg, regPath, nil
	case len(candidates) == 1:
		return candidates[0], reg, regPath, nil
	case opts.Fleet != "":
		return opts.Fleet, reg, regPath, nil
	default:
		return "", reg, regPath, &ErrAmbiguousFleet{Candidates: candidates}
	}
}

// Resolve implements the four consumers' shared lookup: local DB, fleet
// membership, bridge path.
func Resolve(ctx context.Context, startDir string, opts Options) (*Result, error) {
	res := &Result{LocalDBPath: FindLocalDB(startDir)}
	if res.LocalDBPath == "" {
		return res, nil
	}
	res.WorkspaceRoot = filepath.Dir(filepath.Dir(res.LocalDBPath))

	fleetName, reg, regPath, err := selectFleet(res.WorkspaceRoot, opts)
	if err != nil {
		return res, err
	}
	if fleetName == "" {
		return res, nil
	}
	res.FleetName = fleetName

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

// FleetMembers resolves every locally-known member of the fleet claiming
// startDir's workspace (same selection rule as Resolve, including
// ErrAmbiguousFleet) and returns service name -> that member's own local
// graph.db path, for every member this machine already has indexed. A
// member registered but never indexed locally (no LocalPath, or a LocalPath
// whose graph.db is missing — e.g. a sibling never cloned on this machine)
// is silently omitted: GR.3's "search's federation scope" is every
// currently-resolved member, not a network fetch triggered by a search
// query. Returns a nil map (no error) when startDir is not a fleet member at
// all.
func FleetMembers(startDir string, opts Options) (map[string]string, error) {
	localDB := FindLocalDB(startDir)
	if localDB == "" {
		return nil, nil
	}
	workspaceRoot := filepath.Dir(filepath.Dir(localDB))

	fleetName, reg, _, err := selectFleet(workspaceRoot, opts)
	if err != nil {
		return nil, err
	}
	if fleetName == "" {
		return nil, nil
	}

	members := make(map[string]string)
	for _, e := range reg.Entries {
		if e.LocalPath == "" || !containsString(e.Fleets, fleetName) {
			continue
		}
		if dbPath := localDBPathFor(e.LocalPath, e.Service); dbPath != "" {
			members[e.Service] = dbPath
		}
	}
	return members, nil
}

// localDBPathFor mirrors internal/fleetsync's unexported dbPathFor (a
// Subpath-less service's graph.db lives at <localPath>/.polyflow/graph.db;
// a Subpath (monorepo) service's lives at
// <localPath>/.polyflow/services/<name>/graph.db) without needing to load
// the fleet definition just to learn Subpath — the registry only ever
// records LocalPath, so this tries both and trusts whichever exists on
// disk.
func localDBPathFor(localPath, service string) string {
	plain := filepath.Join(localPath, meta.DBDir, meta.DBFile)
	if _, err := os.Stat(plain); err == nil {
		return plain
	}
	scoped := filepath.Join(localPath, meta.DBDir, "services", service, meta.DBFile)
	if _, err := os.Stat(scoped); err == nil {
		return scoped
	}
	return ""
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
