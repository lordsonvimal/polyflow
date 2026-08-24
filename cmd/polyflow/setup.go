package main

// `polyflow setup` — an interactive wizard that registers polyflow's MCP
// server and (where the agent supports it) the context-injection hook, so a
// new user doesn't have to hand-edit JSON/TOML config files or memorize
// `claude mcp add` syntax.
//
// Two independent choices, asked in order because the second (which agent)
// only makes sense once the first (how widely visible) is fixed:
//   1. scope  — repo (shared with the team via version control), user (this
//      person's own config, every project on this machine), or global
//      (every user on this machine — not natively supported by every agent;
//      falls back to user scope with a clear note when it isn't).
//   2. agent  — which coding agent to configure. Each agent is a self-
//      contained profile (internal/setupagents) so adding a new one doesn't
//      touch this wizard's flow.

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/selfupdate"
	"github.com/lordsonvimal/polyflow/internal/setupagents"
)

var (
	setupScope       string
	setupAgent       string
	setupUpdate      bool
	setupCheck       bool
	setupIncremental bool
	setupRepoPath    string
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactively register polyflow's MCP server and context hook with a coding agent",
	Long: `Registers polyflow with a coding agent in two steps: how widely visible the
config should be (repo/user/global), and which agent to configure. Answers
can be supplied via --scope/--agent to skip the prompts (e.g. in CI or a
setup script).

--update and --check are a separate mode: they pull/rebuild polyflow itself
(and, for --update, refresh every repo and fleet this machine knows about)
instead of running the wizard.`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVar(&setupScope, "scope", "", "config scope: repo, user, or global (skips the prompt)")
	setupCmd.Flags().StringVar(&setupAgent, "agent", "", "agent to configure: "+strings.Join(setupagents.Names(), ", ")+" (skips the prompt)")
	setupCmd.Flags().BoolVar(&setupUpdate, "update", false, "pull polyflow's own source, rebuild it, then reindex every registry repo and re-sync every fleet")
	setupCmd.Flags().BoolVar(&setupCheck, "check", false, "check whether polyflow's source is behind its remote, without pulling or rebuilding")
	setupCmd.Flags().BoolVar(&setupIncremental, "incremental", false, "with --update, reindex incrementally instead of the default full re-parse")
	setupCmd.Flags().StringVar(&setupRepoPath, "repo-path", "", "path to the polyflow source checkout (default: $POLYFLOW_REPO, else the machine registry, else walk up from cwd for its go.mod)")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	if setupCheck {
		return runSetupCheck(cmd)
	}
	if setupUpdate {
		return runSetupUpdate(cmd)
	}

	polyflowBin, err := os.Executable()
	if err != nil {
		polyflowBin = "polyflow"
	}

	reader := bufio.NewReader(os.Stdin)

	scope := setupScope
	if scope == "" {
		scope, err = promptScope(reader)
		if err != nil {
			return err
		}
	} else if !isValidScope(scope) {
		return fmt.Errorf("invalid --scope %q: must be repo, user, or global", scope)
	}

	agentName := setupAgent
	if agentName == "" {
		agentName, err = promptAgent(reader)
		if err != nil {
			return err
		}
	}
	agent, ok := setupagents.Get(agentName)
	if !ok {
		return fmt.Errorf("unknown --agent %q: supported agents are %s", agentName, strings.Join(setupagents.Names(), ", "))
	}

	if scope == "global" && !agent.SupportsGlobalScope() {
		fmt.Printf("%s has no system-wide (all-OS-users) config scope — falling back to 'user' scope "+
			"(applies to every project for you, not other accounts on this machine).\n", agent.DisplayName())
		scope = "user"
	}

	fmt.Printf("\nConfiguring %s (%s scope)...\n", agent.DisplayName(), scope)

	mcpResult, err := agent.SetupMCP(scope, polyflowBin)
	if err != nil {
		return fmt.Errorf("mcp setup: %w", err)
	}
	fmt.Println("  " + mcpResult)

	if agent.SupportsHooks() {
		hookResult, err := agent.SetupHooks(scope, polyflowBin)
		if err != nil {
			return fmt.Errorf("hook setup: %w", err)
		}
		fmt.Println("  " + hookResult)
	} else {
		fmt.Printf("  %s has no post-tool-use hook mechanism — skipping the context-injection hook (MCP tools are still fully available).\n", agent.DisplayName())
	}

	fmt.Println("\nDone. Restart your agent session for this to take effect.")
	return nil
}

func isValidScope(s string) bool {
	return s == "repo" || s == "user" || s == "global"
}

func promptScope(reader *bufio.Reader) (string, error) {
	fmt.Println("Where should this config apply?")
	fmt.Println("  1) repo   — shared with your team via version control (checked into this repo)")
	fmt.Println("  2) user   — just you, applies to every project you open on this machine")
	fmt.Println("  3) global — every user on this machine (falls back to 'user' if the agent doesn't support it)")
	for {
		fmt.Print("Choice [1-3]: ")
		choice, err := readChoice(reader, 3)
		if err != nil {
			return "", err
		}
		switch choice {
		case 1:
			return "repo", nil
		case 2:
			return "user", nil
		case 3:
			return "global", nil
		}
	}
}

func promptAgent(reader *bufio.Reader) (string, error) {
	agents := setupagents.All()
	fmt.Println("\nWhich agent should be configured?")
	for i, a := range agents {
		fmt.Printf("  %d) %s — %s\n", i+1, a.DisplayName(), a.Description())
	}
	for {
		fmt.Printf("Choice [1-%d]: ", len(agents))
		choice, err := readChoice(reader, len(agents))
		if err != nil {
			return "", err
		}
		if choice >= 1 && choice <= len(agents) {
			return agents[choice-1].Name(), nil
		}
	}
}

// readChoice reads one line, retrying on anything that isn't an integer in
// [1, max] — a wizard should never hard-fail on a typo.
func readChoice(reader *bufio.Reader, max int) (int, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, fmt.Errorf("read input: %w", err)
		}
		line = strings.TrimSpace(line)
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > max {
			fmt.Printf("Please enter a number from 1 to %d: ", max)
			continue
		}
		return n, nil
	}
}

// runSetupCheck implements `polyflow setup --check`: is polyflow's own
// source behind its remote? Fetch-and-compare only — no pull, no rebuild —
// so it's safe to run at any time, dirty tree included.
func runSetupCheck(cmd *cobra.Command) error {
	repoDir, err := selfupdate.FindRepo(setupRepoPath)
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
	fmt.Println("run `polyflow setup --update` to pull, rebuild, and reindex")
	return nil
}

// runSetupUpdate implements `polyflow setup --update`: pull polyflow's own
// source, rebuild it, then refresh every workspace this machine knows about
// so the new parser/linker code is actually reflected in every graph.db —
// not just the one someone happens to reindex next.
func runSetupUpdate(cmd *cobra.Command) error {
	repoDir, err := selfupdate.FindRepo(setupRepoPath)
	if err != nil {
		return err
	}

	dirty, err := selfupdate.IsDirty(repoDir)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("%s has uncommitted changes — commit or stash before --update", repoDir)
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

	full := !setupIncremental
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
