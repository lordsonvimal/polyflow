//go:build ignore

package negative

var cmd = &Command{
	Use:   "serve",
	Short: "start the server",
	RunE: func(c *Command, args []string) error {
		return nil
	},
}