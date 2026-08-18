package meta

// CLIFlag mirrors one pflag.Flag, walked from the live cobra tree so it can
// never drift from the actual binary (UO.4).
type CLIFlag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage,omitempty"`
}

// CLICommand mirrors one cobra.Command, recursively including subcommands
// (e.g. `polyflow config service add`).
type CLICommand struct {
	Name        string       `json:"name"`
	Short       string       `json:"short,omitempty"`
	Long        string       `json:"long,omitempty"`
	Usage       string       `json:"usage,omitempty"`
	Flags       []CLIFlag    `json:"flags,omitempty"`
	Subcommands []CLICommand `json:"subcommands,omitempty"`
}

// cliDocs is populated once, at server startup, by cmd/polyflow walking its
// own rootCmd — internal/meta cannot import cobra without cmd/polyflow (the
// only importer of internal/server) creating an import cycle, so this
// package just holds the resulting plain data (GET /api/docs/cli's source).
var cliDocs []CLICommand

// SetCLIDocs is called once by cmd/polyflow (runServe) before the server
// starts accepting requests.
func SetCLIDocs(cmds []CLICommand) { cliDocs = cmds }

// CLIDocs returns the tree set by SetCLIDocs, or nil if serve hasn't run.
func CLIDocs() []CLICommand { return cliDocs }
