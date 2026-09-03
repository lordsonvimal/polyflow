package main

// `polyflow update` — pulls polyflow's own source, rebuilds it, and refreshes
// every repo and fleet this machine knows about. `--check` reports whether
// the source is behind its remote without pulling or rebuilding.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/selfupdate"
)

var (
	updateCheck       bool
	updateIncremental bool
	updateRepoPath    string
	updateForce       bool
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
	updateCmd.Flags().BoolVar(&updateForce, "force", false, "when the local checkout has diverged from its remote (e.g. after a force-push), hard-reset it to the upstream, discarding local-only commits")
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

	if status.Diverged() {
		local, logErr := selfupdate.LocalOnlyCommits(ctx, repoDir)
		if logErr != nil {
			return logErr
		}
		if !updateForce {
			return fmt.Errorf("%s has diverged from its remote: %d local commit(s) not on the upstream, %d upstream commit(s) not local — "+
				"a fast-forward is impossible (this is what causes git's \"Need to specify how to reconcile divergent branches\").\n"+
				"Local-only commits that `--force` would discard:\n%s\n"+
				"Re-run `polyflow update --force` to hard-reset to the upstream, or reconcile %s by hand first",
				repoDir, status.Ahead, status.Behind, indentLines(local), repoDir)
		}
		fmt.Printf("%s has diverged from its remote — discarding %d local-only commit(s):\n%s\n", repoDir, status.Ahead, indentLines(local))
		fmt.Printf("Hard-resetting %s to upstream...\n", repoDir)
		if err := selfupdate.ResetToUpstream(ctx, repoDir, os.Stdout); err != nil {
			return err
		}
	} else {
		fmt.Printf("Fast-forwarding %s...\n", repoDir)
		if err := selfupdate.Pull(ctx, repoDir, os.Stdout); err != nil {
			return err
		}
	}

	fmt.Println("\nBuilding (make install)...")
	if err := selfupdate.Build(ctx, repoDir, os.Stdout); err != nil {
		return err
	}

	// Resolve the just-built binary fresh from PATH rather than reusing
	// os.Executable(): `make install` may have removed-and-replaced the file
	// backing the running process, leaving os.Executable() pointing at a path
	// that's now stale (or, on some platforms, suffixed " (deleted)") — every
	// subsequent reindex/fleet-sync subprocess would then fail to exec.
	polyflowBin := "polyflow"
	if p, lookErr := exec.LookPath("polyflow"); lookErr == nil {
		polyflowBin = p
	} else if p, exeErr := os.Executable(); exeErr == nil {
		polyflowBin = p
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
	if status.Diverged() {
		fmt.Printf("diverged: %d commit(s) ahead, %d behind — local %s, remote %s\n", status.Ahead, status.Behind, status.LocalSHA, status.RemoteSHA)
		fmt.Println("run `polyflow update --force` to hard-reset to the remote (discards local-only commits)")
		return nil
	}
	fmt.Printf("outdated: %d commit(s) behind — local %s, remote %s\n", status.Behind, status.LocalSHA, status.RemoteSHA)
	fmt.Println("run `polyflow update` to pull, rebuild, and reindex")
	return nil
}

// indentLines prefixes each line with two spaces for readable multi-line
// error/status output; returns "(none)" for empty input.
func indentLines(s string) string {
	if strings.TrimSpace(s) == "" {
		return "  (none)"
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
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
