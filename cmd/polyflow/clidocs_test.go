package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// countRegistered walks rootCmd's actual tree the same way cobra dispatches
// commands, independent of buildCLIDocs, so the test can't share a bug with
// the code it's checking.
func countRegistered(cmd *cobra.Command) int {
	n := 0
	for _, c := range cmd.Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		n++
		n += countRegistered(c)
	}
	return n
}

func countDocs(cmds []meta.CLICommand) int {
	n := 0
	for _, c := range cmds {
		n++
		n += countDocs(c.Subcommands)
	}
	return n
}

// TestBuildCLIDocs_EveryCommandAppears is the rule-12 accounting test: every
// non-help/completion command reachable from rootCmd must show up in the
// generated docs tree walked by buildCLIDocs (UO.4's GET /api/docs/cli).
func TestBuildCLIDocs_EveryCommandAppears(t *testing.T) {
	want := countRegistered(rootCmd)
	require.Greater(t, want, 10, "sanity: rootCmd should have registered many commands by init() time")

	got := countDocs(buildCLIDocs(rootCmd))
	require.Equal(t, want, got, "every registered cobra command must appear in the generated CLI docs tree")
}

// TestBuildCLIDocs_KnownCommandsPresent pins a handful of top-level and
// nested commands by name/flag so a future refactor that silently drops a
// command's Use/Short/flags (while keeping the count equal) still fails.
func TestBuildCLIDocs_KnownCommandsPresent(t *testing.T) {
	docs := buildCLIDocs(rootCmd)

	byName := map[string]meta.CLICommand{}
	for _, c := range docs {
		byName[c.Name] = c
	}

	for _, name := range []string{"init", "index", "serve", "search", "impact", "trace", "config", "mcp", "doctor"} {
		c, ok := byName[name]
		require.Truef(t, ok, "top-level command %q missing from CLI docs", name)
		require.NotEmpty(t, c.Short, "command %q should carry its Short description", name)
	}

	cfg := byName["config"]
	var svcSub *meta.CLICommand
	for i := range cfg.Subcommands {
		if cfg.Subcommands[i].Name == "service" {
			svcSub = &cfg.Subcommands[i]
		}
	}
	require.NotNil(t, svcSub, "config should have a nested service subcommand")
	var addSub bool
	for _, c := range svcSub.Subcommands {
		if c.Name == "add" {
			addSub = true
		}
	}
	require.True(t, addSub, "config service should have a nested add subcommand (3 levels deep)")

	serve := byName["serve"]
	var hasPortFlag bool
	for _, f := range serve.Flags {
		if f.Name == "port" {
			hasPortFlag = true
		}
	}
	require.True(t, hasPortFlag, "serve command should carry its --port flag")
}
