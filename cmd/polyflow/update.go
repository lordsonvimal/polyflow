package main

// `polyflow update` — pulls polyflow's own source, rebuilds it, and refreshes
// every repo and fleet this machine knows about. `--check` reports whether
// the source is behind its remote without pulling or rebuilding.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/selfupdate"
)

var (
	updateCheck       bool
	updateIncremental bool
	updateRepoPath    string
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Pull, rebuild, and reindex polyflow itself",
	Long: `Pulls polyflow's own source, rebuilds it (make install), then reindexes every
registry repo and re-syncs every fleet this machine knows about — so the new
parser/linker code is actually reflected in every graph.db, not just the one
someone happens to reindex next.

--check reports whether the source is behind its remote, without pulling or
rebuilding.`,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().BoolVar(&updateCheck, "check", false, "check whether polyflow's source is behind its remote, without pulling or rebuilding")
	updateCmd.Flags().BoolVar(&updateIncremental, "incremental", false, "reindex incrementally instead of the default full re-parse")
	updateCmd.Flags().StringVar(&updateRepoPath, "repo-path", "", "path to the polyflow source checkout (default: $POLYFLOW_REPO, else the machine registry, else walk up from cwd for its go.mod)")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	if updateCheck {
		return runUpdateCheck(cmd)
	}

	repoDir, err := selfupdate.FindRepo(updateRepoPath)
	if err != nil {
		return err
	}

	dirty, err := selfupdate.IsDirty(repoDir)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("%s has uncommitted changes — commit or stash before `polyflow update`", repoDir)
	}

	ctx := cmd.Context()

	status, err := selfupdate.CheckOutdated(ctx, repoDir)
	if err != nil {
		return err
	}
	if !status.Outdated() {
		fmt.Printf("Already up to date (%s)\n", status.LocalSHA)
		return nil
	}

	fmt.Printf("Pulling %s...\n", repoDir)
	if err := selfupdate.Pull(ctx, repoDir, os.Stdout); err != nil {
		return err
	}

	fmt.Println("\nBuilding (make install)...")
	if err := selfupdate.Build(ctx, repoDir, os.Stdout); err != nil {
		return err
	}

	polyflowBin, err := os.Executable()
	if err != nil {
		polyflowBin = "polyflow"
	}
	regPath, err := registry.DefaultPath()
	if err != nil {
		return err
	}

	full := !updateIncremental
	fmt.Println("\nReindexing registered repos...")
	reindexResults, err := selfupdate.ReindexAll(ctx, polyflowBin, regPath, full, os.Stdout)
	if err != nil {
		return err
	}

	fmt.Println("\nSyncing fleets...")
	fleetResults, err := selfupdate.SyncAllFleets(ctx, polyflowBin, regPath, os.Stdout)
	if err != nil {
		return err
	}

	fmt.Println("\n--- Summary ---")
	printResultSummary("Repos reindexed", reindexResults)
	printResultSummary("Fleets synced", fleetResults)
	return nil
}

// runUpdateCheck implements `polyflow update --check`: is polyflow's own
// source behind its remote? Fetch-and-compare only — no pull, no rebuild —
// so it's safe to run at any time, dirty tree included.
func runUpdateCheck(cmd *cobra.Command) error {
	repoDir, err := selfupdate.FindRepo(updateRepoPath)
	if err != nil {
		return err
	}
	status, err := selfupdate.CheckOutdated(cmd.Context(), repoDir)
	if err != nil {
		return err
	}
	if !status.Outdated() {
		fmt.Printf("up to date (%s)\n", status.LocalSHA)
		return nil
	}
	fmt.Printf("outdated: %d commit(s) behind — local %s, remote %s\n", status.Behind, status.LocalSHA, status.RemoteSHA)
	fmt.Println("run `polyflow update` to pull, rebuild, and reindex")
	return nil
}

func printResultSummary(label string, results []selfupdate.RepoResult) {
	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	fmt.Printf("%s: %d ok, %d failed (of %d)\n", label, len(results)-failed, failed, len(results))
	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("  FAILED %s: %v\n", r.Name, r.Err)
		}
	}
}
