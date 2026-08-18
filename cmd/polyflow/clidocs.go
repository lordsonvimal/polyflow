package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lordsonvimal/polyflow/internal/meta"
)

// buildCLIDocs walks the live cobra command tree into internal/meta's plain
// data shape, called once from runServe (before the server starts) and
// handed to meta.SetCLIDocs so GET /api/docs/cli is generated from the
// actual binary rather than hand-maintained (docs/plan-13-ui-ops.md UO.4).
// cobra auto-registers "help" and "completion" on the root; those aren't
// operator-facing polyflow commands, so they're skipped.
func buildCLIDocs(cmd *cobra.Command) []meta.CLICommand {
	var out []meta.CLICommand
	for _, c := range cmd.Commands() {
		if c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		out = append(out, cliCommandFrom(c))
	}
	return out
}

func cliCommandFrom(c *cobra.Command) meta.CLICommand {
	seen := map[string]bool{}
	var flags []meta.CLIFlag
	collect := func(f *pflag.Flag) {
		if seen[f.Name] {
			return
		}
		seen[f.Name] = true
		flags = append(flags, meta.CLIFlag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Default:   f.DefValue,
			Usage:     f.Usage,
		})
	}
	c.LocalFlags().VisitAll(collect)
	c.PersistentFlags().VisitAll(collect)

	return meta.CLICommand{
		Name:        c.Name(),
		Short:       c.Short,
		Long:        c.Long,
		Usage:       c.UseLine(),
		Flags:       flags,
		Subcommands: buildCLIDocs(c),
	}
}
