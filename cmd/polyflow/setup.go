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

	"github.com/lordsonvimal/polyflow/internal/setupagents"
)

var (
	setupScope  string
	setupAgent  string
	setupRemove bool
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactively register polyflow's MCP server and context hook with a coding agent",
	Long: `Registers polyflow with a coding agent in two steps: how widely visible the
config should be (repo/user/global), and which agent to configure. Answers
can be supplied via --scope/--agent to skip the prompts (e.g. in CI or a
setup script).

Pass --remove to reverse setup instead: unregisters the MCP server, unwires
the context-injection hook, and removes polyflow's tool-preference nudge
from CLAUDE.md/AGENTS.md (only the marked block polyflow owns — nothing
else in the file is touched). Running setup again afterwards re-adds all
three.

To update polyflow itself, use ` + "`polyflow update`" + ` instead.`,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVar(&setupScope, "scope", "", "config scope: repo, user, or global (skips the prompt)")
	setupCmd.Flags().StringVar(&setupAgent, "agent", "", "agent to configure: "+strings.Join(setupagents.Names(), ", ")+" (skips the prompt)")
	setupCmd.Flags().BoolVar(&setupRemove, "remove", false, "reverse setup: unregister the MCP server, hooks, and CLAUDE.md/AGENTS.md nudge instead of adding them")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
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

	if setupRemove {
		return runSetupRemove(agent, scope)
	}
	return runSetupAdd(agent, scope, polyflowBin)
}

func runSetupAdd(agent setupagents.Agent, scope, polyflowBin string) error {
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

	if agent.SupportsNudge() {
		nudgeResult, err := setupagents.SetupNudge(agent, scope)
		if err != nil {
			return fmt.Errorf("nudge setup: %w", err)
		}
		fmt.Println("  " + nudgeResult)
	} else {
		fmt.Printf("  %s has no persistent instructions file polyflow knows how to steer yet — skipping the tool-preference nudge.\n", agent.DisplayName())
	}

	fmt.Println("\nDone. Restart your agent session for this to take effect.")
	return nil
}

// runSetupRemove reverses runSetupAdd: unregisters the MCP server, unwires
// the context-injection hook, and removes polyflow's nudge block — each
// step is independently idempotent (a no-op result line, not an error, when
// there's nothing to remove), so `setup --remove` is safe to run even if a
// previous setup only partially completed.
func runSetupRemove(agent setupagents.Agent, scope string) error {
	fmt.Printf("\nRemoving %s (%s scope) configuration...\n", agent.DisplayName(), scope)

	mcpResult, err := agent.RemoveMCP(scope)
	if err != nil {
		return fmt.Errorf("mcp removal: %w", err)
	}
	fmt.Println("  " + mcpResult)

	if agent.SupportsHooks() {
		hookResult, err := agent.RemoveHooks(scope)
		if err != nil {
			return fmt.Errorf("hook removal: %w", err)
		}
		fmt.Println("  " + hookResult)
	}

	if agent.SupportsNudge() {
		nudgeResult, err := setupagents.RemoveNudge(agent, scope)
		if err != nil {
			return fmt.Errorf("nudge removal: %w", err)
		}
		fmt.Println("  " + nudgeResult)
	}

	fmt.Println("\nDone.")
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
