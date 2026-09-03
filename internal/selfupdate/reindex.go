package selfupdate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/lordsonvimal/polyflow/internal/registry"
)

// RepoResult is one registry entry's or fleet's outcome, kept separate from
// a hard error so one bad repo doesn't abort the rest of the fan-out.
type RepoResult struct {
	Name string
	Err  error
}

// ReindexAll runs `<polyflowBin> index [--full]` in every registry entry's
// LocalPath — the same set of repos GR.1 self-registers into registry.yml on
// a standalone `polyflow index`. An entry whose LocalPath no longer exists
// (moved or deleted checkout) is skipped, not treated as failure — the
// registry doesn't get to veto a machine's actual filesystem state.
func ReindexAll(ctx context.Context, polyflowBin, regPath string, full bool, out io.Writer, extraArgs ...string) ([]RepoResult, error) {
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	var results []RepoResult
	for _, e := range reg.Entries {
		if e.LocalPath == "" {
			continue
		}
		if _, err := os.Stat(e.LocalPath); err != nil {
			fmt.Fprintf(out, "skip %s: %v\n", e.Service, err)
			continue
		}
		fmt.Fprintf(out, "\n=== reindex %s (%s) ===\n", e.Service, e.LocalPath)
		args := []string{"index"}
		if full {
			args = append(args, "--full")
		}
		args = append(args, extraArgs...)
		err := runStreamed(ctx, e.LocalPath, out, polyflowBin, args...)
		results = append(results, RepoResult{Name: e.Service, Err: err})
		if err != nil {
			fmt.Fprintf(out, "reindex %s: %v\n", e.Service, err)
		}
	}
	return results, nil
}

// SyncAllFleets runs `<polyflowBin> fleet sync --fleet <path>` for every
// fleet this machine has ever resolved (registry.FleetConfigPaths, populated
// by GR.3 the first time `polyflow fleet sync` reads that definition) — so a
// polyflow code change also rebuilds every fleet's bridge.db of cross-service
// edges, not just each member's own graph.db.
func SyncAllFleets(ctx context.Context, polyflowBin, regPath string, out io.Writer) ([]RepoResult, error) {
	reg, err := registry.Load(regPath)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	var results []RepoResult
	for fleetName, cfgPath := range reg.FleetConfigPaths {
		if _, err := os.Stat(cfgPath); err != nil {
			fmt.Fprintf(out, "skip fleet %s: %v\n", fleetName, err)
			continue
		}
		fmt.Fprintf(out, "\n=== fleet sync %s (%s) ===\n", fleetName, cfgPath)
		cmd := exec.CommandContext(ctx, polyflowBin, "fleet", "sync", "--fleet", cfgPath)
		cmd.Stdout = out
		cmd.Stderr = out
		err := cmd.Run()
		results = append(results, RepoResult{Name: fleetName, Err: err})
		if err != nil {
			fmt.Fprintf(out, "fleet sync %s: %v\n", fleetName, err)
		}
	}
	return results, nil
}
